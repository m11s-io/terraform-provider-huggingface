package hfapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestOverlayTypedRequestPayloads(t *testing.T) {
	publisher := CreatePublisherRequest{
		IssuerUrl: CreatePublisherRequestIssuerUrl("https://token.actions.githubusercontent.com"),
		Claims: map[string]PublisherClaim{
			"repository":   {Kind: PublisherClaimKind("eq"), Value: "m11s-io/docker-images"},
			"ref":          {Kind: PublisherClaimKind("eq"), Value: "refs/heads/main"},
			"workflow_ref": {Kind: PublisherClaimKind("prefix"), Value: "m11s-io/docker-images/.github/workflows/timestral-openvino.yaml@"},
		},
	}
	req, err := NewCreateTrustedPublisherRequest("https://example.invalid", "org", "repo", publisher)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"claims":{"ref":{"kind":"eq","value":"refs/heads/main"},"repository":{"kind":"eq","value":"m11s-io/docker-images"},"workflow_ref":{"kind":"prefix","value":"m11s-io/docker-images/.github/workflows/timestral-openvino.yaml@"}},"issuerUrl":"https://token.actions.githubusercontent.com"}`
	if string(raw) != want || req.Method != http.MethodPost || req.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("publisher request: %s %s %s", req.Method, req.Header, raw)
	}
	req, err = NewDeleteRepositoryRequest("https://example.invalid", DeleteRepoRequest{Name: "probe", Organization: "org", Type: DeleteRepoRequestType("model")})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"name":"probe","organization":"org","type":"model"}` || req.Method != http.MethodDelete {
		t.Fatalf("delete request: %s %s", req.Method, raw)
	}
}

func TestOverlayPathEncoding(t *testing.T) {
	const server = "https://example.invalid"
	const namespace = "org/child?query#fragment"
	const name = "r%2Fs ü"
	const publisherID = "binding/id?query#fragment"
	const model = "/api/models/org%2Fchild%3Fquery%23fragment/r%252Fs%20%C3%BC"
	for _, tc := range []struct {
		name, method, path string
		request            func() (*http.Request, error)
	}{
		{"model", http.MethodGet, model, func() (*http.Request, error) { return NewGetModelRequest(server, namespace, name) }},
		{"list", http.MethodGet, model + "/settings/trusted-publishers", func() (*http.Request, error) { return NewListTrustedPublishersRequest(server, namespace, name) }},
		{"create", http.MethodPost, model + "/settings/trusted-publishers", func() (*http.Request, error) {
			return NewCreateTrustedPublisherRequest(server, namespace, name, CreatePublisherRequest{})
		}},
		{"delete", http.MethodDelete, model + "/settings/trusted-publishers/binding%2Fid%3Fquery%23fragment", func() (*http.Request, error) {
			return NewDeleteTrustedPublisherRequest(server, namespace, name, publisherID)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := tc.request()
			if err != nil {
				t.Fatal(err)
			}
			if req.Method != tc.method || req.URL.EscapedPath() != tc.path || req.URL.Host != "example.invalid" || req.URL.RawQuery != "" || req.URL.Fragment != "" {
				t.Fatalf("path escaped into URL structure: %s %s", req.Method, req.URL)
			}
		})
	}
}

func assertSemanticJSON(t *testing.T, got any, want json.RawMessage) {
	t.Helper()
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var a, b any
	if err = json.Unmarshal(encoded, &a); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(want, &b); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("response metadata lost:\ngot %s\nwant %s", encoded, want)
	}
}

func TestVerifiedOverlayFixturesRoundTrip(t *testing.T) {
	for _, file := range []string{"publisher-machine-token-probe.json", "repository-and-pagination-probe.json"} {
		raw, err := os.ReadFile("../../docs/api-contract/sanitized-fixtures/" + file)
		if err != nil {
			t.Fatal(err)
		}
		var events []struct {
			Label, Method, Path string
			Status              int
			Response            json.RawMessage
		}
		if err = json.Unmarshal(raw, &events); err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			t.Run(file+"/"+event.Label, func(t *testing.T) {
				switch {
				case event.Method == "GET" && event.Status == 200 && containsPublisherCollection(event.Path):
					parsed, err := ParseListTrustedPublishersResponse(response(200, "application/json", string(event.Response)))
					if err != nil {
						t.Fatal(err)
					}
					if parsed.JSON200 == nil {
						t.Fatal("array not recognized")
					}
					assertSemanticJSON(t, *parsed.JSON200, event.Response)
				case event.Method == "POST" && event.Status == 200 && containsPublisherCollection(event.Path):
					parsed, err := ParseCreateTrustedPublisherResponse(response(200, "application/json", string(event.Response)))
					if err != nil {
						t.Fatal(err)
					}
					if parsed.JSON200 == nil || parsed.JSON200.ConfiguredBy == nil || parsed.JSON200.CreatedAt == "" || parsed.JSON200.UnderscoreId == "" {
						t.Fatal("publisher required response fields lost")
					}
					assertSemanticJSON(t, parsed.JSON200, event.Response)
				case event.Method == "GET" && event.Status == 200 && len(event.Path) > 12 && event.Path[:12] == "/api/models/":
					parsed, err := ParseGetModelResponse(response(200, "application/json", string(event.Response)))
					if err != nil {
						t.Fatal(err)
					}
					if parsed.JSON200 == nil {
						t.Fatal("model info missing")
					}
					assertSemanticJSON(t, parsed.JSON200, event.Response)
				}
			})
		}
	}
}

