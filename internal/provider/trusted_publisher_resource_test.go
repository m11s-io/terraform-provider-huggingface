package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/m11s-io/terraform-provider-huggingface/internal/client"
)

func TestPublisherLifecycleDriftImportAndIndependentDelete(t *testing.T) {
	ctx := context.Background()
	bindings := map[string]map[string]any{}
	posts, deletes := 0, 0
	fail := false
	id := "000000000000000000000001"
	foreign := "000000000000000000000002"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			values := []map[string]any{}
			for _, v := range bindings {
				values = append(values, v)
			}
			json.NewEncoder(w).Encode(values)
			return
		}
		if fail {
			w.WriteHeader(403)
			return
		}
		if r.Method == "POST" {
			posts++
			var v map[string]any
			json.NewDecoder(r.Body).Decode(&v)
			v["_id"] = id
			bindings[id] = v
			json.NewEncoder(w).Encode(v)
			return
		}
		if r.Method == "DELETE" {
			deletes++
			delete(bindings, id)
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("OK"))
			return
		}
		w.WriteHeader(400)
	}))
	defer server.Close()
	var target resource.Resource
	for _, factory := range New("test")().Resources(ctx) {
		candidate := factory()
		var m resource.MetadataResponse
		candidate.Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "huggingface"}, &m)
		if m.TypeName == "huggingface_trusted_publisher" {
			target = candidate
		}
	}
	if target == nil {
		t.Fatal("publisher resource not registered")
	}
	var schema resource.SchemaResponse
	target.Schema(ctx, resource.SchemaRequest{}, &schema)
	api, err := client.New(client.Config{Endpoint: server.URL, Token: "fake"})
	if err != nil {
		t.Fatal(err)
	}
	var configured resource.ConfigureResponse
	target.(resource.ResourceWithConfigure).Configure(ctx, resource.ConfigureRequest{ProviderData: api}, &configured)
	attrs := map[string]tftypes.Value{}
	for k := range schema.Schema.Attributes {
		attrs[k] = tftypes.NewValue(tftypes.String, nil)
	}
	for k, v := range map[string]string{"repo_id": "alice/model", "repo_type": "model", "github_repository": "example/train", "branch": "main", "workflow": "publish.yml"} {
		attrs[k] = tftypes.NewValue(tftypes.String, v)
	}
	st := tfsdk.State{Schema: schema.Schema, Raw: tftypes.NewValue(schema.Schema.Type().TerraformType(ctx), attrs)}
	out := resource.CreateResponse{State: st}
	target.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: st.Schema, Raw: st.Raw}}, &out)
	if out.Diagnostics.HasError() {
		t.Fatal(out.Diagnostics)
	}
	st = out.State
	var composite string
	st.GetAttribute(ctx, path.Root("id"), &composite)
	if composite != "model/alice/model/"+id {
		t.Fatal(composite)
	}
	claims := bindings[id]["claims"].(map[string]any)
	if claims["ref"].(map[string]any)["value"] != "refs/heads/main" || claims["workflow_ref"].(map[string]any)["kind"] != "prefix" {
		t.Fatal(claims)
	}
	duplicate := resource.CreateResponse{State: tfsdk.State{Schema: st.Schema, Raw: tftypes.NewValue(st.Raw.Type(), nil)}}
	target.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: st.Schema, Raw: st.Raw}}, &duplicate)
	if !duplicate.Diagnostics.HasError() || posts != 1 {
		t.Fatal("duplicate was created", duplicate.Diagnostics)
	}
	claims["ref"].(map[string]any)["value"] = "refs/heads/changed"
	read := resource.ReadResponse{State: st}
	target.Read(ctx, resource.ReadRequest{State: st}, &read)
	if read.Diagnostics.HasError() {
		t.Fatal(read.Diagnostics)
	}
	var branch string
	read.State.GetAttribute(ctx, path.Root("branch"), &branch)
	if branch != "changed" {
		t.Fatal("drift hidden", branch)
	}
	claims["ref"].(map[string]any)["kind"] = "prefix"
	bad := resource.ReadResponse{State: st}
	target.Read(ctx, resource.ReadRequest{State: st}, &bad)
	if !bad.Diagnostics.HasError() || !bad.State.Raw.Equal(st.Raw) {
		t.Fatal("unsupported drift hidden")
	}
	claims["ref"].(map[string]any)["kind"] = "eq"
	imported := resource.ImportStateResponse{State: st}
	target.(resource.ResourceWithImportState).ImportState(ctx, resource.ImportStateRequest{ID: composite}, &imported)
	if imported.Diagnostics.HasError() || posts != 1 || deletes != 0 {
		t.Fatal("import wrote remote", imported.Diagnostics)
	}
	bindings[foreign] = map[string]any{"_id": foreign, "issuerUrl": "https://foreign.example", "claims": map[string]any{"foreign": map[string]any{"kind": "eq", "value": 7}}}
	fail = true
	deleted := resource.DeleteResponse{State: st}
	target.Delete(ctx, resource.DeleteRequest{State: st}, &deleted)
	if !deleted.Diagnostics.HasError() || !deleted.State.Raw.Equal(st.Raw) {
		t.Fatal("failed delete lost state")
	}
	fail = false
	deleted = resource.DeleteResponse{State: st}
	target.Delete(ctx, resource.DeleteRequest{State: st}, &deleted)
	if deleted.Diagnostics.HasError() || bindings[foreign] == nil || bindings[id] != nil {
		t.Fatal("unrelated binding lost", deleted.Diagnostics)
	}
	missing := resource.ReadResponse{State: st}
	target.Read(ctx, resource.ReadRequest{State: st}, &missing)
	if missing.Diagnostics.HasError() || !missing.State.Raw.IsNull() {
		t.Fatal("missing publisher retained", missing.Diagnostics)
	}
	for _, badID := range []string{"alice/model/id", "model/alice/model/extra/extra", "model/alice/model/nothex"} {
		imp := resource.ImportStateResponse{State: st}
		target.(resource.ResourceWithImportState).ImportState(ctx, resource.ImportStateRequest{ID: badID}, &imp)
		if !imp.Diagnostics.HasError() {
			t.Fatal("invalid import", badID)
		}
	}
}
