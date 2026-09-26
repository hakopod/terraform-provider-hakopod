package provider

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"regexp"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hakopod/terraform-provider-hakopod/internal/client"
)

var (
	_ resource.ResourceWithConfigure      = (*applicationResource)(nil)
	_ resource.ResourceWithModifyPlan     = (*applicationResource)(nil)
	_ resource.ResourceWithImportState    = (*applicationResource)(nil)
	_ resource.ResourceWithValidateConfig = (*applicationResource)(nil)
)

func NewApplicationResource() resource.Resource { return &applicationResource{} }

type applicationResource struct{ client *client.Client }

type applicationModel struct {
	Project             types.String `tfsdk:"project"`
	Environment         types.String `tfsdk:"environment"`
	Config              types.String `tfsdk:"config"`
	Spec                jsonValue    `tfsdk:"spec"`
	EnvFiles            types.Map    `tfsdk:"env_files"`
	Wait                types.Bool   `tfsdk:"wait"`
	WaitTimeout         types.String `tfsdk:"wait_timeout"`
	DeleteDataOnDestroy types.Bool   `tfsdk:"delete_data_on_destroy"`
	ID                  types.String `tfsdk:"id"`
	Name                types.String `tfsdk:"name"`
	Revision            types.Int64  `tfsdk:"revision"`
	Status              types.String `tfsdk:"status"`
	DeploymentID        types.String `tfsdk:"deployment_id"`
	PendingChanges      types.Int64  `tfsdk:"pending_changes"`
	ServiceHostnames    types.Map    `tfsdk:"service_hostnames"`
}

func (r *applicationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_application"
}

func (r *applicationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	oneOf := stringvalidator.ExactlyOneOf(path.MatchRoot("config"), path.MatchRoot("spec"))
	resp.Schema = schema.Schema{
		Description: "A Hakopod application. Every apply that changes it deploys a new revision; destroy deploys an empty revision and then deletes the application (project administrator required).",
		Attributes: map[string]schema.Attribute{
			"project":     schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Project the application belongs to."},
			"environment": schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Environment the application belongs to."},
			"config":      schema.StringAttribute{Optional: true, Validators: []validator.String{oneOf}, Description: "Application definition as TOML, typically file(...). Exactly one of config or spec."},
			"spec":        schema.StringAttribute{Optional: true, CustomType: jsonType{}, Validators: []validator.String{oneOf}, Description: "Application definition as JSON, typically jsonencode({...}); key order and whitespace are ignored. Exactly one of config or spec."},
			"env_files": schema.MapAttribute{Optional: true, Sensitive: true, ElementType: types.StringType,
				Description: "Contents for env_file references in config, keyed by the referenced relative path. Only valid with config. With env_files set, the server is not asked for a plan at plan time (its plan imports them into secrets)."},
			"wait":                   schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), Description: "Wait for each deployment to finish. Default true."},
			"wait_timeout":           schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("20m"), Description: "How long to wait for a deployment, as a Go duration. Default 20m."},
			"delete_data_on_destroy": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), Description: "Delete persistent volumes when the application is destroyed. Default false (volumes are retained)."},
			"id":                     schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, Description: "Application ID."},
			"name":                   schema.StringAttribute{Computed: true, Description: "Application name, from the definition. Changing it replaces the application."},
			"revision":               schema.Int64Attribute{Computed: true, Description: "Current application revision."},
			"status":                 schema.StringAttribute{Computed: true, Description: "Application status (healthy, recovered, failed, empty, ...)."},
			"deployment_id":          schema.StringAttribute{Computed: true, Description: "ID of the last deployment this resource made."},
			"pending_changes":        schema.Int64Attribute{Computed: true, Description: "Number of server-reported changes the last planned deployment carried."},
			"service_hostnames":      schema.MapAttribute{Computed: true, ElementType: types.StringType, Description: "Private DNS name of each service, keyed by service name. Another application that joins a shared virtual network segment and is allowed by the service's network_access reaches it at this name. Known after the application is first created."},
		},
	}
}