func containsPublisherCollection(path string) bool {
	return strings.Contains(path, "/settings/trusted-publishers")
}

func TestOverlayStatusAndUnknownFieldsRetained(t *testing.T) {
	for _, tc := range []struct {
		name  string
		parse func(*http.Response) (int, []byte, http.Header, error)
	}{
		{"get-model", func(r *http.Response) (int, []byte, http.Header, error) {
			v, e := ParseGetModelResponse(r)
			if e != nil {
				return 0, nil, nil, e
			}
			return v.StatusCode(), v.Body, v.HTTPResponse.Header, nil
		}},
		{"list-publishers", func(r *http.Response) (int, []byte, http.Header, error) {
			v, e := ParseListTrustedPublishersResponse(r)
			if e != nil {
				return 0, nil, nil, e
			}
			return v.StatusCode(), v.Body, v.HTTPResponse.Header, nil
		}},
		{"create-publisher", func(r *http.Response) (int, []byte, http.Header, error) {
			v, e := ParseCreateTrustedPublisherResponse(r)
			if e != nil {
				return 0, nil, nil, e
			}
			return v.StatusCode(), v.Body, v.HTTPResponse.Header, nil
		}},
		{"delete-publisher", func(r *http.Response) (int, []byte, http.Header, error) {
			v, e := ParseDeleteTrustedPublisherResponse(r)
			if e != nil {
				return 0, nil, nil, e
			}
			return v.StatusCode(), v.Body, v.HTTPResponse.Header, nil
		}},
		{"delete-repository", func(r *http.Response) (int, []byte, http.Header, error) {
			v, e := ParseDeleteRepositoryResponse(r)
			if e != nil {
				return 0, nil, nil, e
			}
			return v.StatusCode(), v.Body, v.HTTPResponse.Header, nil
		}},
	} {
		for _, status := range []int{403, 429, 503} {
			t.Run(fmt.Sprintf("%s/%d", tc.name, status), func(t *testing.T) {
				const raw = `{"error":"unmodeled","future":false}`
				code, body, headers, err := tc.parse(response(status, "application/json", raw))
				if err != nil || code != status || string(body) != raw || headers.Get("X-Request-Id") != "fixture-request" {
					t.Fatalf("metadata lost: code=%d body=%s headers=%v err=%v", code, body, headers, err)
				}
			})
		}
	}
	deleted, err := ParseDeleteRepositoryResponse(response(200, "text/plain", "OK"))
	if err != nil || deleted.StatusCode() != 200 || string(deleted.Body) != "OK" {
		t.Fatalf("model delete: %v %v", deleted, err)
	}
	binding, err := ParseDeleteTrustedPublisherResponse(response(200, "text/plain", "OK"))
	if err != nil || binding.StatusCode() != 200 || string(binding.Body) != "OK" {
		t.Fatalf("publisher delete: %v %v", binding, err)
	}
	missing, err := ParseDeleteTrustedPublisherResponse(response(404, "application/json", `{"error":"Publisher not found","future":false}`))
	if err != nil || missing.JSON404 == nil || missing.JSON404.Error != "Publisher not found" || missing.JSON404.AdditionalProperties["future"] != false {
		t.Fatalf("404 error fields lost: %v %v", missing, err)
	}
	const raw = `{"_id":"0123456789abcdef01234567","claims":{"future_claim":{"kind":"eq","value":"future","extra":false}},"configuredBy":{"name":"synthetic","future":{"arbitrary":true}},"createdAt":"2026-10-29T14:05:01.215Z","issuerUrl":"https://token.actions.githubusercontent.com","future":{"nested":false}}`
	created, err := ParseCreateTrustedPublisherResponse(response(200, "application/json", raw))
	if err != nil {
		t.Fatal(err)
	}
	assertSemanticJSON(t, created.JSON200, json.RawMessage(raw))

}
