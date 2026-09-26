package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/hakopod/terraform-provider-hakopod/internal/client"
)

var (
	_ resource.ResourceWithConfigure   = (*virtualNetworkResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*virtualNetworkResource)(nil)
	_ resource.ResourceWithImportState = (*virtualNetworkResource)(nil)
)

func NewVirtualNetworkResource() resource.Resource { return &virtualNetworkResource{} }

type virtualNetworkResource struct{ client *client.Client }

type virtualNetworkModel struct {
	Project     types.String `tfsdk:"project"`
	Environment types.String `tfsdk:"environment"`
	Config      types.String `tfsdk:"config"`
	Spec        jsonValue    `tfsdk:"spec"`
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Revision    types.Int64  `tfsdk:"revision"`
}

func (r *virtualNetworkResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_virtual_network"
}

func (r *virtualNetworkResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	oneOf := stringvalidator.ExactlyOneOf(path.MatchRoot("config"), path.MatchRoot("spec"))
	resp.Schema = schema.Schema{
		Description: "A Hakopod virtual network: private segments that list the applications allowed to join them. Managing networks requires a project administrator.",
		Attributes: map[string]schema.Attribute{
			"project":     schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Project the network belongs to."},
			"environment": schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Environment the network belongs to."},
			"config":      schema.StringAttribute{Optional: true, Validators: []validator.String{oneOf}, Description: "Network definition as TOML. Exactly one of config or spec."},
			"spec":        schema.StringAttribute{Optional: true, CustomType: jsonType{}, Validators: []validator.String{oneOf}, Description: "Network definition as JSON (key order and whitespace are ignored). Exactly one of config or spec."},
			"id":          schema.StringAttribute{Computed: true, Description: "Server-assigned network ID."},
			"name":        schema.StringAttribute{Computed: true, Description: "Network name, taken from the definition. Changing it replaces the network."},
			"revision":    schema.Int64Attribute{Computed: true, Description: "Server revision of the network."},
		},
	}
}