func (r *applicationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *applicationResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m applicationModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !m.EnvFiles.IsNull() && !m.Spec.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("env_files"), "env_files requires config", "env_files supplies env_file contents for a TOML config; it cannot be used with spec.")
	}
	if !m.WaitTimeout.IsNull() && !m.WaitTimeout.IsUnknown() {
		if _, err := time.ParseDuration(m.WaitTimeout.ValueString()); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("wait_timeout"), "Invalid wait_timeout", err.Error())
		}
	}
}

func (r *applicationResource) planInput(ctx context.Context, m applicationModel) (client.PlanInput, diag.Diagnostics) {
	in := client.PlanInput{Project: m.Project.ValueString(), Environment: m.Environment.ValueString(), TOML: m.Config.ValueString()}
	if !m.Spec.IsNull() {
		in.Spec = json.RawMessage(m.Spec.ValueString())
	}
	var diags diag.Diagnostics
	if !m.EnvFiles.IsNull() {
		diags = m.EnvFiles.ElementsAs(ctx, &in.EnvFiles, false)
	}
	return in, diags
}

// needsDeploy is the skip rule: a zero-change deployment still rolls every pod,
// so only deploy when the server reports changes or the app is not healthy
// (a recovered app runs the previous workload even though its spec matches).
// needsDeploy reports whether an apply must deploy. A zero-change deployment still rolls
// every pod, so only real changes or an unhealthy application redeploy. An application
// configured with no services settles as "empty"; one still rolling out is queued/running.
func needsDeploy(changes int, status string) bool {
	switch status {
	case "healthy", "empty", "queued", "running":
		return changes > 0
	}
	return true
}

