// Package client provides the private, model-only Hugging Face domain client.
package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/m11s-io/terraform-provider-huggingface/internal/hfapi"
)

const (
	DefaultEndpoint = "https://huggingface.co"
	DefaultTimeout  = 30 * time.Second
	maxBody         = 1 << 20
	maxReadAttempts = 3
	retryBudget     = 2 * time.Second
)

// Config configures a client. Zero Endpoint/Timeout use the documented defaults.
// Environment lookup belongs to provider configuration, never the HTTP client.
type Config struct {
	Endpoint, Token string
	Timeout         time.Duration
}

type Client struct {
	http     *http.Client
	api      *hfapi.Client
	endpoint *url.URL
	token    string
	timeout  time.Duration
}

var (
	ErrNotFound         = errors.New("confirmed absence")
	ErrAmbiguousAbsence = errors.New("ambiguous absence")
	ErrImportRequired   = errors.New("explicit import required")
)

// Error is safe to include in diagnostics. Bodies, URLs, and underlying transport
// errors are deliberately excluded. ErrNotFound alone permits removing state.
type Error struct {
	Operation        string
	Status           int
	RequestID        string
	MayHaveSucceeded bool
	ImportIDs        []string
	Kind             error
	detail           string
}

func (e *Error) Error() string {
	msg := e.Operation + ": " + e.detail
	if e.Status != 0 {
		msg += fmt.Sprintf(" (HTTP %d", e.Status)
		if e.RequestID != "" {
			msg += ", request ID " + e.RequestID
		}
		msg += ")"
	}
	if e.MayHaveSucceeded {
		msg += "; the operation may have succeeded; do not retry creation; inspect the remote object and explicitly import it"
	}
	if len(e.ImportIDs) > 0 {
		msg += "; import ID(s): " + strings.Join(e.ImportIDs, ", ")
	}
	return msg
}
func (e *Error) Is(target error) bool { return e.Kind == target }

func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.Token) == "" || strings.IndexFunc(cfg.Token, func(r rune) bool { return r <= 32 || r == 127 }) >= 0 {
		return nil, errors.New("token must be nonempty and contain no whitespace or control characters")
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = DefaultEndpoint
	}
	endpoint, err := url.Parse(cfg.Endpoint)
	if err != nil || endpoint.Host == "" || endpoint.Opaque != "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" || strings.Contains(cfg.Endpoint, "#") {
		return nil, errors.New("endpoint must be an absolute HTTPS URL without userinfo, query or fragment")
	}
	loopback := strings.EqualFold(endpoint.Hostname(), "localhost")
	if ip := net.ParseIP(endpoint.Hostname()); ip != nil {
		loopback = ip.IsLoopback()
	}
	if endpoint.Scheme != "https" && !(endpoint.Scheme == "http" && loopback) {
		return nil, errors.New("endpoint must use HTTPS (HTTP is allowed only for loopback)")
	}
	if endpoint.Port() != "" {
		port, err := strconv.Atoi(endpoint.Port())
		if err != nil || port < 1 || port > 65535 {
			return nil, errors.New("endpoint port is invalid")
		}
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.Timeout < 0 {
		return nil, errors.New("timeout must be positive")
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/"
	c := &Client{endpoint: endpoint, token: cfg.Token, timeout: cfg.Timeout}
	c.http = &http.Client{Timeout: cfg.Timeout, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("redirect limit exceeded")
		}
		if len(via) == 0 || via[0].Method != http.MethodGet || !sameOrigin(endpoint, req.URL) {
			return errors.New("unsafe redirect rejected")
		}
		return nil
	}}
	c.api, err = hfapi.NewClient(endpoint.String(), hfapi.WithHTTPClient(c))
	if err != nil {
		return nil, errors.New("could not initialize API client")
	}
	return c, nil
}
func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host) && b.User == nil
}
func (c *Client) issue(op string, rsp *http.Response, detail string) *Error {
	e := &Error{Operation: op, detail: detail}
	if rsp != nil {
		e.Status = rsp.StatusCode
		e.RequestID = c.safe(rsp.Header.Get("X-Request-Id"))
		if e.RequestID == "" {
			e.RequestID = c.safe(rsp.Header.Get("X-Amzn-Trace-Id"))
		}
	}
	return e
}

