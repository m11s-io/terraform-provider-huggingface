// Package provider contains the Hugging Face Terraform/OpenTofu provider.
package provider

import (
	"context"
	"math"
	"os"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/m11s-io/terraform-provider-huggingface/internal/client"
)

var _ provider.Provider = (*huggingfaceProvider)(nil)

type huggingfaceProvider struct {
	version string
}

// New returns an independent provider factory for the plugin server.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &huggingfaceProvider{version: version}
	}
}

func (p *huggingfaceProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "huggingface"
	resp.Version = p.version
}

func (p *huggingfaceProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Configure Hugging Face model repository and trusted-publisher management with a bootstrap credential.",
		Attributes: map[string]schema.Attribute{
			"token":                   schema.StringAttribute{Optional: true, Sensitive: true, MarkdownDescription: "Bootstrap access token. Falls back to HF_TOKEN only when unset. A legacy Write personal access token is proven for owning-account CRUD."},
			"endpoint":                schema.StringAttribute{Optional: true, MarkdownDescription: "API endpoint. Falls back to HF_ENDPOINT when unset, then https://huggingface.co. HTTPS is required except for loopback testing."},
			"request_timeout_seconds": schema.Int64Attribute{Optional: true, MarkdownDescription: "Positive request timeout in seconds. Defaults to 30."},
		},
	}
}

type providerModel struct {
	Token    types.String `tfsdk:"token"`
	Endpoint types.String `tfsdk:"endpoint"`
	Timeout  types.Int64  `tfsdk:"request_timeout_seconds"`
}

func (p *huggingfaceProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if config.Token.IsUnknown() || config.Endpoint.IsUnknown() || config.Timeout.IsUnknown() {
		resp.Diagnostics.AddError("Unknown provider configuration", "Token, endpoint and request_timeout_seconds must be known before configuring the provider. Environment fallback applies only to unset values.")
		return
	}
	token := config.Token.ValueString()
	if config.Token.IsNull() {
		token = os.Getenv("HF_TOKEN")
	}
	endpoint := config.Endpoint.ValueString()
	if config.Endpoint.IsNull() {
		endpoint = os.Getenv("HF_ENDPOINT")
		if endpoint == "" {
			endpoint = client.DefaultEndpoint
		}
	} else if endpoint == "" {
		resp.Diagnostics.AddError("Invalid endpoint", "An explicitly configured endpoint must be nonempty.")
		return
	}
	seconds := int64(30)
	if !config.Timeout.IsNull() {
		seconds = config.Timeout.ValueInt64()
	}
	if seconds <= 0 || seconds > math.MaxInt64/int64(time.Second) {
		resp.Diagnostics.AddError("Invalid request timeout", "request_timeout_seconds must be a positive integer that fits in a Go duration.")
		return
	}
	api, err := client.New(client.Config{Endpoint: endpoint, Token: token, Timeout: time.Duration(seconds) * time.Second})
	if err != nil {
		resp.Diagnostics.AddError("Invalid provider configuration", err.Error())
		return
	}
	resp.ResourceData = api
}

func (p *huggingfaceProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{NewRepositoryResource, NewTrustedPublisherResource}
}

func (p *huggingfaceProvider) DataSources(context.Context) []func() datasource.DataSource {
	return nil
}
