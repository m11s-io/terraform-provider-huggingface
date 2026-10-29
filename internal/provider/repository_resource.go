package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/m11s-io/terraform-provider-huggingface/internal/client"
)

type stringRule struct {
	description string
	check       func(string) bool
}

func (v stringRule) Description(context.Context) string             { return v.description }
func (v stringRule) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }
func (v stringRule) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if !v.check(req.ConfigValue.ValueString()) {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid value", v.description)
	}
}

var identitySegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func identityValidator() validator.String {
	return stringRule{"Use a nonempty namespace/name segment starting with a letter or digit and containing only letters, digits, dots, underscores or hyphens.", identitySegment.MatchString}
}
func oneOf(values ...string) validator.String {
	return stringRule{"Value must be one of: " + strings.Join(values, ", "), func(s string) bool {
		for _, v := range values {
			if s == v {
				return true
			}
		}
		return false
	}}
}

type repositoryResource struct{ api *client.Client }

// Derived identities remain known for visibility/local-policy updates. They
// deliberately remain unknown when any identity input changes or is unknown,
// so replacement never carries an old repository ID into the new object.
type stableRepositoryIdentity struct{}

func (stableRepositoryIdentity) Description(context.Context) string {
	return "Preserve derived identity only when all repository identity inputs are unchanged."
}
func (m stableRepositoryIdentity) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}
func (stableRepositoryIdentity) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if !req.PlanValue.IsUnknown() || req.StateValue.IsNull() || req.StateValue.IsUnknown() {
		return
	}
	for _, name := range []string{"namespace", "name", "repo_type"} {
		var planned, prior types.String
		resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root(name), &planned)...)
		resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root(name), &prior)...)
		if resp.Diagnostics.HasError() || planned.IsNull() || planned.IsUnknown() || !planned.Equal(prior) {
			return
		}
	}
	resp.PlanValue = req.StateValue
}

type repositoryModel struct {
	Namespace      types.String `tfsdk:"namespace"`
	Name           types.String `tfsdk:"name"`
	RepoType       types.String `tfsdk:"repo_type"`
	Private        types.Bool   `tfsdk:"private"`
	DeletionPolicy types.String `tfsdk:"deletion_policy"`
	RepoID         types.String `tfsdk:"repo_id"`
	URL            types.String `tfsdk:"url"`
	ID             types.String `tfsdk:"id"`
}

