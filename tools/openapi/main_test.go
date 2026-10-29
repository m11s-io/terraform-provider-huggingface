package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func document(t *testing.T, raw string) map[string]any {
	t.Helper()
	var d map[string]any
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatal(err)
	}
	return d
}

// Removing the reference closure would lose Leaf and bearerAuth; copying all
// components would leak Unused into the provider contract.
func TestSubsetPreservesReferencedSchemas(t *testing.T) {
	source := document(t, `{"openapi":"3.1.0","info":{"title":"fixture","version":"1"},"security":[{"bearerAuth":[]}],"paths":{"/kept":{"parameters":[{"$ref":"#/components/parameters/ID"}],"get":{"responses":{"200":{"$ref":"#/components/responses/Result"}}},"post":{"responses":{"204":{"description":"excluded"}}}},"/excluded":{"get":{"responses":{"200":{"description":"excluded"}}}}},"components":{"parameters":{"ID":{"in":"query","name":"id","schema":{"type":"string"}}},"responses":{"Result":{"description":"ok","content":{"application/json":{"schema":{"$ref":"#/components/schemas/Root"}}}}},"schemas":{"Root":{"type":"object","properties":{"leaf":{"$ref":"#/components/schemas/Leaf"}}},"Leaf":{"type":"object","properties":{"again":{"$ref":"#/components/schemas/Root"},"flag":{"type":["boolean","null"]}},"additionalProperties":false},"Unused":{"type":"string"}},"securitySchemes":{"bearerAuth":{"type":"http","scheme":"bearer"}}}}`)
	before, _ := json.Marshal(source)
	got, err := build(source, []Operation{{Path: "/kept", Method: "get", ID: "Kept", Source: "upstream"}}, Overlay{})
	if err != nil {
		t.Fatal(err)
	}
	paths := got["paths"].(map[string]any)
	if len(paths) != 1 || paths["/kept"].(map[string]any)["post"] != nil {
		t.Fatalf("unselected operation retained: %v", paths)
	}
	if paths["/kept"].(map[string]any)["get"].(map[string]any)["operationId"] != "Kept" {
		t.Fatal("explicit operation ID missing")
	}
	components := got["components"].(map[string]any)
	schemas := components["schemas"].(map[string]any)
	if len(schemas) != 2 || !reflect.DeepEqual(schemas["Leaf"], source["components"].(map[string]any)["schemas"].(map[string]any)["Leaf"]) {
		t.Fatalf("transitive schema changed/lost: %v", schemas)
	}
	if components["parameters"] == nil || components["responses"] == nil || components["securitySchemes"] == nil {
		t.Fatal("non-schema references/security lost")
	}
	after, _ := json.Marshal(source)
	if !bytes.Equal(before, after) {
		t.Fatal("upstream mutated")
	}
}

func TestOverlayRejectsUnmatchedTarget(t *testing.T) {
	source := document(t, `{"paths":{},"components":{"schemas":{}}}`)
	_, err := build(source, nil, Overlay{Actions: []Action{{Target: "/paths/missing", Update: map[string]any{"get": map[string]any{}}}}})
	if err == nil || !strings.Contains(err.Error(), "target") {
		t.Fatalf("unmatched overlay target accepted: %v", err)
	}
}

func TestGenerationDeterministic(t *testing.T) {
	root := filepath.Join("..", "..")
	var first []byte
	for i := 0; i < 2; i++ {
		output := filepath.Join(t.TempDir(), "provider.json")
		if err := generate(filepath.Join(root, "openapi"), output); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(output)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = got
		} else if !bytes.Equal(first, got) {
			t.Fatal("provider schema differs across runs")
		}
	}
}

func TestSchemaPreservesCreateConstraints(t *testing.T) {
	root := filepath.Join("..", "..", "openapi")
	output := filepath.Join(t.TempDir(), "provider.json")
	if err := generate(root, output); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "upstream.json"))
	if err != nil {
		t.Fatal(err)
	}
	upstream := document(t, string(raw))
	raw, err = os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	got := document(t, string(raw))
	original := upstream["paths"].(map[string]any)["/api/repos/create"].(map[string]any)["post"].(map[string]any)["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"]
	schema := got["components"].(map[string]any)["schemas"].(map[string]any)["CreateRepoRequest"]
	if !reflect.DeepEqual(original, schema) {
		t.Fatal("create hoist changed oneOf/const/null/required constraints")
	}
	originalResponses := upstream["paths"].(map[string]any)["/api/repos/create"].(map[string]any)["post"].(map[string]any)["responses"]
	derivedResponses := got["paths"].(map[string]any)["/api/repos/create"].(map[string]any)["post"].(map[string]any)["responses"]
	if !reflect.DeepEqual(originalResponses, derivedResponses) {
		t.Fatal("upstream response constraints/statuses changed")
	}

	paths := got["paths"].(map[string]any)
	individual := paths["/api/models/{namespace}/{name}/settings/trusted-publishers/{publisher_id}"].(map[string]any)
	if individual["get"] != nil || individual["delete"] == nil {
		t.Fatal("invented individual publisher GET or lost DELETE")
	}
	if len(paths) != 7 {
		t.Fatalf("allowlist drift: %d paths", len(paths))
	}
}

