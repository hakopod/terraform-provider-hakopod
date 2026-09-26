package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hakopod/terraform-provider-hakopod/internal/client"
)

var (
	_ resource.ResourceWithConfigure   = (*projectResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*projectResource)(nil)
	_ resource.ResourceWithImportState = (*projectResource)(nil)
)

// slugPattern mirrors the server's project/environment ID rule (tree-deploy internal/api/api.go `slug`).
var slugPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,38}[a-z0-9]$|^[a-z]$`)

const slugRule = "must be 1–40 lowercase letters, digits and hyphens, start with a letter and not end with a hyphen"

func NewProjectResource() resource.Resource { return &projectResource{} }

type projectResource struct{ client *client.Client }

type projectModel struct {
	Name             types.String `tfsdk:"name"`
	Environments     types.List   `tfsdk:"environments"`
	DisplayName      types.String `tfsdk:"display_name"`
	Description      types.String `tfsdk:"description"`
	ID               types.String `tfsdk:"id"`
	MetadataRevision types.Int64  `tfsdk:"metadata_revision"`
}

func (r *projectResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project"
}

func (r *projectResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		Description: "A Hakopod project and its environments. Creating or deleting a project requires a platform administrator. Only empty projects can be deleted, and a deleted project's ID can never be reused.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:  []validator.String{stringvalidator.RegexMatches(slugPattern, slugRule)},
				Description: "Project ID. Changing it replaces the project."},
			"environments": schema.ListAttribute{Required: true, ElementType: types.StringType,
				Validators: []validator.List{listvalidator.SizeAtLeast(1), listvalidator.UniqueValues(),
					listvalidator.ValueStringsAre(stringvalidator.RegexMatches(slugPattern, slugRule))},
				Description: "Environment names. The first is created with the project; the rest are added after it. Environments can be added but never removed, and order does not matter after creation."},
			"display_name": schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: keep,
				Description: "Human-readable project name (1–80 characters). Defaults to the project ID. Can be changed in place."},
			"description": schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: keep,
				Description: "Project description (at most 1000 characters). Can only be set at creation; the API cannot change it later."},
			"id": schema.StringAttribute{Computed: true, PlanModifiers: keep, Description: "Server project ID."},
			"metadata_revision": schema.Int64Attribute{Computed: true, PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
				Description: "Server revision of the project's display metadata."},
		},
	}
}

func (r *projectResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *client.Client, got %T", req.ProviderData))
		return
	}
	r.client = c
}

func (r *projectResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}
	var plan, state projectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(checkProjectChange(plan, state)...)
	if !plan.DisplayName.IsUnknown() && !plan.DisplayName.Equal(state.DisplayName) {
		plan.MetadataRevision = types.Int64Unknown() // renaming bumps it
		resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
	}
}

// checkProjectChange rejects the in-place changes the API cannot make.
func checkProjectChange(plan, state projectModel) diag.Diagnostics {
	var diags diag.Diagnostics
	if planned, ok := knownStrings(plan.Environments); ok {
		if gone := missing(listStrings(state.Environments), planned); len(gone) > 0 {
			diags.AddAttributeError(path.Root("environments"), "Environments cannot be removed",
				fmt.Sprintf("Hakopod cannot delete an environment (%v); remove its applications and keep it, or delete the whole project", gone))
		}
	}
	if !plan.Description.IsUnknown() && !plan.Description.Equal(state.Description) {
		diags.AddAttributeError(path.Root("description"), "Description cannot be changed",
			"The Hakopod API cannot change a project's description after creation; change it in the dashboard and update the configuration to match")
	}
	return diags
}

func (r *projectResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m projectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.create(ctx, &m, &resp.Diagnostics) {
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	}
}

// create returns true when m holds the created project. On a partial failure no
// state is saved: a tainted project would be replaced, and its ID can never be reused.
func (r *projectResource) create(ctx context.Context, m *projectModel, diags *diag.Diagnostics) bool {
	name, envs := m.Name.ValueString(), listStrings(m.Environments)
	importHint := fmt.Sprintf("; import it with terraform import hakopod_project.<x> %s", name)
	// Without display fields the server silently adds the environment to an existing project, so look first.
	_, err := r.client.GetProject(ctx, name)
	if err == nil {
		diags.AddError("Project already exists", fmt.Sprintf("project %s already exists%s", name, importHint))
		return false
	}
	// A truncated listing proves nothing, but creation is still safe: the server answers
	// 409 project_exists because CreateProject always sends a display name.
	if !client.IsNotFound(err) && !errors.Is(err, client.ErrProjectListingTruncated) {
		diags.AddError("Reading project failed", err.Error())
		return false
	}
	_, err = r.client.CreateProject(ctx, name, envs[0], m.DisplayName.ValueString(), m.Description.ValueString())
	var apiErr *client.APIError
	switch {
	case err == nil:
	case errors.As(err, &apiErr) && apiErr.Code == "project_exists":
		diags.AddError("Project already exists", err.Error()+importHint)
		return false
	case errors.As(err, &apiErr) && apiErr.Code == "retired_project":
		diags.AddError("Project ID was retired", err.Error()+"; deleted project IDs cannot be reused")
		return false
	default:
		diags.AddError("Creating project failed", err.Error())
		return false
	}
	for _, env := range envs[1:] {
		if err := r.client.CreateEnvironment(ctx, name, env); err != nil {
			diags.AddError("Creating environment failed", fmt.Sprintf("project %s was created but environment %s failed: %v%s", name, env, err, importHint))
			return false
		}
	}
	return r.refresh(ctx, m, diags)
}

func (r *projectResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m, state projectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	for _, env := range missing(listStrings(m.Environments), listStrings(state.Environments)) {
		if err := r.client.CreateEnvironment(ctx, state.ID.ValueString(), env); err != nil {
			resp.Diagnostics.AddError("Creating environment failed", err.Error())
			return
		}
	}
	if !m.DisplayName.Equal(state.DisplayName) {
		if err := r.client.RenameProject(ctx, state.ID.ValueString(), m.DisplayName.ValueString(), state.MetadataRevision.ValueInt64()); err != nil {
			resp.Diagnostics.AddError("Renaming project failed", err.Error()) // 409: changed elsewhere; refresh and retry
			return
		}
	}
	if r.refresh(ctx, &m, &resp.Diagnostics) {
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	}
}

// refresh loads the project into m, keeping m's environment order.
func (r *projectResource) refresh(ctx context.Context, m *projectModel, diags *diag.Diagnostics) bool {
	p, err := r.client.GetProject(ctx, m.Name.ValueString())
	if err != nil {
		diags.AddError("Reading project failed", err.Error())
		return false
	}
	setProject(m, p)
	return true
}

func setProject(m *projectModel, p client.Project) {
	m.ID, m.DisplayName, m.Description = types.StringValue(p.ID), types.StringValue(p.DisplayName), types.StringValue(p.Description)
	m.MetadataRevision = types.Int64Value(p.MetadataRevision)
	m.Environments = stringList(orderEnvironments(listStrings(m.Environments), p.Environments))
}

func (r *projectResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m projectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := r.client.GetProject(ctx, m.Name.ValueString())
	if errors.Is(err, client.ErrProjectListingTruncated) {
		resp.Diagnostics.AddWarning("Project not confirmed", err.Error()+"; keeping it in state.")
		return
	}
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Reading project failed", err.Error())
		return
	}
	setProject(&m, p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *projectResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m projectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := deleteProjectWhenDrained(ctx, r.client, m.ID.ValueString(), m.Name.ValueString(), 3*time.Minute, 5*time.Second)
	switch {
	case err == nil, client.IsNotFound(err):
	case client.IsConflict(err):
		resp.Diagnostics.AddError("Project is not empty", err.Error()+"; destroy the project's applications and networks first, and reclaim data they retained (dashboard Storage, or set delete_data_on_destroy = true on applications before destroying them)")
	default:
		resp.Diagnostics.AddError("Deleting project failed", err.Error())
	}
}

func (r *projectResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("name"), req, resp)
}

// orderEnvironments keeps the prior order for environments that still exist and
// appends server-only ones (in server order), so drift shows but order never flaps.
func orderEnvironments(prior, server []string) []string {
	out := []string{}
	for _, e := range prior {
		if slices.Contains(server, e) {
			out = append(out, e)
		}
	}
	return append(out, missing(server, prior)...)
}

// missing returns the elements of a not in b.
func missing(a, b []string) []string {
	var out []string
	for _, e := range a {
		if !slices.Contains(b, e) {
			out = append(out, e)
		}
	}
	return out
}

func listStrings(l types.List) []string {
	s, _ := knownStrings(l)
	return s
}

// knownStrings returns the list's values, and false if the list or any element is unknown.
func knownStrings(l types.List) ([]string, bool) {
	if l.IsUnknown() {
		return nil, false
	}
	var out []string
	for _, v := range l.Elements() {
		s, ok := v.(types.String)
		if !ok || s.IsUnknown() {
			return nil, false
		}
		out = append(out, s.ValueString())
	}
	return out, true
}

func stringList(s []string) types.List {
	v, _ := types.ListValueFrom(context.Background(), types.StringType, s)
	return v
}

// deleteProjectWhenDrained retries a "not empty" conflict for a bounded time: when the
// same apply destroys applications with delete_data_on_destroy, the server removes their
// data asynchronously (retained status "deleting"), so the project empties shortly after.
func deleteProjectWhenDrained(ctx context.Context, c *client.Client, id, name string, limit, every time.Duration) error {
	deadline := time.Now().Add(limit)
	for {
		err := c.DeleteProject(ctx, id, name)
		if !client.IsConflict(err) || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(every):
		}
	}
}
