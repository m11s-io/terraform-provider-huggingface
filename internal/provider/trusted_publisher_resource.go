package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/m11s-io/terraform-provider-huggingface/internal/client"
)

type publisherResource struct{ api *client.Client }
type publisherModel struct {
	RepoID           types.String `tfsdk:"repo_id"`
	RepoType         types.String `tfsdk:"repo_type"`
	GitHubRepository types.String `tfsdk:"github_repository"`
	Branch           types.String `tfsdk:"branch"`
	Workflow         types.String `tfsdk:"workflow"`
	PublisherID      types.String `tfsdk:"publisher_id"`
	Issuer           types.String `tfsdk:"issuer"`
	ID               types.String `tfsdk:"id"`
}

func NewTrustedPublisherResource() resource.Resource { return &publisherResource{} }
func (*publisherResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_trusted_publisher"
}
func githubInputRule(field string) validator.String {
	return stringRule{"Use a GitHub owner/repo, valid short branch and workflow YAML filename.", func(value string) bool {
		g := client.GitHubPublisher{GitHubRepository: "example/repo", Branch: "main", Workflow: "publish.yml"}
		switch field {
		case "github_repository":
			g.GitHubRepository = value
		case "branch":
			g.Branch = value
		case "workflow":
			g.Workflow = value
		}
		return g.Validate() == nil
	}}
}
func (*publisherResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replacement := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{MarkdownDescription: "Manage exactly one GitHub Actions trusted-publisher binding. Repo/claim changes replace the binding with destroy before create by default. The management API is undocumented; other bindings are preserved.", Attributes: map[string]schema.Attribute{
		"repo_id":           schema.StringAttribute{Required: true, PlanModifiers: replacement, Validators: []validator.String{stringRule{"Use namespace/name.", func(s string) bool { _, e := parseRepositoryID("model/" + s); return e == nil }}}, MarkdownDescription: "Target model repository namespace/name."},
		"repo_type":         schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("model"), PlanModifiers: replacement, Validators: []validator.String{oneOf("model")}, MarkdownDescription: "Only model is supported."},
		"github_repository": schema.StringAttribute{Required: true, PlanModifiers: replacement, Validators: []validator.String{githubInputRule("github_repository")}, MarkdownDescription: "GitHub owner/repo, matched exactly."},
		"branch":            schema.StringAttribute{Required: true, PlanModifiers: replacement, Validators: []validator.String{githubInputRule("branch")}, MarkdownDescription: "Short branch name, matched as refs/heads/<branch>."},
		"workflow":          schema.StringAttribute{Required: true, PlanModifiers: replacement, Validators: []validator.String{githubInputRule("workflow")}, MarkdownDescription: "YAML filename in .github/workflows/."},
		"publisher_id":      schema.StringAttribute{Computed: true, MarkdownDescription: "Immutable remote binding identifier."},
		"issuer":            schema.StringAttribute{Computed: true, MarkdownDescription: "GitHub Actions OIDC issuer."},
		"id":                schema.StringAttribute{Computed: true, MarkdownDescription: "Import identity model/namespace/name/publisher_id."},
	}}
}
func (r *publisherResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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
func (m publisherModel) remote() (client.Repository, error) {
	return parseRepositoryID(m.RepoType.ValueString() + "/" + m.RepoID.ValueString())
}
func (m publisherModel) github() client.GitHubPublisher {
	return client.GitHubPublisher{GitHubRepository: m.GitHubRepository.ValueString(), Branch: m.Branch.ValueString(), Workflow: m.Workflow.ValueString()}
}
func managedGitHub(p client.Publisher) (client.GitHubPublisher, error) {
	fail := func() (client.GitHubPublisher, error) {
		return client.GitHubPublisher{}, errors.New("remote publisher has unsupported or inconsistent GitHub issuer/claims; inspect the binding and explicitly replace it rather than retaining planned claims")
	}
	if p.Issuer != client.GitHubIssuer || len(p.RawClaims) != 3 || len(p.Claims) != 3 {
		return fail()
	}
	get := func(key, kind string) (string, bool) {
		v, ok := p.Claims[key]
		s, isString := v.Value.(string)
		return s, ok && isString && v.Kind == kind && len(v.AdditionalProperties) == 0
	}
	repo, okRepo := get("repository", "eq")
	ref, okRef := get("ref", "eq")
	workflow, okWorkflow := get("workflow_ref", "prefix")
	prefix := repo + "/.github/workflows/"
	if !okRepo || !okRef || !okWorkflow || !strings.HasPrefix(ref, "refs/heads/") || !strings.HasPrefix(workflow, prefix) || !strings.HasSuffix(workflow, "@") {
		return fail()
	}
	g := client.GitHubPublisher{GitHubRepository: repo, Branch: strings.TrimPrefix(ref, "refs/heads/"), Workflow: strings.TrimSuffix(strings.TrimPrefix(workflow, prefix), "@")}
	if g.Validate() != nil {
		return fail()
	}
	return g, nil
}
func (m *publisherModel) refresh(repo client.Repository, p client.Publisher) error {
	g, err := managedGitHub(p)
	if err != nil {
		return err
	}
	m.RepoType = types.StringValue("model")
	m.RepoID = types.StringValue(repo.RepoID())
	m.GitHubRepository = types.StringValue(g.GitHubRepository)
	m.Branch = types.StringValue(g.Branch)
	m.Workflow = types.StringValue(g.Workflow)
	m.PublisherID = types.StringValue(p.ID)
	m.Issuer = types.StringValue(p.Issuer)
	m.ID = types.StringValue(repo.ID() + "/" + p.ID)
	return nil
}
func (r *publisherResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m publisherModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	repo, err := m.remote()
	if err != nil {
		resp.Diagnostics.AddError("Invalid publisher repository", err.Error())
		return
	}
	if r.api == nil {
		resp.Diagnostics.AddError("Unconfigured provider", "Hugging Face client is unavailable.")
		return
	}
	p, err := r.api.CreatePublisher(ctx, repo, m.github())
	if err != nil {
		resp.Diagnostics.AddError("Create publisher failed", err.Error())
		return
	}
	createdID := repo.ID() + "/" + p.ID
	p, err = r.api.GetPublisher(ctx, repo, p.ID)
	if err != nil {
		resp.Diagnostics.AddError("Verify created publisher failed", fmt.Sprintf("Binding may have been created. Inspect the repository and explicitly import %s; do not blindly retry. %s", createdID, err))
		return
	}
	if err = m.refresh(repo, p); err != nil {
		resp.Diagnostics.AddError("Unsupported publisher drift", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *publisherResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	resp.State = req.State
	var m publisherModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	repo, err := m.remote()
	if err != nil {
		resp.Diagnostics.AddError("Invalid publisher repository", err.Error())
		return
	}
	if r.api == nil {
		resp.Diagnostics.AddError("Unconfigured provider", "Hugging Face client is unavailable.")
		return
	}
	p, err := r.api.GetPublisher(ctx, repo, m.PublisherID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Read publisher failed", err.Error())
		return
	}
	if err = m.refresh(repo, p); err != nil {
		resp.Diagnostics.AddError("Unsupported publisher drift", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (*publisherResource) Update(_ context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.State = req.State
	resp.Diagnostics.AddError("Publisher replacement required", "All configured publisher identity and claim changes require replacement.")
}
func (r *publisherResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.State = req.State
	var m publisherModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	repo, err := m.remote()
	if err != nil {
		resp.Diagnostics.AddError("Invalid publisher repository", err.Error())
		return
	}
	if r.api == nil {
		resp.Diagnostics.AddError("Unconfigured provider", "Hugging Face client is unavailable.")
		return
	}
	if err = r.api.DeletePublisher(ctx, repo, m.PublisherID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Delete publisher failed", err.Error())
		return
	}
	resp.State.RemoveResource(ctx)
}

var remotePublisherID = regexp.MustCompile(`^[a-fA-F0-9]{24}$`)

func (*publisherResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 4 || !remotePublisherID.MatchString(parts[3]) {
		resp.Diagnostics.AddError("Invalid import ID", "Expected model/namespace/name/publisher_id.")
		return
	}
	repo, err := parseRepositoryID(strings.Join(parts[:3], "/"))
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	m := publisherModel{RepoType: types.StringValue("model"), RepoID: types.StringValue(repo.RepoID()), PublisherID: types.StringValue(parts[3]), ID: types.StringValue(req.ID), GitHubRepository: types.StringNull(), Branch: types.StringNull(), Workflow: types.StringNull(), Issuer: types.StringNull()}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