func TestRejectRemoteAndDanglingReferences(t *testing.T) {
	for _, ref := range []string{"https://example.invalid/schema.json", "#/components/schemas/Missing"} {
		s := document(t, `{"paths":{"/fixture":{"get":{"responses":{"200":{"$ref":"`+ref+`"}}}}},"components":{"schemas":{}}}`)
		if _, err := build(s, []Operation{{Path: "/fixture", Method: "get", ID: "Fixture", Source: "upstream"}}, Overlay{}); err == nil {
			t.Fatalf("accepted reference %q", ref)
		}
	}
}

func TestOverlayDoesNotOverwriteUpstream(t *testing.T) {
	s := document(t, `{"paths":{"/fixture":{"get":{"responses":{"200":{"description":"original"}}}}}}`)
	_, err := build(s, nil, Overlay{Actions: []Action{{Target: "/paths", Update: map[string]any{"/fixture": map[string]any{"get": map[string]any{}}}}}})
	if err == nil {
		t.Fatal("overlay silently replaced upstream path")
	}
}

func TestCandidateUpdateLeavesVendoredInputsUntouched(t *testing.T) {
	root := t.TempDir()
	original := []byte(`{"openapi":"3.1.0","info":{"title":"old"},"paths":{}}`)
	provenance := []byte(`{"downloaded_schema_sha256":"original"}`)
	for name, raw := range map[string][]byte{"upstream.json": original, "provenance.json": provenance} {
		if err := os.WriteFile(filepath.Join(root, name), raw, 0644); err != nil {
			t.Fatal(err)
		}
	}
	candidate := `{"openapi":"3.1.0","info":{"title":"new"},"paths":{}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, candidate) }))
	defer server.Close()
	if err := updateCandidate(root, server.URL, server.Client()); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]byte{"upstream.json": original, "provenance.json": provenance} {
		got, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("update overwrote %s", name)
		}
	}
	got, err := os.ReadFile(filepath.Join(root, "candidate.json"))
	if err != nil || string(got) != candidate {
		t.Fatalf("candidate %q: %v", got, err)
	}
	got, err = os.ReadFile(filepath.Join(root, "candidate.diff"))
	if err != nil || !bytes.Contains(got, []byte(`+{"openapi":"3.1.0","info":{"title":"new"}`)) {
		t.Fatalf("candidate diff %q: %v", got, err)
	}
	got, err = os.ReadFile(filepath.Join(root, "candidate-provenance.json"))
	if err != nil {
		t.Fatal(err)
	}
	meta := document(t, string(got))
	sum := sha256.Sum256([]byte(candidate))
	if meta["candidate_sha256"] != hex.EncodeToString(sum[:]) || meta["upstream_url"] != server.URL || meta["retrieved_at"] == nil {
		t.Fatalf("candidate provenance missing: %s", got)
	}
}

func TestGenerationRejectsUnreviewedUpstream(t *testing.T) {
	input := t.TempDir()
	if err := os.WriteFile(filepath.Join(input, "upstream.json"), []byte(`{"paths":{}}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(input, "provenance.json"), []byte(`{"downloaded_schema_sha256":"wrong"}`), 0644); err != nil {
		t.Fatal(err)
	}
	err := generate(input, filepath.Join(input, "provider.json"))
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("unreviewed bytes accepted: %v", err)
	}
}

func TestAllowlistRejectsAmbiguity(t *testing.T) {
	s := document(t, `{"paths":{"/fixture":{"get":{"responses":{"200":{"description":"ok"}}}}}}`)
	for name, ops := range map[string][]Operation{
		"wrong-source":      {{Path: "/fixture", Method: "get", ID: "Fixture", Source: "overlay"}},
		"missing-operation": {{Path: "/fixture", Method: "post", ID: "Fixture", Source: "upstream"}},
		"duplicate-id":      {{Path: "/fixture", Method: "get", ID: "Fixture", Source: "upstream"}, {Path: "/fixture", Method: "get", ID: "Fixture", Source: "upstream"}},
		"implicit-source":   {{Path: "/fixture", Method: "get", ID: "Fixture"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := build(s, ops, Overlay{}); err == nil {
				t.Fatal("ambiguous allowlist accepted")
			}
		})
	}
}
