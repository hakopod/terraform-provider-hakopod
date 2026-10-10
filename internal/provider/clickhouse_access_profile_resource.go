package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/boolvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hakopod/terraform-provider-hakopod/internal/client"
)

var (
	_ resource.ResourceWithConfigure   = (*clickHouseAccessProfileResource)(nil)
	_ resource.ResourceWithImportState = (*clickHouseAccessProfileResource)(nil)
)

func NewClickHouseAccessProfileResource() resource.Resource {
	return &clickHouseAccessProfileResource{}
}

type clickHouseAccessProfileResource struct{ client *client.Client }
type clickHouseAccessProfileModel struct {
	DatabaseID                types.String `tfsdk:"database_id"`
	AccessProfile             types.String `tfsdk:"access_profile"`
	AllowTenantAdministration types.Bool   `tfsdk:"allow_tenant_administration"`
	ID                        types.String `tfsdk:"id"`
	Revision                  types.Int64  `tfsdk:"revision"`
}

func (r *clickHouseAccessProfileResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_clickhouse_access_profile"
}

func (r *clickHouseAccessProfileResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manage tenant administration on an existing standalone ClickHouse database. Removing this resource preserves the database and its privileges.",
		Attributes: map[string]schema.Attribute{
			"database_id":                 schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.LengthAtLeast(1)}, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, Description: "Existing managed database ID. This resource never creates a database."},
			"access_profile":              schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.OneOf("tenant_admin")}, Description: "Set tenant_admin. Hakopod cannot disable tenant administration in place."},
			"allow_tenant_administration": schema.BoolAttribute{Required: true, Validators: []validator.Bool{boolvalidator.Equals(true)}, Description: "Set true to permit the application login to manage SQL users and quotas. Backups exclude tenant access entities."},
			"id":                          schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, Description: "Managed database ID."},
			"revision":                    schema.Int64Attribute{Computed: true, Description: "Last observed database revision. It does not prove that the database is healthy."},
		},
	}
}

func (r *clickHouseAccessProfileResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("Expected *client.Client. Received %T.", req.ProviderData))
		return
	}
	r.client = c
}

func accessProfileOptIn(m clickHouseAccessProfileModel) error {
	if m.DatabaseID.IsUnknown() || m.DatabaseID.IsNull() || m.DatabaseID.ValueString() == "" {
		return fmt.Errorf("set database_id to an existing standalone ClickHouse database ID")
	}
	if m.AccessProfile.IsUnknown() || m.AccessProfile.IsNull() || m.AccessProfile.ValueString() != "tenant_admin" {
		return fmt.Errorf("set access_profile to tenant_admin")
	}
	if m.AllowTenantAdministration.IsUnknown() || m.AllowTenantAdministration.IsNull() || !m.AllowTenantAdministration.ValueBool() {
		return fmt.Errorf("set allow_tenant_administration to true to permit this privilege change")
	}
	return nil
}

func setAccessProfile(m *clickHouseAccessProfileModel, d client.DatabaseAccess) error {
	profile, err := d.Profile()
	if err != nil {
		return err
	}
	m.ID = types.StringValue(d.ID)
	m.DatabaseID = types.StringValue(d.ID)
	m.AccessProfile = types.StringValue(profile)
	m.Revision = types.Int64Value(d.Revision)
	return nil
}

func (r *clickHouseAccessProfileResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m clickHouseAccessProfileModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := accessProfileOptIn(m); err != nil {
		resp.Diagnostics.AddError("Tenant administration requires explicit consent", err.Error())
		return
	}
	d, err := r.client.ActivateClickHouseTenantAdmin(ctx, m.DatabaseID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Tenant administration was not confirmed", err.Error()+" Refresh or import the database profile after inspection. Do not retry an unconfirmed operation blindly.")
		return
	}
	if err := setAccessProfile(&m, d); err != nil {
		resp.Diagnostics.AddError("Invalid database profile", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *clickHouseAccessProfileResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m, prior clickHouseAccessProfileModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !m.DatabaseID.Equal(prior.DatabaseID) {
		resp.Diagnostics.AddError("Database identity changed", "Replace the resource to manage a different database. The previous database retains its privileges.")
		return
	}
	if err := accessProfileOptIn(m); err != nil {
		resp.Diagnostics.AddError("Tenant administration requires explicit consent", err.Error())
		return
	}
	d, err := r.client.ActivateClickHouseTenantAdmin(ctx, m.DatabaseID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Tenant administration was not confirmed", err.Error())
		return
	}
	if err := setAccessProfile(&m, d); err != nil {
		resp.Diagnostics.AddError("Invalid database profile", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *clickHouseAccessProfileResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m clickHouseAccessProfileModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, err := r.client.GetDatabaseAccess(ctx, m.DatabaseID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the database profile", err.Error())
		return
	}
	if err := setAccessProfile(&m, d); err != nil {
		resp.Diagnostics.AddError("Invalid database profile", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *clickHouseAccessProfileResource) Delete(ctx context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	// The profile is irreversible. Relinquish Terraform management without an API call.
	resp.State.RemoveResource(ctx)
}

func (r *clickHouseAccessProfileResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	d, err := r.client.GetDatabaseAccess(ctx, req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Cannot import the database profile", err.Error())
		return
	}
	m := clickHouseAccessProfileModel{AllowTenantAdministration: types.BoolValue(false)}
	if err := setAccessProfile(&m, d); err != nil {
		resp.Diagnostics.AddError("Invalid database profile", err.Error())
		return
	}
	// Import reads state only. The configuration must separately authorize activation.
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	resp.Diagnostics.AddWarning("Import does not authorize a privilege change", "Set allow_tenant_administration to true in the configuration before applying tenant administration.")
}