func NewRepositoryResource() resource.Resource { return &repositoryResource{} }
func (*repositoryResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_repository"
}
func (*repositoryResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{MarkdownDescription: "Manage a model repository's identity and visibility. Files and artifact commits are never managed. Retain is the default destroy policy; replacement leaves retained repositories intact.", Attributes: map[string]schema.Attribute{
		"namespace":       schema.StringAttribute{Required: true, Validators: []validator.String{identityValidator()}, PlanModifiers: replace, MarkdownDescription: "Owning Hugging Face user or organization."},
		"name":            schema.StringAttribute{Required: true, Validators: []validator.String{identityValidator()}, PlanModifiers: replace, MarkdownDescription: "Repository name. Changes require replacement."},
		"repo_type":       schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("model"), Validators: []validator.String{oneOf("model")}, PlanModifiers: replace, MarkdownDescription: "Repository type; v1 supports model only."},
		"private":         schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), MarkdownDescription: "Private visibility, default true. Updated in place."},
		"deletion_policy": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("retain"), Validators: []validator.String{oneOf("retain", "delete")}, MarkdownDescription: "Local destroy policy: retain (default) or delete. Imports default to retain."},
		"repo_id":         schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stableRepositoryIdentity{}}, MarkdownDescription: "Repository identity namespace/name."},
		"url":             schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stableRepositoryIdentity{}}, MarkdownDescription: "Repository URL."},
		"id":              schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stableRepositoryIdentity{}}, MarkdownDescription: "Import identity model/namespace/name."},
	}}
}
func (r *repositoryResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", "Expected Hugging Face client.")
		return
	}
	r.api = api
}
func (m repositoryModel) remote() client.Repository {
	return client.Repository{Namespace: m.Namespace.ValueString(), Name: m.Name.ValueString(), RepoType: m.RepoType.ValueString(), Private: m.Private.ValueBool()}
}
func (m *repositoryModel) refresh(v client.Repository) {
	m.Namespace = types.StringValue(v.Namespace)
	m.Name = types.StringValue(v.Name)
	m.RepoType = types.StringValue(v.RepoType)
	m.Private = types.BoolValue(v.Private)
	m.ID = types.StringValue(v.ID())
	m.RepoID = types.StringValue(v.RepoID())
	m.URL = types.StringValue(v.URL)
	if m.DeletionPolicy.IsNull() {
		m.DeletionPolicy = types.StringValue("retain")
	}
}
func (r *repositoryResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m repositoryModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.api == nil {
		resp.Diagnostics.AddError("Unconfigured provider", "Hugging Face client is unavailable.")
		return
	}
	if m.Private.IsUnknown() || m.Namespace.IsUnknown() || m.Name.IsUnknown() || m.RepoType.IsUnknown() {
		resp.Diagnostics.AddError("Unknown repository configuration", "Repository identity and visibility must be known at creation.")
		return
	}
	v, err := r.api.CreateRepository(ctx, m.remote())
	if err != nil {
		resp.Diagnostics.AddError("Create repository failed", err.Error())
		return
	}
	m.refresh(v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *repositoryResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	resp.State = req.State
	var m repositoryModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.api == nil {
		resp.Diagnostics.AddError("Unconfigured provider", "Hugging Face client is unavailable.")
		return
	}
	v, err := r.api.GetRepository(ctx, m.remote())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Read repository failed", err.Error())
		return
	}
	m.refresh(v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *repositoryResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.State = req.State
	var planned, old repositoryModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &planned)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.api == nil {
		resp.Diagnostics.AddError("Unconfigured provider", "Hugging Face client is unavailable.")
		return
	}
	if planned.Private.IsUnknown() {
		resp.Diagnostics.AddError("Unknown visibility", "Visibility must be known at update.")
		return
	}
	if !planned.Private.Equal(old.Private) {
		if err := r.api.UpdateRepository(ctx, planned.remote()); err != nil {
			resp.Diagnostics.AddError("Update repository failed", err.Error())
			return
		}
	}
	v, err := r.api.GetRepository(ctx, planned.remote())
	if err != nil {
		resp.Diagnostics.AddError("Read after update failed", err.Error())
		return
	}
	planned.refresh(v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &planned)...)
}
func (r *repositoryResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.State = req.State
	var m repositoryModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if m.DeletionPolicy.ValueString() == "retain" {
		resp.State.RemoveResource(ctx)
		return
	}
	if m.DeletionPolicy.ValueString() != "delete" {
		resp.Diagnostics.AddError("Invalid deletion policy", "Expected retain or delete; no remote deletion attempted.")
		return
	}
	if r.api == nil {
		resp.Diagnostics.AddError("Unconfigured provider", "Hugging Face client is unavailable.")
		return
	}
	if err := r.api.DeleteRepository(ctx, m.remote()); err != nil {
		resp.Diagnostics.AddError("Delete repository failed", err.Error())
		return
	}
	resp.State.RemoveResource(ctx)
}
func parseRepositoryID(id string) (client.Repository, error) {
	parts := strings.Split(id, "/")
	if len(parts) != 3 || parts[0] != "model" || !identitySegment.MatchString(parts[1]) || !identitySegment.MatchString(parts[2]) {
		return client.Repository{}, fmt.Errorf("expected import ID model/namespace/name")
	}
	return client.Repository{RepoType: parts[0], Namespace: parts[1], Name: parts[2]}, nil
}
func (*repositoryResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	v, err := parseRepositoryID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	m := repositoryModel{Namespace: types.StringValue(v.Namespace), Name: types.StringValue(v.Name), RepoType: types.StringValue("model"), DeletionPolicy: types.StringValue("retain"), Private: types.BoolNull(), RepoID: types.StringValue(v.RepoID()), ID: types.StringValue(req.ID), URL: types.StringNull()}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
