package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hakopod/terraform-provider-hakopod/internal/client"
)

func TestSuspensionDrift(t *testing.T) {
	for _, tc := range []struct {
		name, requested, observed string
		changed, invalid          bool
	}{
		{"resume", `{"services":{"web":{"suspended":false}}}`, `{"services":{"web":{"suspended":true}}}`, true, false},
		{"suspend", `{"services":{"web":{"suspended":true}}}`, `{"services":{"web":{}}}`, true, false},
		{"omitted false", `{"services":{"web":{}}}`, `{"services":{"web":{"suspended":false}}}`, false, false},
		{"same true", `{"services":{"web":{"suspended":true}}}`, `{"services":{"web":{"suspended":true}}}`, false, false},
		{"unrelated fields", `{"services":{"web":{"image":"new","env":{"KEY":"value"}}}}`, `{"services":{"web":{"image":"old"}}}`, false, false},
		{"added service", `{"services":{"web":{"suspended":true},"api":{}}}`, `{"services":{"web":{}}}`, false, false},
		{"different service", `{"services":{"api":{"suspended":true}}}`, `{"services":{"web":{}}}`, false, false},
		{"empty", `{}`, `{"services":{}}`, false, false},
		{"invalid boolean", `{"services":{"web":{"suspended":"secret-value"}}}`, `{"services":{"web":{}}}`, false, true},
		{"null boolean", `{"services":{"web":{"suspended":null}}}`, `{}`, false, true},
		{"invalid observed", `{}`, `{"services":{"web":{"suspended":1}}}`, false, true},
		{"invalid JSON", `secret-value`, `{}`, false, true},
		{"null spec", `null`, `{}`, false, true},
		{"null services", `{"services":null}`, `{}`, false, true},
		{"null service", `{"services":{"web":null}}`, `{}`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := suspensionDrift(json.RawMessage(tc.requested), json.RawMessage(tc.observed))
			if got != tc.changed || (err != nil) != tc.invalid {
				t.Fatalf("changed=%v error=%v", got, err)
			}
			if err != nil && (strings.Contains(err.Error(), "secret-value") || !strings.HasPrefix(err.Error(), "APPLICATION_SUSPENSION_SPEC_INVALID:")) {
				t.Fatal("error must use a fixed diagnostic without spec values")
			}
		})
	}
}

type suspensionFixture struct {
	observed json.RawMessage
	deploys  int
	retain   bool
	getError bool
}

