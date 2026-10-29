package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const secret = "hf_synthetic-secret"

var model = Repository{RepoType: "model", Namespace: "alice", Name: "test-model", Private: true}

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	c, err := New(Config{Endpoint: s.URL, Token: secret, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func jsonReply(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}
func TestConfigValidation(t *testing.T) {
	for _, endpoint := range []string{"http://example.com", "https://a:b@example.com", "https://example.com/?a=b", "https://example.com/#frag", "ftp://localhost", "https:///missing", "http://localhost.evil"} {
		if _, err := New(Config{Endpoint: endpoint, Token: secret}); err == nil {
			t.Errorf("accepted %q", endpoint)
		}
	}
	for _, endpoint := range []string{"https://huggingface.co", "http://127.0.0.1:1234", "http://[::1]:1234", "http://localhost:1234"} {
		if _, err := New(Config{Endpoint: endpoint, Token: secret}); err != nil {
			t.Errorf("%s: %v", endpoint, err)
		}
	}
	for _, token := range []string{"", " ", "bad\nheader"} {
		if _, err := New(Config{Token: token}); err == nil {
			t.Error("accepted invalid token")
		}
	}
	if _, err := New(Config{Token: secret, Timeout: -time.Second}); err == nil {
		t.Error("accepted negative timeout")
	}
}
func TestAuthPathAndRepositoryEncoding(t *testing.T) {
	var seen []string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.EscapedPath())
		if r.Header.Get("Authorization") != "Bearer "+secret {
			t.Error("missing auth")
		}
		switch r.Method {
		case "GET":
			jsonReply(w, 200, `{"id":"alice/test-model","private":false}`)
		case "POST":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["type"] != "model" || body["private"] != false || body["organization"] != "alice" {
				t.Errorf("create payload: %#v", body)
			}
			jsonReply(w, 200, `{"id":"0123456789abcdef01234567","name":"alice/test-model","url":"https://huggingface.co/alice/test-model"}`)
		case "PUT":
			b, _ := io.ReadAll(r.Body)
			if string(b) != `{"private":false}` {
				t.Errorf("visibility: %s", b)
			}
			jsonReply(w, 200, `{"private":false}`)
		case "DELETE":
			b, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(b), `"type":"model"`) {
				t.Error("missing const")
			}
			_, _ = io.WriteString(w, "OK")
		}
	})
	input := model
	input.Private = false
	repo, err := c.CreateRepository(context.Background(), input)
	if err != nil || repo.Private {
		t.Fatalf("create: %#v %v", repo, err)
	}
	if err := c.UpdateRepository(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteRepository(context.Background(), model); err != nil {
		t.Fatal(err)
	}
	if strings.Join(seen, "|") != "POST /api/repos/create|GET /api/models/alice/test-model|PUT /api/models/alice/test-model/settings|DELETE /api/repos/delete" {
		t.Fatalf("routes: %v", seen)
	}
	for _, name := range []string{"../escape", "bad/name", "a?b", "a#b", ".", "..", ""} {
		bad := model
		bad.Name = name
		if _, err := c.GetRepository(context.Background(), bad); err == nil {
			t.Errorf("unsafe path accepted %q", name)
		}
	}
	bad := model
	bad.RepoType = "dataset"
	if _, err := c.CreateRepository(context.Background(), bad); err == nil {
		t.Error("accepted non-model")
	}
}
func TestErrorsRetainMetadataWithoutSecrets(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{{401, `{"error":"` + secret + `"}`}, {403, "denied"}, {500, "{"}, {200, "{"}, {200, `{"id":"alice/test-model"}`}, {200, strings.Repeat("x", 2<<20)}} {
		t.Run(fmt.Sprint(tc.status, len(tc.body)), func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Request-Id", "req-123")
				jsonReply(w, tc.status, tc.body)
			})
			_, err := c.GetRepository(context.Background(), model)
			if err == nil {
				t.Fatal("expected error")
			}
			var api *Error
			if !errors.As(err, &api) || api.Status != tc.status || api.RequestID != "req-123" {
				t.Fatalf("metadata: %v", err)
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), tc.body) && len(tc.body) > 50 {
				t.Fatal("unsafe error")
			}
		})
	}
}
func TestAmbiguousAndConfirmedRepositoryAbsence(t *testing.T) {
	for _, tc := range []struct {
		name, auth string
		missing    bool
	}{{"alice", `{"type":"access_token","accessToken":{"role":"write"}}`, true}, {"org", `{"type":"access_token","accessToken":{"role":"write"}}`, false}, {"alice", `{"type":"access_token","accessToken":{"role":"read"}}`, false}, {"alice", `{"type":"access_token","accessToken":{"role":"fineGrained"}}`, false}, {"alice", `{"type":"unknown"}`, false}} {
		t.Run(tc.name+tc.auth, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/whoami-v2" {
					jsonReply(w, 200, `{"type":"user","name":"alice","auth":`+tc.auth+`}`)
					return
				}
				jsonReply(w, 404, `{"error":"not found"}`)
			})
			repo := model
			repo.Namespace = tc.name
			_, err := c.GetRepository(context.Background(), repo)
			if errors.Is(err, ErrNotFound) != tc.missing {
				t.Fatalf("absence: %v", err)
			}
			if !tc.missing && !errors.Is(err, ErrAmbiguousAbsence) {
				t.Fatalf("must preserve state: %v", err)
			}
		})
	}
}
func TestReadRetriesAndRetryAfterBudget(t *testing.T) {
	for _, retryAfter := range []string{"0", "120", time.Now().Add(2 * time.Minute).UTC().Format(http.TimeFormat)} {
		t.Run(retryAfter, func(t *testing.T) {
			var calls atomic.Int32
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					w.Header().Set("Retry-After", retryAfter)
					jsonReply(w, 429, `{}`)
					return
				}
				jsonReply(w, 200, `{"id":"alice/test-model","private":true}`)
			})
			_, err := c.GetRepository(context.Background(), model)
			if retryAfter == "0" {
				if err != nil || calls.Load() != 2 {
					t.Fatalf("retry failed: %v %d", err, calls.Load())
				}
			} else if err == nil || calls.Load() != 1 {
				t.Fatalf("retried too early: %v %d", err, calls.Load())
			}
		})
	}
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); jsonReply(w, 503, `{}`) })
	_, err := c.GetRepository(context.Background(), model)
	if err == nil || calls.Load() != 3 {
		t.Fatalf("unbounded/wrong retries: %d %v", calls.Load(), err)
	}
}
func TestCancellationAndTimeout(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	_, err := c.GetRepository(ctx, model)
	if err == nil || time.Since(start) > time.Second {
		t.Fatal("cancellation ignored")
	}
	c.http.Timeout = 20 * time.Millisecond
	_, err = c.GetRepository(context.Background(), model)
	if err == nil {
		t.Fatal("timeout ignored")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestRedirectOrigin(t *testing.T) {
	for _, location := range []string{"https://sub.example.com/api/models/a/b", "https://example.com:444/a", "http://example.com/a"} {
		c, err := New(Config{Endpoint: "https://example.com", Token: secret})
		if err != nil {
			t.Fatal(err)
		}
		var n int
		c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			n++
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{location}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		})
		_, err = c.GetRepository(context.Background(), model)
		if err == nil || n != 1 {
			t.Fatalf("cross origin followed: %d %v", n, err)
		}
	}
}
func TestMutationNotRetriedAndConflictRequiresImport(t *testing.T) {
	for _, status := range []int{409, 503} {
		var n int
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "POST" {
				n++
			}
			jsonReply(w, status, `{"error":"`+secret+`"}`)
		})
		_, err := c.CreateRepository(context.Background(), model)
		if err == nil || n != 1 {
			t.Fatalf("mutation replay: %d %v", n, err)
		}
		if status == 409 && (!errors.Is(err, ErrImportRequired) || !strings.Contains(err.Error(), "model/alice/test-model")) {
			t.Fatalf("missing import: %v", err)
		}
	}
}
