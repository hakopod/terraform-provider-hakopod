package provider

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/boolvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hakopod/terraform-provider-hakopod/internal/client"
)

var (
	_ resource.ResourceWithConfigure   = (*secretResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*secretResource)(nil)
	_ resource.ResourceWithImportState = (*secretResource)(nil)
)

func NewSecretResource() resource.Resource { return &secretResource{} }

type secretResource struct{ client *client.Client }

type secretModel struct {
	Project      types.String `tfsdk:"project"`
	Environment  types.String `tfsdk:"environment"`
	Application  types.String `tfsdk:"application"`
	Name         types.String `tfsdk:"name"`
	Value        types.String `tfsdk:"value"`
	Generate     types.Bool   `tfsdk:"generate"`
	Format       types.String `tfsdk:"format"`
	ID           types.String `tfsdk:"id"`
	ValueVersion types.String `tfsdk:"value_version"`
}

func (m secretModel) scope() (string, string, string, string) {
	return m.Project.ValueString(), m.Environment.ValueString(), m.Application.ValueString(), m.Name.ValueString()
}

// imported: state holds neither a value nor generate, so the secret's origin is unknown.
func (m secretModel) imported() bool { return m.Value.IsNull() && !m.Generate.ValueBool() }

func (r *secretResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_secret"
}

func (r *secretResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	slugs := []validator.String{stringvalidator.RegexMatches(slugPattern, slugRule)}
	resp.Schema = schema.Schema{
		Description: "A Hakopod application secret. Services reference it by name as `ENV = { ref = \"<name>\" }` in `[services.<x>.secrets]`. Values are write-only: Hakopod never returns them. A running application picks up a changed value only after it restarts.",
		Attributes: map[string]schema.Attribute{
			"project":     schema.StringAttribute{Required: true, PlanModifiers: replace, Validators: slugs, Description: "Project name. Changing it replaces the secret."},
			"environment": schema.StringAttribute{Required: true, PlanModifiers: replace, Validators: slugs, Description: "Environment name. Changing it replaces the secret."},
			"application": schema.StringAttribute{Required: true, PlanModifiers: replace, Validators: slugs,
				Description: "Application name; the application need not exist yet. Changing it replaces the secret."},
			"name": schema.StringAttribute{Required: true, PlanModifiers: replace, Validators: slugs,
				Description: "Reference name used in `{ ref = \"<name>\" }`. Changing it replaces the secret."},
			"value": schema.StringAttribute{Optional: true, Sensitive: true,
				Validators: []validator.String{
					stringvalidator.ExactlyOneOf(path.MatchRoot("value"), path.MatchRoot("generate")),
					stringvalidator.LengthBetween(1, 64<<10),
					stringvalidator.RegexMatches(regexp.MustCompile(`^[^\x00]*$`), "must not contain NUL"),
				},
				Description: "Secret value, 1 byte to 64 KiB without NUL. Stored in Terraform state. Exactly one of `value` or `generate`. Changing it updates the secret in place."},
			"generate": schema.BoolAttribute{Optional: true,
				Validators:  []validator.Bool{boolvalidator.Equals(true)},
				Description: "Let Hakopod generate a random 32-byte value that never enters Terraform state. Must be `true` when set. Switching between `value` and `generate` replaces the secret."},
			"format": schema.StringAttribute{Optional: true,
				Validators: []validator.String{stringvalidator.OneOf("base64url", "hex"),
					stringvalidator.AlsoRequires(path.MatchRoot("generate"))},
				Description: "Encoding of a generated value: `base64url` (default) or `hex`. Only with `generate`. Changing it replaces the secret."},
			"id": schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				Description: "`project/environment/application/name`."},
			"value_version": schema.StringAttribute{Computed: true,
				Description: "Changes whenever the secret's value changes, without revealing it: a truncated SHA-256 of `value`, or a random token set when a value is generated. Put it in the application's `env` to redeploy it when the secret changes."},
		},
	}
}

