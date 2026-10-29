package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

var githubInput = GitHubPublisher{GitHubRepository: "org/repo", Branch: "main", Workflow: "publish.yaml"}

const publisherJSON = `{"_id":"0123456789abcdef01234567","issuerUrl":"https://token.actions.githubusercontent.com","claims":{"repository":{"kind":"eq","value":"org/repo"},"ref":{"kind":"eq","value":"refs/heads/main"},"workflow_ref":{"kind":"prefix","value":"org/repo/.github/workflows/publish.yaml@"}},"configuredBy":{},"createdAt":"2026-10-29T00:00:00Z"}`

func TestCreateResponseLostRequiresImport(t *testing.T) {
	var persisted bool
	var posts int
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			if persisted {
				jsonReply(w, 200, "["+publisherJSON+"]")
			} else {
				jsonReply(w, 200, "[]")
			}
			return
		}
		posts++
		persisted = true
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		want := `{"issuerUrl":"https://token.actions.githubusercontent.com","claims":{"repository":{"kind":"eq","value":"org/repo"},"ref":{"kind":"eq","value":"refs/heads/main"},"workflow_ref":{"kind":"prefix","value":"org/repo/.github/workflows/publish.yaml@"}}}`
		var expected map[string]any
		_ = json.Unmarshal([]byte(want), &expected)
		got, _ := json.Marshal(body)
		wantBytes, _ := json.Marshal(expected)
		if string(got) != string(wantBytes) {
			t.Errorf("wrong claims: %s", got)
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	})
	result, err := c.CreatePublisher(context.Background(), model, githubInput)
	var detail *Error
	if err == nil || result.ID != "" || !errors.As(err, &detail) || !detail.MayHaveSucceeded || !strings.Contains(err.Error(), "model/alice/test-model/0123456789abcdef01234567") {
		t.Fatalf("unsafe recovery: %#v %v", result, err)
	}
	result, err = c.CreatePublisher(context.Background(), model, githubInput)
	if !errors.Is(err, ErrImportRequired) || result.ID != "" || posts != 1 {
		t.Fatalf("second apply duplicated/adopted: posts=%d result=%#v err=%v", posts, result, err)
	}
}
func TestRepositoryCreateResponseLostRequiresImport(t *testing.T) {
	var persisted bool
	var posts int
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			if persisted {
				jsonReply(w, 200, `{"id":"alice/test-model","private":true}`)
			} else {
				jsonReply(w, 404, `{}`)
			}
			return
		}
		posts++
		if persisted {
			jsonReply(w, 409, `{}`)
			return
		}
		persisted = true
		conn, _, _ := w.(http.Hijacker).Hijack()
		_ = conn.Close()
	})
	repo, err := c.CreateRepository(context.Background(), model)
	var detail *Error
	if repo.Name != "" || !errors.As(err, &detail) || !detail.MayHaveSucceeded || !strings.Contains(err.Error(), "model/alice/test-model") {
		t.Fatalf("missing recovery: %#v %v", repo, err)
	}
	_, err = c.CreateRepository(context.Background(), model)
	if !errors.Is(err, ErrImportRequired) || posts != 2 {
		t.Fatalf("silent adoption/replay: %d %v", posts, err)
	}
}
func TestPublisherListPaginationAndGenericClaims(t *testing.T) {
	var calls []string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.RequestURI())
		if r.URL.RawQuery == "" {
			w.Header().Set("Link", `<?page=2>; rel="next"`)
			jsonReply(w, 200, `[{"_id":"aaaaaaaaaaaaaaaaaaaaaaaa","issuerUrl":"https://other.example","claims":{"other":{"kind":"custom","value":"value","extra":true}},"configuredBy":{},"createdAt":"now"}]`)
			return
		}
		jsonReply(w, 200, "["+publisherJSON+"]")
	})
	p, err := c.GetPublisher(context.Background(), model, "0123456789abcdef01234567")
	if err != nil || p.Claims["ref"].Value != "refs/heads/main" {
		t.Fatalf("lookup: %#v %v", p, err)
	}
	if len(calls) != 2 || !strings.HasSuffix(calls[1], "?page=2") {
		t.Fatalf("not paginated: %v", calls)
	}
	list, err := c.ListPublishers(context.Background(), model)
	if err != nil || len(list) != 2 || list[0].Claims["other"].AdditionalProperties["extra"] != true {
		t.Fatalf("dropped unrelated publisher: %#v %v", list, err)
	}
	_, err = c.GetPublisher(context.Background(), model, "bbbbbbbbbbbbbbbbbbbbbbbb")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("complete list absence: %v", err)
	}
}
func TestPublisherListFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		body, link string
		status     int
	}{{`{"items":[]}`, "", 200}, {`null`, "", 200}, {`[{"_id":"bad"}]`, "", 200}, {"[]", `<https://other.example/p>; rel="next"`, 200}, {"[]", `<?page=2>; rel="next"`, 200}, {"[]", `garbage`, 200}, {"[]", "", 206}, {"[]", "", 404}} {
		t.Run(fmt.Sprintf("%d %s %s", tc.status, tc.body, tc.link), func(t *testing.T) {
			var n int
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				n++
				if tc.link != "" {
					w.Header().Set("Link", tc.link)
				}
				jsonReply(w, tc.status, tc.body)
			})
			_, err := c.CreatePublisher(context.Background(), model, githubInput)
			if err == nil {
				t.Fatal("incomplete list permitted create")
			}
			if n > 4 {
				t.Fatalf("unbounded loop: %d", n)
			}
		})
	}
}
func TestPublisherCreateReadAndSelectiveDelete(t *testing.T) {
	var deleted string
	var posts int
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			jsonReply(w, 200, `[{"_id":"aaaaaaaaaaaaaaaaaaaaaaaa","issuerUrl":"https://other.example","claims":{},"configuredBy":{},"createdAt":"now"}]`)
		case "POST":
			posts++
			jsonReply(w, 200, publisherJSON)
		case "DELETE":
			deleted = r.URL.Path
			_, _ = io.WriteString(w, "OK")
		}
	})
	p, err := c.CreatePublisher(context.Background(), model, githubInput)
	if err != nil || p.ID != "0123456789abcdef01234567" {
		t.Fatalf("create: %#v %v", p, err)
	}
	if err := c.DeletePublisher(context.Background(), model, p.ID); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(deleted, "/trusted-publishers/0123456789abcdef01234567") || posts != 1 {
		t.Fatalf("incorrect scope: %s %d", deleted, posts)
	}
	for _, bad := range []GitHubPublisher{{}, {"org/repo", "refs/heads/main", "publish.yaml"}, {"org/repo", "main", "../publish.yaml"}, {"org/repo", "main", "publish.yaml@main"}, {"org/repo", "main", "publish.txt"}, {"bad", "main", "publish.yaml"}, {"org/repo", "bad branch", "publish.yaml"}} {
		if _, err := c.CreatePublisher(context.Background(), model, bad); err == nil {
			t.Errorf("invalid claims accepted: %#v", bad)
		}
	}
	if err := c.DeletePublisher(context.Background(), model, "../other"); err == nil {
		t.Error("unsafe ID")
	}
}
