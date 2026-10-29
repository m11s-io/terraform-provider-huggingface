package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/m11s-io/terraform-provider-huggingface/internal/client"
)

// These tests catch lost authoritative reads, accidental artifact ownership,
// omission of false visibility, write retries, and ownership loss on failed IO.
type repositoryFake struct {
	sync.Mutex
	exists, private               bool
	failWrite, denied, lostCreate bool
	role, sha                     string
	calls                         []string
	updates                       []map[string]any
}

func (f *repositoryFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.Lock()
	defer f.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("Authorization") != "Bearer hf_fake" {
		w.WriteHeader(401)
		return
	}
	switch {
	case r.Method == "POST" && r.URL.Path == "/api/repos/create":
		if f.exists {
			w.WriteHeader(409)
			return
		}
		var b struct{ Private bool }
		_ = json.NewDecoder(r.Body).Decode(&b)
		f.exists = true
		f.private = b.Private
		if f.lostCreate {
			c, _, _ := w.(http.Hijacker).Hijack()
			_ = c.Close()
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "remote-id", "name": "alice/model", "url": "https://example.test/alice/model"})
	case r.Method == "GET" && r.URL.Path == "/api/models/alice/model":
		if f.denied || !f.exists {
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "alice/model", "private": f.private, "sha": f.sha})
	case r.Method == "GET" && r.URL.Path == "/api/whoami-v2":
		role := f.role
		if role == "" {
			role = "write"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"type": "user", "name": "alice", "auth": map[string]any{"type": "access_token", "accessToken": map[string]any{"role": role}}})
	case r.Method == "PUT" && r.URL.Path == "/api/models/alice/model/settings":
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		f.updates = append(f.updates, b)
		if f.failWrite {
			w.WriteHeader(403)
			return
		}
		f.private = b["private"].(bool)
		_ = json.NewEncoder(w).Encode(b)
	case r.Method == "DELETE" && r.URL.Path == "/api/repos/delete":
		if f.failWrite {
			w.WriteHeader(403)
			return
		}
		f.exists = false
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("OK"))
	default:
		w.WriteHeader(400)
	}
}
func repositoryTestResource(t *testing.T, f *repositoryFake) (resource.Resource, tfsdk.State) {
	t.Helper()
	factories := New("test")().Resources(context.Background())
	var r resource.Resource
	for _, factory := range factories {
		candidate := factory()
		var metadata resource.MetadataResponse
		candidate.Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "huggingface"}, &metadata)
		if metadata.TypeName == "huggingface_repository" {
			r = candidate
		}
	}
	if r == nil {
		t.Fatal("repository resource is not registered")
	}
	var s resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &s)
	server := httptest.NewServer(f)
	t.Cleanup(server.Close)
	api, err := client.New(client.Config{Endpoint: server.URL, Token: "hf_fake"})
	if err != nil {
		t.Fatal(err)
	}
	var cfg resource.ConfigureResponse
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: api}, &cfg)
	if cfg.Diagnostics.HasError() {
		t.Fatal(cfg.Diagnostics)
	}
	st := tfsdk.State{Schema: s.Schema}
	attrs := map[string]tftypes.Value{}
	for k := range s.Schema.Attributes {
		attrs[k] = tftypes.NewValue(tftypes.String, nil)
	}
	attrs["private"] = tftypes.NewValue(tftypes.Bool, true)
	for k, v := range map[string]string{"namespace": "alice", "name": "model", "repo_type": "model", "deletion_policy": "retain"} {
		attrs[k] = tftypes.NewValue(tftypes.String, v)
	}
	st.Raw = tftypes.NewValue(s.Schema.Type().TerraformType(context.Background()), attrs)
	return r, st
}
func createRepository(t *testing.T, r resource.Resource, st tfsdk.State) tfsdk.State {
	t.Helper()
	out := resource.CreateResponse{State: st}
	r.Create(context.Background(), resource.CreateRequest{Plan: tfsdk.Plan{Raw: st.Raw, Schema: st.Schema}}, &out)
	if out.Diagnostics.HasError() {
		t.Fatal(out.Diagnostics)
	}
	return out.State
}
func TestRepositoryCreateRefreshAndArtifactCommitsUnchanged(t *testing.T) {
	f := &repositoryFake{sha: "ci-commit-one"}
	r, st := repositoryTestResource(t, f)
	st = createRepository(t, r, st)
	var id, repoID, url string
	st.GetAttribute(context.Background(), path.Root("id"), &id)
	st.GetAttribute(context.Background(), path.Root("repo_id"), &repoID)
	st.GetAttribute(context.Background(), path.Root("url"), &url)
	if id != "model/alice/model" || repoID != "alice/model" || !strings.HasSuffix(url, "/alice/model") {
		t.Fatalf("identity %q %q %q", id, repoID, url)
	}
	if len(f.calls) != 2 {
		t.Fatalf("create must POST then one authoritative GET: %v", f.calls)
	}
	f.sha = "ci-commit-two"
	out := resource.ReadResponse{State: st}
	r.Read(context.Background(), resource.ReadRequest{State: st}, &out)
	if out.Diagnostics.HasError() || !st.Raw.Equal(out.State.Raw) {
		t.Fatalf("artifact commit changed managed state: %v", out.Diagnostics)
	}
}
func TestRepositoryVisibilityFalseAndFailedWritePreserveState(t *testing.T) {
	f := &repositoryFake{}
	r, st := repositoryTestResource(t, f)
	st = createRepository(t, r, st)
	plan := tfsdk.Plan{Raw: st.Raw, Schema: st.Schema}
	plan.SetAttribute(context.Background(), path.Root("private"), false)
	out := resource.UpdateResponse{State: st}
	r.Update(context.Background(), resource.UpdateRequest{State: st, Plan: plan}, &out)
	if out.Diagnostics.HasError() || f.private || !reflect.DeepEqual(f.updates, []map[string]any{{"private": false}}) {
		t.Fatalf("false visibility: %v %v", f.updates, out.Diagnostics)
	}
	st = out.State
	f.failWrite = true
	plan.SetAttribute(context.Background(), path.Root("private"), true)
	out = resource.UpdateResponse{State: st}
	r.Update(context.Background(), resource.UpdateRequest{State: st, Plan: plan}, &out)
	if !out.Diagnostics.HasError() || !st.Raw.Equal(out.State.Raw) {
		t.Fatal("failed update lost previous state")
	}
}
func TestRepositoryMissingAndPermissionLoss(t *testing.T) {
	for _, ambiguous := range []bool{false, true} {
		t.Run(map[bool]string{false: "external deletion", true: "permission loss"}[ambiguous], func(t *testing.T) {
			f := &repositoryFake{}
			r, st := repositoryTestResource(t, f)
			st = createRepository(t, r, st)
			f.exists = false
			if ambiguous {
				f.role = "read"
			}
			out := resource.ReadResponse{State: st}
			r.Read(context.Background(), resource.ReadRequest{State: st}, &out)
			if ambiguous {
				if !out.Diagnostics.HasError() || !st.Raw.Equal(out.State.Raw) {
					t.Fatal("ambiguous absence removed state")
				}
			} else if !out.State.Raw.IsNull() || out.Diagnostics.HasError() {
				t.Fatalf("confirmed absence retained state: %v", out.Diagnostics)
			}
		})
	}
}
func TestRepositoryDeletePoliciesAndFailure(t *testing.T) {
	for _, policy := range []string{"retain", "delete", "failed delete"} {
		t.Run(policy, func(t *testing.T) {
			f := &repositoryFake{}
			r, st := repositoryTestResource(t, f)
			st = createRepository(t, r, st)
			before := len(f.calls)
			if policy != "retain" {
				st.SetAttribute(context.Background(), path.Root("deletion_policy"), "delete")
			}
			f.failWrite = policy == "failed delete"
			out := resource.DeleteResponse{State: st}
			r.Delete(context.Background(), resource.DeleteRequest{State: st}, &out)
			if policy == "retain" {
				if len(f.calls) != before || !f.exists {
					t.Fatal("retain sent HTTP")
				}
			} else if f.calls[len(f.calls)-1] != "DELETE /api/repos/delete" {
				t.Fatal(f.calls)
			}
			if policy == "failed delete" {
				if !out.Diagnostics.HasError() || !st.Raw.Equal(out.State.Raw) {
					t.Fatal("failed delete lost state")
				}
			} else if out.Diagnostics.HasError() {
				t.Fatal(out.Diagnostics)
			}
		})
	}
}
func TestRepositoryConflictAndLostCreateRequireImport(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "409", true: "lost response"}[lost], func(t *testing.T) {
			f := &repositoryFake{exists: !lost, lostCreate: lost}
			r, st := repositoryTestResource(t, f)
			out := resource.CreateResponse{State: tfsdk.State{Schema: st.Schema, Raw: tftypes.NewValue(st.Raw.Type(), nil)}}
			r.Create(context.Background(), resource.CreateRequest{Plan: tfsdk.Plan{Raw: st.Raw, Schema: st.Schema}}, &out)
			if !out.Diagnostics.HasError() || !out.State.Raw.IsNull() || !strings.Contains(out.Diagnostics[0].Detail(), "import") {
				t.Fatalf("create adopted repository: %v", out.Diagnostics)
			}
		})
	}
}
func TestRepositoryImportReadOnlyAndRetain(t *testing.T) {
	f := &repositoryFake{exists: true, private: false}
	r, st := repositoryTestResource(t, f)
	out := resource.ImportStateResponse{State: st}
	r.(resource.ResourceWithImportState).ImportState(context.Background(), resource.ImportStateRequest{ID: "model/alice/model"}, &out)
	if out.Diagnostics.HasError() || len(f.calls) != 0 {
		t.Fatal("import mutated remote", out.Diagnostics)
	}
	var policy string
	out.State.GetAttribute(context.Background(), path.Root("deletion_policy"), &policy)
	if policy != "retain" {
		t.Fatal(policy)
	}
	read := resource.ReadResponse{State: out.State}
	r.Read(context.Background(), resource.ReadRequest{State: out.State}, &read)
	if read.Diagnostics.HasError() {
		t.Fatal(read.Diagnostics)
	}
	for _, bad := range []string{"alice/model", "dataset/alice/model", "model/alice/model/extra", "model/../model"} {
		invalid := resource.ImportStateResponse{State: st}
		r.(resource.ResourceWithImportState).ImportState(context.Background(), resource.ImportStateRequest{ID: bad}, &invalid)
		if !invalid.Diagnostics.HasError() {
			t.Fatalf("accepted %q", bad)
		}
	}
}
