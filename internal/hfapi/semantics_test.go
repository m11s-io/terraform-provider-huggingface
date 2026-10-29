package hfapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/oapi-codegen/nullable"
)

func ptr[T any](v T) *T { return &v }
func checkJSON(t *testing.T, v any, want string) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != want {
		t.Fatalf("got %s, want %s", b, want)
	}
	t.Log(string(b))
}

func TestNullableAndUnionEncoding(t *testing.T) {
	for _, tc := range []struct {
		name string
		body CreateRepoRequest1
		want string
	}{
		{"omitted", CreateRepoRequest1{Name: "probe"}, `{"name":"probe"}`},
		{"false", CreateRepoRequest1{Name: "probe", Private: nullable.NewNullableWithValue(false)}, `{"name":"probe","private":false}`},
		{"true", CreateRepoRequest1{Name: "probe", Private: nullable.NewNullableWithValue(true)}, `{"name":"probe","private":true}`},
		{"null", CreateRepoRequest1{Name: "probe", Private: nullable.NewNullNullable[bool](), Organization: nullable.NewNullNullable[string](), ResourceGroupId: nullable.NewNullNullable[string]()}, `{"name":"probe","organization":null,"private":null,"resourceGroupId":null}`},
		{"values", CreateRepoRequest1{Name: "probe", Organization: nullable.NewNullableWithValue("org"), ResourceGroupId: nullable.NewNullableWithValue("0123456789abcdef01234567"), Type: ptr(CreateRepoRequest1TypeModel)}, `{"name":"probe","organization":"org","resourceGroupId":"0123456789abcdef01234567","type":"model"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkJSON(t, tc.body, tc.want)
			var union CreateRepoRequest
			if err := union.FromCreateRepoRequest1(tc.body); err != nil {
				t.Fatal(err)
			}
			req, err := NewCreateRepositoryRequest("https://example.invalid", CreateRepositoryJSONRequestBody(union))
			if err != nil {
				t.Fatal(err)
			}
			b, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatal(err)
			}
			if string(b) != tc.want {
				t.Fatalf("request=%s want=%s", b, tc.want)
			}
			var decoded CreateRepoRequest1
			if err := json.Unmarshal(b, &decoded); err != nil {
				t.Fatal(err)
			}
			checkJSON(t, decoded, tc.want)
			var decodedUnion CreateRepoRequest
			if err := json.Unmarshal(b, &decodedUnion); err != nil {
				t.Fatal(err)
			}
			member, err := decodedUnion.AsCreateRepoRequest1()
			if err != nil {
				t.Fatal(err)
			}
			checkJSON(t, member, tc.want)
			checkJSON(t, decodedUnion, tc.want)
		})
	}
}

func TestBoolFalseNotDropped(t *testing.T) {
	for _, tc := range []struct {
		body UpdateModelSettingsJSONRequestBody
		want string
	}{
		{UpdateModelSettingsJSONRequestBody{}, `{}`},
		{UpdateModelSettingsJSONRequestBody{Private: ptr(false)}, `{"private":false}`},
		{UpdateModelSettingsJSONRequestBody{Private: ptr(true)}, `{"private":true}`},
	} {
		req, err := NewUpdateModelSettingsRequest("https://example.invalid", "org", "repo", tc.body)
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != tc.want {
			t.Fatalf("got=%s want=%s", b, tc.want)
		}
		t.Log(string(b))
	}
}

func TestAnyOfConstValidPayloads(t *testing.T) {
	var gated UpdateModelSettingsJSONBody_Gated
	if err := gated.FromUpdateModelSettingsJSONBodyGated0(UpdateModelSettingsJSONBodyGated0False); err != nil {
		t.Fatal(err)
	}
	checkJSON(t, UpdateModelSettingsJSONBody{Gated: &gated}, `{"gated":false}`)
	var decodedGated UpdateModelSettingsJSONBody_Gated
	if err := json.Unmarshal([]byte(`false`), &decodedGated); err != nil {
		t.Fatal(err)
	}
	boolMember, err := decodedGated.AsUpdateModelSettingsJSONBodyGated0()
	if err != nil || boolMember != UpdateModelSettingsJSONBodyGated0False {
		t.Fatalf("false union round trip: %v %v", boolMember, err)
	}
	for _, v := range []UpdateModelSettingsJSONBodyGated1{UpdateModelSettingsJSONBodyGated1Auto, UpdateModelSettingsJSONBodyGated1Manual} {
		if err := gated.FromUpdateModelSettingsJSONBodyGated1(v); err != nil {
			t.Fatal(err)
		}
		checkJSON(t, gated, fmt.Sprintf("%q", v))
		raw, err := json.Marshal(gated)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &decodedGated); err != nil {
			t.Fatal(err)
		}
		member, err := decodedGated.AsUpdateModelSettingsJSONBodyGated1()
		if err != nil || member != v {
			t.Fatalf("string union round trip: %v %v", member, err)
		}
	}
	var sharing UpdateModelSettingsJSONBody_GatedProfileSharing
	if err := sharing.FromUpdateModelSettingsJSONBodyGatedProfileSharing0(UpdateModelSettingsJSONBodyGatedProfileSharing0{Username: UpdateModelSettingsJSONBodyGatedProfileSharing0UsernameTrue, Email: false}); err != nil {
		t.Fatal(err)
	}
	checkJSON(t, sharing, `{"email":false,"username":true}`)
	if err := sharing.FromUpdateModelSettingsJSONBodyGatedProfileSharing1(UpdateModelSettingsJSONBodyGatedProfileSharing1{Username: UpdateModelSettingsJSONBodyGatedProfileSharing1UsernameFalse, Email: UpdateModelSettingsJSONBodyGatedProfileSharing1EmailFalse}); err != nil {
		t.Fatal(err)
	}
	checkJSON(t, sharing, `{"email":false,"username":false}`)
}

// Generated client/types preserve union payloads but do not validate schemas.
// The test passes only when this limitation is observed, so it cannot be
// mistaken for a conformance test proving that invalid instances are rejected.
func TestCharacterizeNoAutomaticConstOrOneOfValidation(t *testing.T) {
	invalidConst := UpdateModelSettingsJSONBodyGated0(true)
	if invalidConst.Valid() {
		t.Fatal("const helper accepts true")
	}
	var gated UpdateModelSettingsJSONBody_Gated
	if err := gated.FromUpdateModelSettingsJSONBodyGated0(invalidConst); err != nil {
		t.Fatalf("unexpected constructor validation: %v", err)
	}
	checkJSON(t, gated, `true`)
	var union CreateRepoRequest
	bad := CreateRepoRequest1{Name: "probe", Type: ptr(CreateRepoRequest1Type("bogus"))}
	if bad.Type.Valid() {
		t.Fatal("const helper accepts bogus type")
	}
	if err := union.FromCreateRepoRequest1(bad); err != nil {
		t.Fatalf("unexpected constructor validation: %v", err)
	}
	checkJSON(t, union, `{"name":"probe","type":"bogus"}`)
	for _, raw := range []string{`{"name":"probe","type":"bogus"}`, `{"type":"model"}`, `{"name":"probe","type":"model"}`} {
		if err := json.Unmarshal([]byte(raw), &union); err != nil {
			t.Fatal(err)
		}
		if _, err := union.AsCreateRepoRequest0(); err != nil {
			t.Fatalf("dataset accessor rejected non-dataset payload: %v", err)
		}
		checkJSON(t, union, raw)
	}
	t.Log("LIMITATION: constructors, accessors and JSON unmarshal do not enforce const, required fields, or oneOf member validity")
}

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestPathEncodingOnTransport(t *testing.T) {
	for _, tc := range []struct{ namespace, repo string }{
		{"org", "repo"}, {"org/child", "a/b"}, {"org space", "repo space"}, {"org?query#fragment", "r%2Fs"}, {"ü", "模型"},
	} {
		t.Run(tc.namespace+"-"+tc.repo, func(t *testing.T) {
			want := "/api/models/" + url.PathEscape(tc.namespace) + "/" + url.PathEscape(tc.repo) + "/settings"
			client, err := NewClient("https://example.invalid", WithHTTPClient(doerFunc(func(req *http.Request) (*http.Response, error) {
				if got := req.URL.EscapedPath(); got != want {
					t.Errorf("escaped path=%q want=%q", got, want)
				}
				if req.URL.RawQuery != "" || req.URL.Fragment != "" || req.URL.Host != "example.invalid" {
					t.Errorf("input escaped into URL structure: %s", req.URL)
				}
				t.Log(req.URL.String())
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`)), Header: http.Header{}}, nil
			})))
			if err != nil {
				t.Fatal(err)
			}
			rsp, err := client.UpdateModelSettings(context.Background(), tc.namespace, tc.repo, UpdateModelSettingsJSONRequestBody{})
			if err != nil {
				t.Fatal(err)
			}
			rsp.Body.Close()
		})
	}
}

