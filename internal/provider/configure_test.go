package provider

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/m11s-io/terraform-provider-huggingface/internal/client"
)

func TestProviderConfigurationPrecedence(t *testing.T) {
	t.Setenv("HF_TOKEN", "hf_synthetic-env")
	t.Setenv("HF_ENDPOINT", "http://localhost:1234")
	for _, tc := range []struct {
		name                     string
		token, endpoint, timeout any
		wantError                bool
	}{
		{"null uses env", nil, nil, nil, false},
		{"explicit beats env", "hf_explicit", "https://example.com", int64(12), false},
		{"empty token", "", nil, nil, true},
		{"unknown token", tftypes.UnknownValue, nil, nil, true},
		{"unknown endpoint", nil, tftypes.UnknownValue, nil, true},
		{"empty endpoint", nil, "", nil, true},
		{"unknown timeout", nil, nil, tftypes.UnknownValue, true},
		{"zero timeout", nil, nil, int64(0), true},
		{"negative timeout", nil, nil, int64(-1), true},
		{"overflow timeout", nil, nil, int64(math.MaxInt64), true},
		{"bad endpoint", nil, "http://other.example", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := New("test")()
			var schema provider.SchemaResponse
			p.Schema(context.Background(), provider.SchemaRequest{}, &schema)
			raw := tftypes.NewValue(tftypes.Object{AttributeTypes: map[string]tftypes.Type{"token": tftypes.String, "endpoint": tftypes.String, "request_timeout_seconds": tftypes.Number}}, map[string]tftypes.Value{"token": tftypes.NewValue(tftypes.String, tc.token), "endpoint": tftypes.NewValue(tftypes.String, tc.endpoint), "request_timeout_seconds": tftypes.NewValue(tftypes.Number, tc.timeout)})
			var resp provider.ConfigureResponse
			p.Configure(context.Background(), provider.ConfigureRequest{Config: tfsdk.Config{Raw: raw, Schema: schema.Schema}}, &resp)
			if resp.Diagnostics.HasError() != tc.wantError {
				t.Fatalf("diagnostics: %v", resp.Diagnostics)
			}
			for _, d := range resp.Diagnostics {
				if strings.Contains(d.Detail(), "hf_synthetic-env") || strings.Contains(d.Detail(), "hf_explicit") {
					t.Fatal("credential leaked")
				}
			}
			if !tc.wantError {
				if _, ok := resp.ResourceData.(*client.Client); !ok {
					t.Fatalf("ResourceData: %T", resp.ResourceData)
				}
				if resp.DataSourceData != nil {
					t.Error("unexpected data source config")
				}
			}
		})
	}
	t.Setenv("HF_TOKEN", "")
	p := New("test")()
	var schema provider.SchemaResponse
	p.Schema(context.Background(), provider.SchemaRequest{}, &schema)
	raw := tftypes.NewValue(tftypes.Object{AttributeTypes: map[string]tftypes.Type{"token": tftypes.String, "endpoint": tftypes.String, "request_timeout_seconds": tftypes.Number}}, map[string]tftypes.Value{"token": tftypes.NewValue(tftypes.String, nil), "endpoint": tftypes.NewValue(tftypes.String, nil), "request_timeout_seconds": tftypes.NewValue(tftypes.Number, nil)})
	var resp provider.ConfigureResponse
	p.Configure(context.Background(), provider.ConfigureRequest{Config: tfsdk.Config{Raw: raw, Schema: schema.Schema}}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("missing token accepted")
	}
}

func TestConfiguredClientUsesSelectedIdentityAndEndpoint(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "environment", true: "explicit"}[explicit], func(t *testing.T) {
			wantToken := "hf_env"
			if explicit {
				wantToken = "hf_explicit"
			}
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+wantToken || r.URL.Path != "/api/models/alice/model" {
					t.Errorf("selected config did not reach request: %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"alice/model","private":true}`))
			}))
			defer s.Close()
			t.Setenv("HF_TOKEN", "hf_env")
			t.Setenv("HF_ENDPOINT", s.URL)
			var token, endpoint any
			if explicit {
				token, endpoint = wantToken, s.URL
				t.Setenv("HF_ENDPOINT", "http://invalid.example")
			}
			p := New("test")()
			var schema provider.SchemaResponse
			p.Schema(context.Background(), provider.SchemaRequest{}, &schema)
			raw := tftypes.NewValue(tftypes.Object{AttributeTypes: map[string]tftypes.Type{"token": tftypes.String, "endpoint": tftypes.String, "request_timeout_seconds": tftypes.Number}}, map[string]tftypes.Value{"token": tftypes.NewValue(tftypes.String, token), "endpoint": tftypes.NewValue(tftypes.String, endpoint), "request_timeout_seconds": tftypes.NewValue(tftypes.Number, nil)})
			var resp provider.ConfigureResponse
			p.Configure(context.Background(), provider.ConfigureRequest{Config: tfsdk.Config{Raw: raw, Schema: schema.Schema}}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatal(resp.Diagnostics)
			}
			api, ok := resp.ResourceData.(*client.Client)
			if !ok {
				t.Fatalf("ResourceData: %T", resp.ResourceData)
			}
			if _, err := api.GetRepository(context.Background(), client.Repository{RepoType: "model", Namespace: "alice", Name: "model"}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
