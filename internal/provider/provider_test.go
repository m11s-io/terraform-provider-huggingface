package provider

import (
	"context"
	"testing"

	framework "github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

// Catch a factory that drops the release version or exposes an invalid schema
// through the protocol consumed by Terraform and OpenTofu.
func TestBootstrapProviderProtocol6(t *testing.T) {
	ctx := context.Background()
	p := New("0.0.1-test")()
	var metadata framework.MetadataResponse
	p.Metadata(ctx, framework.MetadataRequest{}, &metadata)
	if metadata.TypeName != "huggingface" || metadata.Version != "0.0.1-test" {
		t.Fatalf("unexpected provider metadata: %#v", metadata)
	}

	server := providerserver.NewProtocol6(p)()
	response, err := server.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, diagnostic := range response.Diagnostics {
		if diagnostic.Severity == tfprotov6.DiagnosticSeverityError {
			t.Fatalf("invalid protocol 6 schema: %s: %s", diagnostic.Summary, diagnostic.Detail)
		}
	}
	if response.Provider == nil || response.Provider.Block == nil {
		t.Fatal("protocol 6 server did not expose the provider schema")
	}
}
