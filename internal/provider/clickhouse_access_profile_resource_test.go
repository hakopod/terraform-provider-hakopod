package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hakopod/terraform-provider-hakopod/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func profileModel() clickHouseAccessProfileModel {
	return clickHouseAccessProfileModel{
		DatabaseID: types.StringValue("db1"), ID: types.StringValue("db1"),
		AccessProfile: types.StringValue("tenant_admin"), AllowTenantAdministration: types.BoolValue(true), Revision: types.Int64Value(4),
	}
}

func profileState(t *testing.T, m clickHouseAccessProfileModel) tfsdk.State {
	t.Helper()
	var schema resource.SchemaResponse
	NewClickHouseAccessProfileResource().Schema(context.Background(), resource.SchemaRequest{}, &schema)
	state := tfsdk.State{Schema: schema.Schema}
	if diags := state.Set(context.Background(), m); diags.HasError() {
		t.Fatal(diags)
	}
	return state
}

func profileResourceServer(t *testing.T, status int, profile string) (*clickHouseAccessProfileResource, *int) {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.URL.Path != "/api/v1/databases/db1" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status != 200 {
			w.Write([]byte(`{"error":{"code":"not_found","message":"database unavailable"}}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id": "db1", "revision": 5, "status": "ready",
			"spec":        map[string]any{"engine": "clickhouse", "mode": "standalone", "clickhouse": map[string]any{"access_profile": profile}},
			"observation": map[string]any{"status": "ready", "revision": 5, "observed_at": time.Now().Add(-time.Second)},
		})
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL, "key", "", "test")
	if err != nil {
		t.Fatal(err)
	}
	return &clickHouseAccessProfileResource{client: c}, &calls
}

func TestClickHouseAccessProfileSchema(t *testing.T) {
	var resp resource.SchemaResponse
	NewClickHouseAccessProfileResource().Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if diags := resp.Schema.ValidateImplementation(context.Background()); diags.HasError() {
		t.Fatal(diags)
	}
	if !resp.Schema.Attributes["allow_tenant_administration"].IsRequired() {
		t.Fatal("consent must be explicit")
	}
}

func TestClickHouseAccessProfileOptIn(t *testing.T) {
	for _, field := range []string{"consent false", "consent unknown", "profile", "database"} {
		t.Run(field, func(t *testing.T) {
			m := profileModel()
			switch field {
			case "consent false":
				m.AllowTenantAdministration = types.BoolValue(false)
			case "consent unknown":
				m.AllowTenantAdministration = types.BoolUnknown()
			case "profile":
				m.AccessProfile = types.StringValue("application")
			case "database":
				m.DatabaseID = types.StringUnknown()
			}
			if err := accessProfileOptIn(m); err == nil {
				t.Fatal("accepted missing consent or invalid target")
			}
		})
	}
	if err := accessProfileOptIn(profileModel()); err != nil {
		t.Fatal(err)
	}
}

func TestClickHouseAccessProfileReadDetectsDrift(t *testing.T) {
	r, calls := profileResourceServer(t, 200, "application")
	state := profileState(t, profileModel())
	resp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var m clickHouseAccessProfileModel
	if d := resp.State.Get(context.Background(), &m); d.HasError() {
		t.Fatal(d)
	}
	if m.AccessProfile.ValueString() != "application" || m.Revision.ValueInt64() != 5 || !m.AllowTenantAdministration.ValueBool() || *calls != 1 {
		t.Fatalf("drift missing: %+v calls %d", m, *calls)
	}
}

func TestClickHouseAccessProfileReadMissingOrForbidden(t *testing.T) {
	for _, status := range []int{404, 403} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			r, _ := profileResourceServer(t, status, "application")
			state := profileState(t, profileModel())
			resp := resource.ReadResponse{State: state}
			r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
			if status == 404 && (!resp.State.Raw.IsNull() || resp.Diagnostics.HasError()) {
				t.Fatalf("missing database not removed: %v", resp.Diagnostics)
			}
			if status == 403 && (!resp.Diagnostics.HasError() || resp.State.Raw.IsNull()) {
				t.Fatal("authorization failure discarded state")
			}
		})
	}
}

func TestClickHouseAccessProfileImportDoesNotAuthorizeOrWrite(t *testing.T) {
	r, calls := profileResourceServer(t, 200, "application")
	state := profileState(t, profileModel())
	resp := resource.ImportStateResponse{State: state}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "db1"}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var m clickHouseAccessProfileModel
	if d := resp.State.Get(context.Background(), &m); d.HasError() {
		t.Fatal(d)
	}
	if m.AllowTenantAdministration.ValueBool() || m.AccessProfile.ValueString() != "application" || *calls != 1 {
		t.Fatalf("import changed consent or wrote: %+v", m)
	}
}

func TestClickHouseAccessProfileDeleteOnlyRelinquishes(t *testing.T) {
	r, calls := profileResourceServer(t, 200, "tenant_admin")
	state := profileState(t, profileModel())
	resp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() || !resp.State.Raw.IsNull() || *calls != 0 {
		t.Fatalf("delete mutated database: %v calls %d", resp.Diagnostics, *calls)
	}
}

func TestClickHouseAccessProfileCreateRequiresConsentBeforeRequest(t *testing.T) {
	r, calls := profileResourceServer(t, 200, "tenant_admin")
	m := profileModel()
	m.AllowTenantAdministration = types.BoolValue(false)
	state := profileState(t, m)
	resp := resource.CreateResponse{State: state}
	r.Create(context.Background(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: state.Schema, Raw: state.Raw}}, &resp)
	if !resp.Diagnostics.HasError() || *calls != 0 {
		t.Fatal("create did not stop before network access")
	}
}
