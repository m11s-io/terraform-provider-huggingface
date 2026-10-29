package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/m11s-io/terraform-provider-huggingface/internal/hfapi"
)

const GitHubIssuer = "https://token.actions.githubusercontent.com"
const maxPages = 100
const maxPublishers = 10000

// Claim retains foreign claim values and unknown properties. Only GitHub create
// inputs are typed; arbitrary returned issuers/claims must survive list reads.
type Claim struct {
	Kind                 string
	Value                any
	AdditionalProperties map[string]any
}

// Publisher exposes immutable identity and remote claims for resource drift checks.
// RawClaims additionally preserves unsupported claim shapes on unrelated issuers.
type Publisher struct {
	ID, Issuer string
	Claims     map[string]Claim
	RawClaims  map[string]json.RawMessage
}
type GitHubPublisher struct{ GitHubRepository, Branch, Workflow string }

// Validate checks the typed GitHub inputs without performing any HTTP request.
func (g GitHubPublisher) Validate() error { _, err := g.payload(); return err }

var publisherID = regexp.MustCompile(`^[a-fA-F0-9]{24}$`)
var workflow = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*\.ya?ml$`)

func (g GitHubPublisher) payload() (hfapi.CreatePublisherRequest, error) {
	parts := strings.Split(g.GitHubRepository, "/")
	if len(parts) != 2 || !segment.MatchString(parts[0]) || !segment.MatchString(parts[1]) || !workflow.MatchString(g.Workflow) || !validBranch(g.Branch) {
		return hfapi.CreatePublisherRequest{}, errors.New("publisher requires owner/repo, a short branch name and a workflow YAML filename")
	}
	return hfapi.CreatePublisherRequest{IssuerUrl: GitHubIssuer, Claims: map[string]hfapi.PublisherClaim{
		"repository":   {Kind: "eq", Value: g.GitHubRepository},
		"ref":          {Kind: "eq", Value: "refs/heads/" + g.Branch},
		"workflow_ref": {Kind: "prefix", Value: g.GitHubRepository + "/.github/workflows/" + g.Workflow + "@"},
	}}, nil
}
func validBranch(branch string) bool {
	if branch == "" || strings.HasPrefix(branch, "refs/") || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") || strings.HasSuffix(branch, ".") || strings.Contains(branch, "..") || strings.Contains(branch, "@{") || strings.Contains(branch, "//") || branch == "@" {
		return false
	}
	for _, part := range strings.Split(branch, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return strings.IndexFunc(branch, func(r rune) bool { return r <= 32 || r == 127 || strings.ContainsRune("~^:?*[\\", r) }) < 0
}

func (c *Client) ListPublishers(ctx context.Context, r Repository) ([]Publisher, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	rsp, err := c.api.ListTrustedPublishers(ctx, r.Namespace, r.Name)
	results := []Publisher{}
	seenURLs := map[string]bool{}
	seenIDs := map[string]bool{}
	totalBytes := 0
	expectedTotal := -1
	for page := 0; page < maxPages; page++ {
		if err != nil {
			return nil, err
		}
		if rsp.StatusCode == 404 {
			_ = rsp.Body.Close()
			// A collection 404 is about the parent, not proof a publisher is absent.
			_, parentErr := c.GetRepository(ctx, r)
			if errors.Is(parentErr, ErrNotFound) {
				return nil, parentErr
			}
			e := c.issue("list publishers", rsp, "parent lookup or publisher access is ambiguous; preserve state")
			e.Kind = ErrAmbiguousAbsence
			return nil, e
		}
		if rsp.StatusCode != 200 {
			return nil, c.issue("list publishers", rsp, "unexpected collection response")
		}
		if rsp.Request == nil || rsp.Request.URL == nil {
			return nil, c.issue("list publishers", rsp, "missing collection request metadata")
		}
		current := rsp.Request.URL.String()
		if seenURLs[current] {
			return nil, c.issue("list publishers", rsp, "pagination loop detected")
		}
		seenURLs[current] = true
		body, readErr := io.ReadAll(rsp.Body)
		_ = rsp.Body.Close()
		totalBytes += len(body)
		if readErr != nil || totalBytes > 8*maxBody {
			return nil, c.issue("list publishers", rsp, "collection exceeds the total body limit")
		}
		if !strings.Contains(strings.ToLower(rsp.Header.Get("Content-Type")), "application/json") || !bytes.HasPrefix(bytes.TrimSpace(body), []byte("[")) {
			return nil, c.issue("list publishers", rsp, "invalid or unknown collection envelope")
		}
		var raw []json.RawMessage
		if json.Unmarshal(body, &raw) != nil {
			return nil, c.issue("list publishers", rsp, "invalid collection JSON")
		}
		for _, item := range raw {
			p, decodeErr := decodePublisher(item)
			if decodeErr != nil || seenIDs[p.ID] {
				return nil, c.issue("list publishers", rsp, "invalid publisher or repeated immutable ID")
			}
			seenIDs[p.ID] = true
			results = append(results, p)
			if len(results) > maxPublishers {
				return nil, c.issue("list publishers", rsp, "collection exceeds the item limit")
			}
		}
		if count := rsp.Header.Get("X-Total-Count"); count != "" {
			n, e := strconv.Atoi(count)
			if e != nil || n < 0 || (expectedTotal >= 0 && expectedTotal != n) {
				return nil, c.issue("list publishers", rsp, "invalid collection total")
			}
			expectedTotal = n
		}
		next, linkErr := nextLink(rsp.Header.Values("Link"), rsp.Request.URL, c.endpoint)
		if linkErr != nil {
			return nil, c.issue("list publishers", rsp, "unsafe or malformed pagination link")
		}
		if rsp.Header.Get("Content-Range") != "" || rsp.Header.Get("X-Next-Page") != "" || strings.EqualFold(rsp.Header.Get("X-Has-More"), "true") || strings.EqualFold(rsp.Header.Get("X-Truncated"), "true") {
			return nil, c.issue("list publishers", rsp, "unsupported collection truncation signal")
		}
		if next == nil {
			if expectedTotal >= 0 && expectedTotal != len(results) {
				return nil, c.issue("list publishers", rsp, "collection total indicates truncation")
			}
			return results, nil
		}
		if seenURLs[next.String()] {
			return nil, c.issue("list publishers", rsp, "pagination loop detected")
		}
		if page+1 >= maxPages {
			return nil, c.issue("list publishers", rsp, "collection exceeds the page limit")
		}
		req, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, next.String(), nil)
		if requestErr != nil {
			return nil, c.issue("list publishers", rsp, "invalid pagination request")
		}
		rsp, err = c.Do(req)
	}
	return nil, c.issue("list publishers", rsp, "collection exceeds the page limit")
}
func decodePublisher(body []byte) (Publisher, error) {
	var raw struct {
		ID     string                     `json:"_id"`
		Issuer string                     `json:"issuerUrl"`
		Claims map[string]json.RawMessage `json:"claims"`
	}
	if json.Unmarshal(body, &raw) != nil || !publisherID.MatchString(raw.ID) || raw.Issuer == "" || raw.Claims == nil {
		return Publisher{}, errors.New("invalid publisher")
	}
	p := Publisher{ID: raw.ID, Issuer: raw.Issuer, RawClaims: raw.Claims, Claims: map[string]Claim{}}
	// The verified GitHub contract uses typed operators/string values. Preserve
	// unsupported remote values as raw data so resource reads can diagnose drift.
	for name, value := range raw.Claims {
		var fields map[string]any
		if json.Unmarshal(value, &fields) != nil || fields == nil {
			continue
		}
		kind, _ := fields["kind"].(string)
		claim := Claim{Kind: kind, Value: fields["value"], AdditionalProperties: map[string]any{}}
		for key, v := range fields {
			if key != "kind" && key != "value" {
				claim.AdditionalProperties[key] = v
			}
		}
		p.Claims[name] = claim
	}
	return p, nil
}
func (c *Client) GetPublisher(ctx context.Context, r Repository, id string) (Publisher, error) {
	if !publisherID.MatchString(id) {
		return Publisher{}, errors.New("publisher ID must be 24 hex characters")
	}
	list, err := c.ListPublishers(ctx, r)
	if err != nil {
		return Publisher{}, err
	}
	for _, p := range list {
		if p.ID == id {
			return p, nil
		}
	}
	e := c.issue("read publisher", nil, "publisher is absent from the complete collection")
	e.Kind = ErrNotFound
	return Publisher{}, e
}
func equivalent(p Publisher, want hfapi.CreatePublisherRequest) bool {
	if p.Issuer != string(want.IssuerUrl) || len(p.RawClaims) != len(want.Claims) {
		return false
	}
	for name, claim := range want.Claims {
		got, ok := p.Claims[name]
		value, isString := got.Value.(string)
		if !ok || !isString || got.Kind != string(claim.Kind) || value != claim.Value || len(got.AdditionalProperties) != 0 {
			return false
		}
	}
	return true
}
func (c *Client) CreatePublisher(ctx context.Context, r Repository, g GitHubPublisher) (Publisher, error) {
	if err := r.validate(); err != nil {
		return Publisher{}, err
	}
	body, err := g.payload()
	if err != nil {
		return Publisher{}, err
	}
	list, err := c.ListPublishers(ctx, r)
	if err != nil {
		return Publisher{}, err
	}
	ids := matchingIDs(c, r, list, body)
	if len(ids) > 0 {
		e := c.issue("create publisher", nil, "equivalent binding already exists; explicitly import it")
		e.Kind = ErrImportRequired
		e.ImportIDs = ids
		return Publisher{}, e
	}
	rsp, err := c.api.CreateTrustedPublisher(ctx, r.Namespace, r.Name, body)
	parsed, parseErr := parse(c, "create publisher", rsp, err, hfapi.ParseCreateTrustedPublisherResponse)
	var result Publisher
	if parseErr == nil {
		result, err = decodePublisher(parsed.Body)
		if parsed.JSON200 == nil || err != nil || !equivalent(result, body) {
			parseErr = c.issue("create publisher", rsp, "invalid publisher create response")
		}
	}
	if parseErr != nil {
		var e *Error
		if errors.As(parseErr, &e) && (e.Status == 0 || e.Status == 200 || e.Status >= 500 || e.MayHaveSucceeded) {
			e.MayHaveSucceeded = true
			if recovered, lookupErr := c.ListPublishers(ctx, r); lookupErr == nil {
				e.ImportIDs = matchingIDs(c, r, recovered, body)
			}
		}
		return Publisher{}, parseErr
	}
	return result, nil
}
func matchingIDs(c *Client, r Repository, list []Publisher, body hfapi.CreatePublisherRequest) []string {
	var ids []string
	for _, p := range list {
		if equivalent(p, body) {
			ids = append(ids, c.safe(r.ID()+"/"+p.ID))
		}
	}
	return ids
}
func (c *Client) DeletePublisher(ctx context.Context, r Repository, id string) error {
	if err := r.validate(); err != nil {
		return err
	}
	if !publisherID.MatchString(id) {
		return errors.New("publisher ID must be 24 hex characters")
	}
	rsp, err := c.api.DeleteTrustedPublisher(ctx, r.Namespace, r.Name, id)
	if err == nil && rsp.StatusCode == 404 {
		_ = rsp.Body.Close()
		_, lookupErr := c.GetPublisher(ctx, r, id)
		if errors.Is(lookupErr, ErrNotFound) {
			return nil
		}
		if lookupErr != nil {
			return lookupErr
		}
		return c.issue("delete publisher", rsp, "binding still exists after delete returned 404")
	}
	parsed, err := parse(c, "delete publisher", rsp, err, hfapi.ParseDeleteTrustedPublisherResponse)
	if err != nil {
		return err
	}
	if string(parsed.Body) != "OK" {
		return c.issue("delete publisher", rsp, "invalid delete acknowledgement")
	}
	return nil
}

// nextLink accepts RFC-style Link entries with quoted rel values. Unknown
// relations are ignored; malformed/ambiguous links fail closed. Never invent a
// query cursor and never forward credentials to another scheme/host/port.
func nextLink(headers []string, current, origin *url.URL) (*url.URL, error) {
	var next *url.URL
	for _, header := range headers {
		entries, err := splitLinks(header)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			entry = strings.TrimSpace(entry)
			end := strings.IndexByte(entry, '>')
			if !strings.HasPrefix(entry, "<") || end < 2 {
				return nil, errors.New("malformed Link")
			}
			target := entry[1:end]
			params := strings.TrimSpace(entry[end+1:])
			rel := ""
			for params != "" {
				if params[0] != ';' {
					return nil, errors.New("malformed Link parameters")
				}
				params = strings.TrimSpace(params[1:])
				eq := strings.IndexByte(params, '=')
				if eq <= 0 {
					return nil, errors.New("malformed Link parameter")
				}
				key := strings.TrimSpace(params[:eq])
				params = strings.TrimSpace(params[eq+1:])
				value := ""
				if strings.HasPrefix(params, "\"") {
					params = params[1:]
					close := strings.IndexByte(params, '"')
					if close < 0 {
						return nil, errors.New("unterminated Link quote")
					}
					value = params[:close]
					params = strings.TrimSpace(params[close+1:])
				} else {
					end := strings.IndexByte(params, ';')
					if end < 0 {
						value = params
						params = ""
					} else {
						value = strings.TrimSpace(params[:end])
						params = params[end:]
					}
				}
				if key == "rel" {
					if rel != "" {
						return nil, errors.New("duplicate relation")
					}
					rel = value
				}
			}
			for _, relation := range strings.Fields(rel) {
				if relation == "next" {
					candidate, err := current.Parse(target)
					if err != nil || !sameOrigin(origin, candidate) || candidate.Fragment != "" || candidate.Opaque != "" || next != nil {
						return nil, errors.New("unsafe next link")
					}
					next = candidate
				}
			}
		}
	}
	return next, nil
}
func splitLinks(value string) ([]string, error) {
	var out []string
	start := 0
	angle := false
	quoted := false
	for i, r := range value {
		switch r {
		case '<':
			if !quoted {
				if angle {
					return nil, errors.New("nested Link")
				}
				angle = true
			}
		case '>':
			if !quoted {
				if !angle {
					return nil, errors.New("unmatched Link")
				}
				angle = false
			}
		case '"':
			if !angle {
				quoted = !quoted
			}
		case ',':
			if !angle && !quoted {
				out = append(out, value[start:i])
				start = i + 1
			}
		}
	}
	if angle || quoted {
		return nil, errors.New("unterminated Link")
	}
	out = append(out, value[start:])
	return out, nil
}