func response(status int, contentType, body string) *http.Response {
	return &http.Response{StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)), Header: http.Header{"Content-Type": {contentType}, "X-Request-Id": {"fixture-request"}}, Body: io.NopCloser(strings.NewReader(body))}
}
func TestUnknownHTTPStatusRetained(t *testing.T) {
	for _, tc := range []struct {
		status            int
		contentType, body string
	}{
		{200, "application/json", `{"id":"0123456789abcdef01234567","name":"probe","url":"https://example.invalid/org/probe","filesCopyPending":false}`},
		{409, "application/json", `{"error":"exists","url":"https://example.invalid/org/probe"}`},
		{403, "application/json", `{"error":"forbidden"}`},
		{429, "application/json", `{"error":"rate limited"}`},
		{503, "text/plain", "unavailable"},
		{409, "text/html", "<p>conflict</p>"},
	} {
		t.Run(fmt.Sprint(tc.status)+tc.contentType, func(t *testing.T) {
			rsp, err := ParseCreateRepositoryResponse(response(tc.status, tc.contentType, tc.body))
			if err != nil {
				t.Fatal(err)
			}
			if rsp.StatusCode() != tc.status || string(rsp.Body) != tc.body || rsp.Status() != fmt.Sprintf("%d %s", tc.status, http.StatusText(tc.status)) || rsp.HTTPResponse.Header.Get("X-Request-Id") != "fixture-request" {
				t.Fatalf("status/body/header lost: %#v", rsp)
			}
			if tc.status == 200 && (rsp.JSON200 == nil || rsp.JSON200.FilesCopyPending == nil || *rsp.JSON200.FilesCopyPending) {
				t.Fatal("success false field lost")
			}
			if tc.status == 409 && tc.contentType == "application/json" && (rsp.JSON409 == nil || rsp.JSON409.Error != "exists" || rsp.JSON200 != nil) {
				t.Fatal("conflict not retained distinctly")
			}
			t.Logf("status=%d raw=%q typed200=%t typed409=%t", rsp.StatusCode(), rsp.Body, rsp.JSON200 != nil, rsp.JSON409 != nil)
		})
	}
}

func TestCharacterizeMalformedTypedJSONLosesResponseWrapper(t *testing.T) {
	rsp, err := ParseCreateRepositoryResponse(response(409, "application/json", `{"error":`))
	if err == nil || rsp != nil {
		t.Fatalf("expected nil wrapper on malformed typed JSON: rsp=%#v err=%v", rsp, err)
	}
	t.Logf("LIMITATION: malformed recognized JSON status returns nil wrapper, err=%v", err)
}