func (r *secretResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *secretResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan secretModel
	var state *secretModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if !req.State.Raw.IsNull() {
		state = &secretModel{}
		resp.Diagnostics.Append(req.State.Get(ctx, state)...)
	}
	if resp.Diagnostics.HasError() {
		return
	}
	resp.RequiresReplace = append(resp.RequiresReplace, planSecret(&plan, state)...)
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

// planSecret sets the planned value_version and returns the attributes that force
// replacement. An imported secret (state has neither value nor generate) adopts the
// configuration without replacement, so importing never regenerates a secret.
func planSecret(plan, state *secretModel) path.Paths {
	var replace path.Paths
	switch {
	case state == nil || state.imported():
	case state.Generate.ValueBool():
		if !plan.Generate.ValueBool() {
			replace = append(replace, path.Root("generate"))
		}
		if !plan.Format.IsUnknown() && format(plan.Format) != format(state.Format) {
			replace = append(replace, path.Root("format"))
		}
	default: // state has a value
		if !plan.Value.IsUnknown() && plan.Value.IsNull() {
			replace = append(replace, path.Root("value"))
		}
	}
	switch {
	case plan.Value.IsUnknown():
		plan.ValueVersion = types.StringUnknown()
	case !plan.Value.IsNull():
		plan.ValueVersion = types.StringValue(valueVersion(plan.Value.ValueString()))
	case state != nil && !state.imported() && len(replace) == 0:
		plan.ValueVersion = state.ValueVersion // generated and unchanged
	default:
		plan.ValueVersion = types.StringUnknown()
	}
	return replace
}

func format(f types.String) string {
	if f.ValueString() == "" {
		return "base64url"
	}
	return f.ValueString()
}

func valueVersion(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])[:16]
}

func randomVersion() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b) // crypto/rand.Read never fails on supported platforms
	return hex.EncodeToString(b)
}

func (r *secretResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m secretModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.create(ctx, &m, &resp.Diagnostics) {
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	}
}

func (r *secretResource) create(ctx context.Context, m *secretModel, diags *diag.Diagnostics) bool {
	p, e, a, n := m.scope()
	err := r.client.CreateSecret(ctx, p, e, a, n, m.Value.ValueString(), m.Format.ValueString())
	var apiErr *client.APIError
	switch {
	case err == nil:
	case errors.As(err, &apiErr) && apiErr.Code == "secret_exists":
		diags.AddError("Secret already exists", fmt.Sprintf("%v; import it with terraform import hakopod_secret.<x> %s/%s/%s/%s", err, p, e, a, n))
		return false
	default:
		diags.AddError("Creating secret failed", err.Error())
		return false
	}
	m.ID = types.StringValue(strings.Join([]string{p, e, a, n}, "/"))
	if m.Value.IsNull() {
		m.ValueVersion = types.StringValue(randomVersion())
	} else {
		m.ValueVersion = types.StringValue(valueVersion(m.Value.ValueString()))
	}
	return true
}

func (r *secretResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m, state secretModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.update(ctx, &m, state, &resp.Diagnostics) {
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	}
}

// update writes a changed (or newly adopted) value; an adopted generate is only recorded.
func (r *secretResource) update(ctx context.Context, m *secretModel, state secretModel, diags *diag.Diagnostics) bool {
	p, e, a, n := m.scope()
	m.ID = types.StringValue(strings.Join([]string{p, e, a, n}, "/"))
	switch {
	case !m.Value.IsNull() && !m.Value.Equal(state.Value):
		restart, err := r.client.PutSecret(ctx, p, e, a, n, m.Value.ValueString())
		if err != nil {
			diags.AddError("Updating secret failed", err.Error())
			return false
		}
		if restart {
			diags.AddWarning("Application restart required",
				fmt.Sprintf("Secret %s changed; application %s in %s/%s uses the new value only after it restarts. Redeploy it, for example by putting this secret's value_version in its env.", n, a, p, e))
		}
		m.ValueVersion = types.StringValue(valueVersion(m.Value.ValueString()))
	case m.Value.IsNull() && state.imported():
		m.ValueVersion = types.StringValue(randomVersion())
	default:
		m.ValueVersion = state.ValueVersion
	}
	return true
}

func (r *secretResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m secretModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	found, err := r.exists(ctx, m)
	if err != nil {
		resp.Diagnostics.AddError("Reading secret failed", err.Error())
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}
	p, e, a, n := m.scope()
	m.ID = types.StringValue(strings.Join([]string{p, e, a, n}, "/"))
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *secretResource) exists(ctx context.Context, m secretModel) (bool, error) {
	p, e, a, n := m.scope()
	names, err := r.client.SecretNames(ctx, p, e, a)
	return slices.Contains(names, n), err
}

func (r *secretResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m secretModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, e, a, n := m.scope()
	if err := r.client.DeleteSecret(ctx, p, e, a, n); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Deleting secret failed", err.Error()) // 409 carries the server's reason
	}
}

func (r *secretResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 4 || slices.Contains(parts, "") {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("expected <project>/<environment>/<application>/<name>, got %q", req.ID))
		return
	}
	for i, attr := range []string{"project", "environment", "application", "name"} {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(attr), parts[i])...)
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