func (f *suspensionFixture) resource(t *testing.T) *applicationResource {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST /api/v1/plan":
			var input struct {
				Spec json.RawMessage `json:"spec"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(w).Encode(client.AppPlan{ApplicationID: appID, ExpectedRevision: 4, Spec: input.Spec})
		case "GET /api/v1/applications/" + appID:
			if f.getError {
				http.Error(w, `{"error":{"code":"unavailable","message":"Unavailable"}}`, 503)
				return
			}
			_ = json.NewEncoder(w).Encode(client.Application{ID: appID, Name: "shop", Status: "healthy", Revision: int64(4 + f.deploys), Spec: f.observed})
		case "POST /api/v1/deployments":
			var input struct {
				Spec json.RawMessage `json:"spec"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			f.deploys++
			if !f.retain {
				f.observed = input.Spec
			}
			_ = json.NewEncoder(w).Encode(client.Deployment{ID: "dep1", ApplicationID: appID, Status: "queued", Revision: 5})
		case "GET /api/v1/deployments/dep1":
			_, _ = io.WriteString(w, `{"id":"dep1","status":"succeeded","revision":5}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", 404)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL, "test", "", "test")
	if err != nil {
		t.Fatal(err)
	}
	return &applicationResource{client: c}
}

func suspensionSpec(suspended string) json.RawMessage {
	return json.RawMessage(`{"name":"shop","services":{"web":{"image":"nginx","suspended":` + suspended + `}}}`)
}

func suspensionModel(spec json.RawMessage) applicationModel {
	m := appModel()
	m.Config = types.StringNull()
	m.Spec = jsonValue{StringValue: types.StringValue(string(spec))}
	m.Name, m.Revision, m.Status = types.StringValue("shop"), types.Int64Value(4), types.StringValue("healthy")
	m.DeploymentID, m.PendingChanges = types.StringValue("previous"), types.Int64Value(0)
	m.ServiceHostnames = serviceHostnames(appID, spec)
	return m
}

func applicationState(t *testing.T, r *applicationResource, m applicationModel) tfsdk.State {
	t.Helper()
	var schema resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schema)
	state := tfsdk.State{Schema: schema.Schema}
	if d := state.Set(context.Background(), &m); d.HasError() {
		t.Fatal(d)
	}
	return state
}

func TestSuspensionModifyPlanAndUpdate(t *testing.T) {
	for _, tc := range []struct {
		name, desired, observed string
		deploy                  bool
	}{
		{"false state recovers live true", "false", "true", true},
		{"true state recovers live false", "true", "false", true},
		{"no-op false", "false", "false", false},
		{"no-op true", "true", "true", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := &suspensionFixture{observed: suspensionSpec(tc.observed)}
			r := f.resource(t)
			m := suspensionModel(suspensionSpec(tc.desired))
			state := applicationState(t, r, m)
			plan := tfsdk.Plan{Schema: state.Schema, Raw: state.Raw}
			response := resource.ModifyPlanResponse{Plan: plan}
			r.ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: plan, State: state}, &response)
			if response.Diagnostics.HasError() {
				t.Fatal(response.Diagnostics)
			}
			var planned applicationModel
			if d := response.Plan.Get(ctx, &planned); d.HasError() {
				t.Fatal(d)
			}
			if planned.Revision.IsUnknown() != tc.deploy || planned.PendingChanges.ValueInt64() != 0 || f.deploys != 0 {
				t.Fatal("plan must identify suspension drift without writing or changing the server count")
			}
			if tc.deploy && len(response.Diagnostics.Warnings()) != 1 {
				t.Fatal("expected omitted-suspension warning")
			}
			updated := resource.UpdateResponse{State: state}
			r.Update(ctx, resource.UpdateRequest{Plan: response.Plan, State: state}, &updated)
			if updated.Diagnostics.HasError() || (f.deploys == 1) != tc.deploy {
				t.Fatalf("deploys=%d diagnostics=%v", f.deploys, updated.Diagnostics)
			}
			if string(f.observed) != string(suspensionSpec(tc.desired)) {
				t.Fatal("applied suspension differs from requested suspension")
			}
		})
	}
}

func TestSuspensionPostWaitMismatch(t *testing.T) {
	f := &suspensionFixture{observed: suspensionSpec("true"), retain: true}
	r := f.resource(t)
	m := suspensionModel(suspensionSpec("false"))
	var d diag.Diagnostics
	saved := false
	r.apply(context.Background(), &m, appID, &d, func() { saved = true })
	if !d.HasError() || !saved || f.deploys != 1 || d.Errors()[0].Summary() != "Application suspension did not match" {
		t.Fatalf("saved=%v deploys=%d diagnostics=%v", saved, f.deploys, d)
	}
}

func TestSuspensionModifyPlanReadFailure(t *testing.T) {
	f := &suspensionFixture{observed: suspensionSpec("true"), getError: true}
	r := f.resource(t)
	state := applicationState(t, r, suspensionModel(suspensionSpec("false")))
	plan := tfsdk.Plan{Schema: state.Schema, Raw: state.Raw}
	response := resource.ModifyPlanResponse{Plan: plan}
	r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{Plan: plan, State: state}, &response)
	if !response.Diagnostics.HasError() || f.deploys != 0 {
		t.Fatal("failed observation must stop planning without deploying")
	}
}

func TestSuspensionReadPreservesConfiguration(t *testing.T) {
	f := &suspensionFixture{observed: suspensionSpec("true")}
	r := f.resource(t)
	m := suspensionModel(suspensionSpec("false"))
	state := applicationState(t, r, m)
	response := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &response)
	var read applicationModel
	response.Diagnostics.Append(response.State.Get(context.Background(), &read)...)
	if response.Diagnostics.HasError() || !read.Spec.Equal(m.Spec) || f.deploys != 0 {
		t.Fatal("Read must preserve input configuration and must not deploy")
	}
}

func TestSuspensionApplyRejectsMalformedObservation(t *testing.T) {
	f := &suspensionFixture{observed: suspensionSpec(`"secret-value"`)}
	r := f.resource(t)
	m := suspensionModel(suspensionSpec("false"))
	var d diag.Diagnostics
	saved := false
	r.apply(context.Background(), &m, appID, &d, func() { saved = true })
	if !d.HasError() || saved || f.deploys != 0 || strings.Contains(d.Errors()[0].Detail(), "secret-value") {
		t.Fatal("malformed observation must fail before deployment without spec values")
	}
}