func (r *applicationResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || r.client == nil {
		return
	}
	var plan, state applicationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	hasState := !req.State.Raw.IsNull()
	if hasState {
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	}
	if resp.Diagnostics.HasError() || plan.Project.IsUnknown() || plan.Environment.IsUnknown() || plan.Config.IsUnknown() || plan.Spec.IsUnknown() {
		return
	}
	if hasState && !plan.Spec.IsNull() && !state.Spec.IsNull() && jsonEqual(plan.Spec.ValueString(), state.Spec.ValueString()) {
		plan.Spec = state.Spec // key order/whitespace only: Terraform accepts the prior value
	}
	unknown := func() {
		plan.Revision, plan.Status, plan.DeploymentID = types.Int64Unknown(), types.StringUnknown(), types.StringUnknown()
		plan.ServiceHostnames = types.MapUnknown(types.StringType)
	}
	keep := func() {
		plan.Name, plan.Revision, plan.Status, plan.DeploymentID, plan.PendingChanges = state.Name, state.Revision, state.Status, state.DeploymentID, state.PendingChanges
		plan.ServiceHostnames = state.ServiceHostnames
	}

	if !plan.EnvFiles.IsNull() {
		// The server imports env_file contents into secrets during /plan; a plan must not write.
		if hasState && !plan.EnvFiles.IsUnknown() && plan.Config.Equal(state.Config) && plan.EnvFiles.Equal(state.EnvFiles) && state.Status.ValueString() == "healthy" {
			keep()
		} else {
			plan.Name, plan.PendingChanges = types.StringUnknown(), types.Int64Unknown()
			if hasState && plan.Config.Equal(state.Config) {
				plan.Name = state.Name
			}
			unknown()
		}
		resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
		return
	}

	in, diags := r.planInput(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if diags.HasError() {
		return
	}
	p, err := r.client.PlanApplication(ctx, in)
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && (apiErr.Code == "invalid_network" || (!hasState && apiErr.Status == 409)) {
		// invalid_network: the grant may come from a network change in this same plan.
		// 409 without state: a conflict such as a retired name must not block planning
		// the rest of the configuration, including a destroy; apply rejects it for real.
		// The grant this application needs may come from a network change in the same
		// plan, which the server cannot see yet. Apply plans again and fails for real.
		resp.Diagnostics.AddWarning("Application checked at apply", err.Error()+". A network grant from this same plan is only visible to the server after apply; any other conflict fails the apply.")
		plan.Name, plan.PendingChanges = types.StringUnknown(), types.Int64Unknown()
		if hasState {
			plan.Name = state.Name
		}
		unknown()
		resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Planning application failed", err.Error())
		return
	}
	for _, w := range p.Warnings {
		resp.Diagnostics.AddWarning("Hakopod plan warning", w)
	}
	name := specName(p.Spec)
	if !hasState || needsDeploy(len(p.Changes), state.Status.ValueString()) {
		unknown()
		plan.PendingChanges = types.Int64Value(int64(len(p.Changes)))
	} else {
		keep()
	}
	plan.Name = types.StringValue(name)
	if hasState && !state.ID.IsNull() {
		// The ID is fixed for an existing application, so its service names are known now.
		plan.ServiceHostnames = serviceHostnames(state.ID.ValueString(), p.Spec)
	}
	if hasState && state.Name.ValueString() != name {
		resp.RequiresReplace = append(resp.RequiresReplace, path.Root("name"))
	}
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

func (r *applicationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m applicationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.apply(ctx, &m, "", &resp.Diagnostics, func() { resp.Diagnostics.Append(resp.State.Set(ctx, &m)...) })
}

func (r *applicationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m, state applicationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	m.ID = state.ID
	if !m.Revision.IsUnknown() {
		// The plan kept the deployed values (no changes, or env_files unchanged), so only
		// settings such as wait or wait_timeout changed. Deploying now would contradict the
		// reviewed plan and report an inconsistent result.
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
		return
	}
	if m.DeploymentID.IsUnknown() {
		m.DeploymentID = state.DeploymentID
	}
	r.apply(ctx, &m, state.ID.ValueString(), &resp.Diagnostics, func() { resp.Diagnostics.Append(resp.State.Set(ctx, &m)...) })
}

// apply plans, deploys (unless the skip rule holds for an existing app) and
// waits. save is called whenever m holds an application ID worth tracking,
// including before a wait failure is reported, so Terraform taints rather than orphans.
func (r *applicationResource) apply(ctx context.Context, m *applicationModel, existingID string, diags *diag.Diagnostics, save func()) {
	creating := existingID == ""
	in, d := r.planInput(ctx, *m)
	if diags.Append(d...); d.HasError() {
		return
	}
	p, err := r.client.PlanApplication(ctx, in)
	if err != nil {
		diags.AddError("Planning application failed", err.Error())
		return
	}
	// Adopting an existing application would let a later destroy delete something
	// Terraform never created, so creation refuses and points at import instead.
	if existingID == "" && p.ApplicationID != "" {
		// A failed first deployment is not saved to state (see below), so the next apply
		// finds its application here. Adopt it only if no release ever succeeded: that is
		// the same broken attempt, never something working that Terraform did not create.
		if existing, err := r.client.GetApplication(ctx, p.ApplicationID); err == nil && existing.NeverSucceeded() {
			diags.AddWarning("Adopting a never-deployed application", fmt.Sprintf("Application %s exists but no release of it has succeeded, typically a failed earlier apply; Terraform now manages it.", specName(p.Spec)))
			existingID = p.ApplicationID
		}
	}
	if existingID == "" && p.ApplicationID != "" {
		diags.AddError("Application already exists", fmt.Sprintf("Application %s already exists in %s/%s. Import it with: terraform import <address> %s/%s/%s",
			specName(p.Spec), m.Project.ValueString(), m.Environment.ValueString(), m.Project.ValueString(), m.Environment.ValueString(), specName(p.Spec)))
		return
	}
	if len(p.MissingSecrets) > 0 {
		diags.AddError("Application needs secrets", fmt.Sprintf("Missing secrets: %s. Set these secrets in the dashboard or with the CLI, then apply again.", strings.Join(p.MissingSecrets, ", ")))
		return
	}
	for _, w := range p.Warnings {
		diags.AddWarning("Hakopod plan warning", w)
	}
	if m.Name.IsUnknown() {
		m.Name = types.StringValue(specName(p.Spec))
	}
	if m.PendingChanges.IsUnknown() {
		m.PendingChanges = types.Int64Value(int64(len(p.Changes)))
	}
	if existingID != "" && p.ApplicationID != existingID {
		// Only possible when the name was unknown at plan time, so no replacement was
		// planned. Deploying would create a second application and orphan this one.
		diags.AddError("Application name changed", fmt.Sprintf("The configuration now names application %q, not the one Terraform manages. Renaming replaces the application; run terraform plan again so the replacement is planned, and note that the old name is retired when it is destroyed.", specName(p.Spec)))
		return
	}
	if existingID != "" {
		app, err := r.client.GetApplication(ctx, existingID)
		if err != nil {
			diags.AddError("Reading application failed", err.Error())
			return
		}
		if !needsDeploy(len(p.Changes), app.Status) {
			m.Revision, m.Status = types.Int64Value(app.Revision), types.StringValue(app.Status)
			m.ServiceHostnames = serviceHostnames(existingID, p.Spec)
			save()
			return
		}
	}
	key, err := idempotencyKey()
	if err != nil {
		diags.AddError("Generating idempotency key failed", err.Error())
		return
	}
	dep, err := r.client.Deploy(ctx, m.Project.ValueString(), m.Environment.ValueString(), p.Spec, p.ExpectedRevision, key)
	if err != nil {
		diags.AddError("Deploying application failed", err.Error())
		return
	}
	m.ID, m.DeploymentID, m.Revision, m.Status = types.StringValue(dep.ApplicationID), types.StringValue(dep.ID), types.Int64Value(dep.Revision), types.StringValue(dep.Status)
	m.ServiceHostnames = serviceHostnames(dep.ApplicationID, p.Spec)
	var waitErr error
	if m.Wait.ValueBool() {
		waitErr = r.wait(ctx, dep.ID, m.WaitTimeout.ValueString())
	}
	if app, err := r.client.GetApplication(ctx, dep.ApplicationID); err == nil {
		m.Revision, m.Status = types.Int64Value(app.Revision), types.StringValue(app.Status)
	} else if waitErr == nil && creating {
		// The deployment succeeded; an error here would taint the resource and the next
		// apply would delete it, retiring its name. Keep the deployment's values instead.
		diags.AddWarning("Reading application failed", err.Error()+"; the next refresh updates its status.")
	} else if waitErr == nil {
		diags.AddError("Reading application failed", err.Error())
	}
	if waitErr != nil && creating {
		// Saving state here would taint the resource, and Terraform replaces a tainted
		// resource by deleting it; Hakopod retires a deleted application's name for good.
		// Leave it out of state instead: the next apply adopts it (see above) and retries.
		diags.AddError("Deployment did not succeed", waitErr.Error()+". The application was created but is not yet tracked; fix the configuration and apply again to retry it.")
		return
	}
	save()
	if waitErr != nil {
		diags.AddError("Deployment did not succeed", waitErr.Error())
	}
}

func (r *applicationResource) wait(ctx context.Context, id, timeout string) error {
	d, err := time.ParseDuration(timeout)
	if err != nil {
		d = 20 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	dep, err := r.client.WaitDeployment(ctx, id, 0)
	if err != nil {
		return err
	}
	if dep.Status != "succeeded" {
		return fmt.Errorf("deployment %s %s: %s", dep.ID, dep.Status, dep.Error)
	}
	return nil
}

func (r *applicationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m applicationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	app, err := r.client.GetApplication(ctx, m.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Reading application failed", err.Error())
		return
	}
	m.Name, m.Revision, m.Status = types.StringValue(app.Name), types.Int64Value(app.Revision), types.StringValue(app.Status)
	m.ServiceHostnames = serviceHostnames(app.ID, app.Spec)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *applicationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m applicationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.delete(ctx, m)
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Deleting application failed", err.Error())
	}
}

func (r *applicationResource) delete(ctx context.Context, m applicationModel) error {
	app, err := r.client.GetApplication(ctx, m.ID.ValueString())
	if err != nil {
		return err
	}
	revision := app.Revision
	if !(app.Status == "empty" && servicesEmpty(app.Spec)) {
		spec, err := emptySpec(app.Spec)
		if err != nil {
			return err
		}
		p, err := r.client.PlanApplication(ctx, client.PlanInput{Project: app.Project, Environment: app.Environment, Spec: spec})
		if err != nil {
			return fmt.Errorf("planning the empty revision: %w", err)
		}
		key, err := idempotencyKey()
		if err != nil {
			return err
		}
		dep, err := r.client.Deploy(ctx, app.Project, app.Environment, p.Spec, p.ExpectedRevision, key)
		if err != nil {
			return fmt.Errorf("deploying the empty revision: %w", err)
		}
		if err := r.wait(ctx, dep.ID, m.WaitTimeout.ValueString()); err != nil {
			return fmt.Errorf("removing services before delete: %w", err)
		}
		revision = dep.Revision
	}
	return r.client.DeleteApplication(ctx, app.ID, app.Name, revision, m.DeleteDataOnDestroy.ValueBool())
}

// emptySpec is the final-removal revision, built like the dashboard's
// withoutService (web/src/lib/remove-service.ts): services becomes an explicit
// empty table and domains (which name services) go; secrets, env, networks and
// volumes stay so the empty revision neither changes variables nor drops volume definitions.
func emptySpec(spec json.RawMessage) (json.RawMessage, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(spec, &doc); err != nil {
		return nil, fmt.Errorf("decoding current spec: %w", err)
	}
	doc["services"] = json.RawMessage("{}")
	delete(doc, "domains")
	return json.Marshal(doc)
}

func servicesEmpty(spec json.RawMessage) bool {
	var doc struct {
		Services map[string]json.RawMessage `json:"services"`
	}
	return json.Unmarshal(spec, &doc) == nil && doc.Services != nil && len(doc.Services) == 0
}

// serviceHostnames mirrors the server: each application runs in namespace
// "hp-" + hex(sha256(application ID)[:16]) (internal/cluster/types.go Namespace), and the
// network inspector reports "<service>.<namespace>.svc.cluster.local"
// (internal/api/virtual_networks.go).
func serviceHostnames(applicationID string, spec json.RawMessage) types.Map {
	var doc struct {
		Services map[string]json.RawMessage `json:"services"`
	}
	_ = json.Unmarshal(spec, &doc)
	sum := sha256.Sum256([]byte(applicationID))
	namespace := "hp-" + hex.EncodeToString(sum[:16])
	names := map[string]attr.Value{}
	for service := range doc.Services {
		names[service] = types.StringValue(service + "." + namespace + ".svc.cluster.local")
	}
	return types.MapValueMust(types.StringType, names)
}

func specName(spec json.RawMessage) string {
	var doc struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(spec, &doc)
	return doc.Name
}

func idempotencyKey() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

var hexID = regexp.MustCompile(`^[0-9a-f]{32}$`)

func (r *applicationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "the Hakopod provider must be configured before import")
		return
	}
	var app client.Application
	var err error
	if hexID.MatchString(req.ID) {
		app, err = r.client.GetApplication(ctx, req.ID)
	} else if parts := strings.Split(req.ID, "/"); len(parts) == 3 && parts[0] != "" && parts[1] != "" && parts[2] != "" {
		app, err = r.client.FindApplication(ctx, parts[0], parts[1], parts[2])
	} else {
		resp.Diagnostics.AddError("Invalid import ID", "use project/environment/name or a 32-character application ID")
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Importing application failed", err.Error())
		return
	}
	m := applicationModel{
		Project: types.StringValue(app.Project), Environment: types.StringValue(app.Environment),
		Config: types.StringNull(), Spec: jsonValue{StringValue: types.StringNull()}, EnvFiles: types.MapNull(types.StringType), ServiceHostnames: types.MapNull(types.StringType),
		Wait: types.BoolValue(true), WaitTimeout: types.StringValue("20m"), DeleteDataOnDestroy: types.BoolValue(false),
		ID: types.StringValue(app.ID), Name: types.StringValue(app.Name), Revision: types.Int64Value(app.Revision),
		Status: types.StringValue(app.Status), DeploymentID: types.StringNull(), PendingChanges: types.Int64Value(0),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