var unsafeDiagnostic = regexp.MustCompile(`[^a-zA-Z0-9_.:/=, -]`)

func (c *Client) safe(s string) string {
	s = strings.ReplaceAll(s, c.token, "[redacted]")
	s = unsafeDiagnostic.ReplaceAllString(s, "")
	if len(s) > 160 {
		s = s[:160]
	}
	return s
}

// Do is the generated client's HTTP boundary. It captures status and headers
// before any parser runs, bounds every body, and retries only read operations.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if !sameOrigin(c.endpoint, req.URL) {
		return nil, c.issue("HTTP", nil, "cross-origin request rejected")
	}
	ctx, cancel := context.WithTimeout(req.Context(), c.timeout)
	defer cancel()
	req = req.Clone(ctx)
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	read := req.Method == http.MethodGet
	var waited time.Duration
	for attempt := 0; attempt < maxReadAttempts; attempt++ {
		rsp, err := c.http.Do(req)
		if err != nil {
			if rsp != nil && rsp.Body != nil {
				_ = rsp.Body.Close()
			}
			if read && rsp == nil && ctx.Err() == nil && attempt+1 < maxReadAttempts {
				if err = waitFor(ctx, time.Duration(attempt+1)*100*time.Millisecond); err == nil {
					continue
				}
			}
			e := c.issue("HTTP", rsp, "request failed; check connectivity and authorization")
			if ctx.Err() != nil {
				e.detail = "request canceled or timed out"
			}
			e.MayHaveSucceeded = !read
			return nil, e
		}
		body, readErr := io.ReadAll(io.LimitReader(rsp.Body, maxBody+1))
		_ = rsp.Body.Close()
		if readErr != nil || len(body) > maxBody {
			e := c.issue("HTTP", rsp, "response body is incomplete or exceeds the size limit")
			e.MayHaveSucceeded = !read
			return nil, e
		}
		rsp.Body = io.NopCloser(bytes.NewReader(body))
		if read && retryable(rsp.StatusCode) && attempt+1 < maxReadAttempts {
			delay := time.Duration(attempt+1) * 100 * time.Millisecond
			if value := rsp.Header.Get("Retry-After"); value != "" {
				var ok bool
				delay, ok = retryAfter(value, time.Now())
				if !ok {
					return nil, c.issue("HTTP", rsp, "invalid Retry-After; read was not retried")
				}
			}
			if delay > retryBudget-waited {
				return nil, c.issue("HTTP", rsp, "Retry-After exceeds the retry budget; read was not retried")
			}
			waited += delay
			if err := waitFor(ctx, delay); err != nil {
				return nil, c.issue("HTTP", rsp, "request canceled or timed out")
			}
			continue
		}
		return rsp, nil
	}
	panic("unreachable")
}
func retryable(status int) bool {
	return status == 429 || status == 500 || status == 502 || status == 503 || status == 504
}
func retryAfter(value string, now time.Time) (time.Duration, bool) {
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds < 0 || seconds > int64(retryBudget/time.Second) {
			return retryBudget + time.Second, seconds >= 0
		}
		return time.Duration(seconds) * time.Second, true
	}
	date, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	delay := date.Sub(now)
	if delay < 0 {
		delay = 0
	}
	return delay, true
}
func waitFor(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// parse invokes the generated parser only after the bounded HTTP response has
// been captured. A malformed recognized JSON status retains HTTP metadata.
func parse[T any](c *Client, op string, rsp *http.Response, err error, parser func(*http.Response) (*T, error)) (*T, error) {
	if err != nil {
		return nil, err
	}
	if rsp.StatusCode != 200 {
		e := c.issue(op, rsp, "unexpected response; check access permissions")
		if rsp.Request != nil && rsp.Request.Method != "GET" && (rsp.StatusCode >= 500 || (rsp.StatusCode >= 200 && rsp.StatusCode < 400) || rsp.StatusCode == 408) {
			e.MayHaveSucceeded = true
		}
		return nil, e
	}
	value, err := parser(rsp)
	if err != nil {
		return nil, c.issue(op, rsp, "invalid response JSON")
	}
	return value, nil
}