func (r *virtualNetworkResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// plan asks the server to validate and normalize the configured definition.
func (r *virtualNetworkResource) plan(ctx context.Context, m virtualNetworkModel) (client.NetworkPlan, error) {
	var spec json.RawMessage
	if !m.Spec.IsNull() {
		spec = json.RawMessage(m.Spec.ValueString())
	}
	return r.client.PlanNetwork(ctx, m.Project.ValueString(), m.Environment.ValueString(), m.Config.ValueString(), spec)
}

func (r *virtualNetworkResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || r.client == nil {
		return
	}
	var plan virtualNetworkModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || plan.Project.IsUnknown() || plan.Environment.IsUnknown() || plan.Config.IsUnknown() || plan.Spec.IsUnknown() {
		return
	}
	p, err := r.plan(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Planning virtual network failed", err.Error())
		return
	}
	// On create the name stays unknown until apply: applications that reference it then
	// defer their own server review until the network exists, instead of failing the plan.
	plan.Name = types.StringUnknown()
	plan.ID, plan.Revision = types.StringUnknown(), types.Int64Unknown()
	if !req.State.Raw.IsNull() {
		plan.Name = types.StringValue(p.Name())
		var state virtualNetworkModel
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}
		if !plan.Spec.IsNull() && !state.Spec.IsNull() && jsonEqual(plan.Spec.ValueString(), state.Spec.ValueString()) {
			plan.Spec = state.Spec // key order/whitespace only: keep the prior value so the plan is empty
		}
		if state.Name.ValueString() != p.Name() {
			resp.RequiresReplace = append(resp.RequiresReplace, path.Root("name"))
		} else {
			plan.ID = state.ID
			if p.Unchanged() && state.Revision.ValueInt64() == p.ExpectedRevision && state.ID.ValueString() == p.ExpectedID {
				plan.Revision = state.Revision
			}
		}
	}
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

func (r *virtualNetworkResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m virtualNetworkModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := r.plan(ctx, m)
	if err != nil {
		resp.Diagnostics.AddError("Planning virtual network failed", err.Error())
		return
	}
	if err := createCheck(m.Project.ValueString(), m.Environment.ValueString(), p); err != nil {
		resp.Diagnostics.AddError("Virtual network already exists", err.Error())
		return
	}
	r.put(ctx, &m, p, &resp.State, &resp.Diagnostics)
}

func (r *virtualNetworkResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m, state virtualNetworkModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := r.plan(ctx, m)
	if err != nil {
		resp.Diagnostics.AddError("Planning virtual network failed", err.Error())
		return
	}
	if err := updateCheck(state.ID.ValueString(), state.Revision.ValueInt64(), p); err != nil {
		resp.Diagnostics.AddError("Virtual network changed outside Terraform", err.Error())
		return
	}
	if !p.Unchanged() {
		r.put(ctx, &m, p, &resp.State, &resp.Diagnostics)
		return
	}
	n, err := r.client.GetNetwork(ctx, m.Project.ValueString(), m.Environment.ValueString(), p.Name())
	if err != nil {
		resp.Diagnostics.AddError("Reading virtual network failed", err.Error())
		return
	}
	m.ID, m.Name, m.Revision = types.StringValue(n.ID), types.StringValue(n.Name), types.Int64Value(n.Revision)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *virtualNetworkResource) put(ctx context.Context, m *virtualNetworkModel, p client.NetworkPlan, state *tfsdk.State, diags *diag.Diagnostics) {
	n, err := r.client.PutNetwork(ctx, m.Project.ValueString(), m.Environment.ValueString(), p)
	if err != nil {
		diags.AddError("Saving virtual network failed", err.Error()) // conflicts carry the server message verbatim
		return
	}
	m.ID, m.Name, m.Revision = types.StringValue(n.ID), types.StringValue(n.Name), types.Int64Value(n.Revision)
	diags.Append(state.Set(ctx, m)...)
}

func (r *virtualNetworkResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m virtualNetworkModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	n, err := r.client.GetNetwork(ctx, m.Project.ValueString(), m.Environment.ValueString(), m.Name.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Reading virtual network failed", err.Error())
		return
	}
	m.ID, m.Name, m.Revision = types.StringValue(n.ID), types.StringValue(n.Name), types.Int64Value(n.Revision)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *virtualNetworkResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m virtualNetworkModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project, env, name := m.Project.ValueString(), m.Environment.ValueString(), m.Name.ValueString()
	n, err := r.client.GetNetwork(ctx, project, env, name)
	if err == nil && n.ID != m.ID.ValueString() {
		// A network of the same name recreated outside Terraform is not this resource.
		resp.Diagnostics.AddWarning("Virtual network was replaced outside Terraform", fmt.Sprintf("%s now has ID %s, not %s; it was left in place and removed from state only.", name, n.ID, m.ID.ValueString()))
		return
	}
	if err == nil {
		err = r.client.DeleteNetwork(ctx, project, env, name, n.ID, n.Revision)
	}
	switch {
	case err == nil, client.IsNotFound(err):
	case client.IsConflict(err):
		resp.Diagnostics.AddError("Virtual network is still in use", err.Error()+"; detach the applications (deploy them without this network) before destroying it")
	default:
		resp.Diagnostics.AddError("Deleting virtual network failed", err.Error())
	}
}

func (r *virtualNetworkResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	project, env, name, err := parseNetworkImportID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project"), project)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("environment"), env)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), name)...)
}

func parseNetworkImportID(id string) (project, environment, name string, err error) {
	parts := strings.Split(id, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", fmt.Errorf("expected <project>/<environment>/<name>, got %q", id)
	}
	return parts[0], parts[1], parts[2], nil
}

func createCheck(project, environment string, p client.NetworkPlan) error {
	if p.ExpectedRevision > 0 {
		return fmt.Errorf("virtual network %s already exists in %s/%s; import it with terraform import hakopod_virtual_network.<x> %s/%s/%s", p.Name(), project, environment, project, environment, p.Name())
	}
	return nil
}

// updateCheck compares the server's current network with the one Terraform reviewed at
// plan time (state is refreshed then), so a change made between plan and apply is refused
// rather than overwritten by an update carrying the newer revision.
func updateCheck(stateID string, stateRevision int64, p client.NetworkPlan) error {
	if stateID != p.ExpectedID {
		return fmt.Errorf("virtual network %s has ID %q on the server but %q in state: it was deleted and recreated outside Terraform; remove it from state and import it", p.Name(), p.ExpectedID, stateID)
	}
	if stateRevision != p.ExpectedRevision {
		return fmt.Errorf("virtual network %s changed on the server after it was reviewed (revision %d, reviewed %d); run terraform plan again", p.Name(), p.ExpectedRevision, stateRevision)
	}
	return nil
}

// jsonType/jsonValue: a string holding JSON, compared semantically so key order and whitespace never diff.
type jsonType struct{ basetypes.StringType }

func (t jsonType) Equal(o attr.Type) bool               { _, ok := o.(jsonType); return ok }
func (t jsonType) String() string                       { return "jsonType" }
func (t jsonType) ValueType(context.Context) attr.Value { return jsonValue{} }
func (t jsonType) ValueFromString(_ context.Context, v basetypes.StringValue) (basetypes.StringValuable, diag.Diagnostics) {
	return jsonValue{StringValue: v}, nil
}
func (t jsonType) ValueFromTerraform(ctx context.Context, in tftypes.Value) (attr.Value, error) {
	v, err := t.StringType.ValueFromTerraform(ctx, in)
	if err != nil {
		return nil, err
	}
	return jsonValue{StringValue: v.(basetypes.StringValue)}, nil
}

type jsonValue struct{ basetypes.StringValue }

var _ basetypes.StringValuableWithSemanticEquals = jsonValue{}

func (v jsonValue) Type(context.Context) attr.Type { return jsonType{} }
func (v jsonValue) Equal(o attr.Value) bool {
	other, ok := o.(jsonValue)
	return ok && v.StringValue.Equal(other.StringValue)
}
func (v jsonValue) StringSemanticEquals(_ context.Context, o basetypes.StringValuable) (bool, diag.Diagnostics) {
	other, ok := o.(jsonValue)
	return ok && jsonEqual(v.ValueString(), other.ValueString()), nil
}

func jsonEqual(a, b string) bool {
	var x, y any
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal([]byte(b), &y) != nil {
		return a == b
	}
	return reflect.DeepEqual(x, y)
}
