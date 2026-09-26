package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hakopod/terraform-provider-hakopod/internal/client"
)

type projectCall struct {
	Method, Path string
	Body         map[string]any
}

// projectServer records every request and answers with reply.
func projectServer(t *testing.T, reply func(c projectCall) (int, string)) (*projectResource, *[]projectCall) {
	t.Helper()
	var calls []projectCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := projectCall{Method: r.Method, Path: r.URL.Path}
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			_ = json.Unmarshal(b, &c.Body)
		}
		calls = append(calls, c)
		status, body := reply(c)
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL, "k", "", "test")
	if err != nil {
		t.Fatal(err)
	}
	return &projectResource{client: c}, &calls
}

const shopListing = `{"items":[{"id":"shop","name":"shop","display_name":"shop","description":"","metadata_revision":1,"environments":[{"name":"dev"},{"name":"prod"},{"name":"staging"}]}]}`

func projectPlan(envs ...string) projectModel {
	return projectModel{Name: types.StringValue("shop"), Environments: stringList(envs),
		DisplayName: types.StringUnknown(), Description: types.StringUnknown(), ID: types.StringUnknown(), MetadataRevision: types.Int64Unknown()}
}

func writes(calls []projectCall) []projectCall {
	var out []projectCall
	for _, c := range calls {
		if c.Method != http.MethodGet {
			out = append(out, c)
		}
	}
	return out
}

func TestProjectCreateThreeEnvironments(t *testing.T) {
	created := false
	r, calls := projectServer(t, func(c projectCall) (int, string) {
		if c.Method == http.MethodGet {
			if created {
				return 200, shopListing
			}
			return 200, `{"items":[]}`
		}
		created = true
		return 201, `{"name":"shop"}`
	})
	m := projectPlan("prod", "staging", "dev")
	var diags diag.Diagnostics
	if !r.create(context.Background(), &m, &diags) || diags.HasError() {
		t.Fatalf("create failed: %v", diags)
	}
	want := []projectCall{
		{"POST", "/api/v1/projects", map[string]any{"name": "shop", "environment": "prod", "display_name": "shop"}}, // display_name makes the server answer 409 for an existing project
		{"POST", "/api/v1/projects/shop/environments", map[string]any{"name": "staging"}},
		{"POST", "/api/v1/projects/shop/environments", map[string]any{"name": "dev"}},
	}
	if got := writes(*calls); !reflect.DeepEqual(got, want) {
		t.Fatalf("writes %+v", got)
	}
	if got := listStrings(m.Environments); !reflect.DeepEqual(got, []string{"prod", "staging", "dev"}) {
		t.Errorf("state environments %v", got)
	}
	if m.ID.ValueString() != "shop" || m.DisplayName.ValueString() != "shop" || m.Description.ValueString() != "" || m.MetadataRevision.ValueInt64() != 1 {
		t.Errorf("state %+v", m)
	}
}

func TestProjectCreateWhenExists(t *testing.T) {
	// Listed already: no write at all (the server would silently add the environment).
	r, calls := projectServer(t, func(projectCall) (int, string) { return 200, shopListing })
	m, diags := projectPlan("prod"), diag.Diagnostics{}
	if r.create(context.Background(), &m, &diags) || !strings.Contains(diags.Errors()[0].Detail(), "terraform import hakopod_project.<x> shop") {
		t.Fatalf("got %v", diags)
	}
	if len(writes(*calls)) != 0 {
		t.Fatalf("writes %+v", *calls)
	}
	// Not visible to the key but taken, or retired.
	for code, want := range map[string]string{"project_exists": "terraform import hakopod_project.<x> shop", "retired_project": "deleted project IDs cannot be reused"} {
		r, _ := projectServer(t, func(c projectCall) (int, string) {
			if c.Method == http.MethodGet {
				return 200, `{"items":[]}`
			}
			return 409, `{"error":{"code":"` + code + `","message":"server says no"}}`
		})
		m, diags := projectPlan("prod"), diag.Diagnostics{}
		if r.create(context.Background(), &m, &diags) || !strings.Contains(diags.Errors()[0].Detail(), want) || !strings.Contains(diags.Errors()[0].Detail(), "server says no") {
			t.Errorf("%s: got %v", code, diags)
		}
	}
}

func TestProjectEnvironmentRemovalAndDescription(t *testing.T) {
	state := projectPlan("prod", "staging")
	state.Description = types.StringValue("d")
	// Reordering and adding are fine.
	plan := projectPlan("staging", "prod", "dev")
	plan.Description = types.StringValue("d")
	if d := checkProjectChange(plan, state); d.HasError() {
		t.Fatalf("reorder/add: %v", d)
	}
	plan = projectPlan("prod")
	plan.Description = types.StringValue("d")
	if d := checkProjectChange(plan, state); !d.HasError() || !strings.Contains(d.Errors()[0].Detail(), "Hakopod cannot delete an environment ([staging])") {
		t.Fatalf("removal: %v", d)
	}
	plan = projectPlan("prod", "staging")
	plan.Description = types.StringValue("new")
	if d := checkProjectChange(plan, state); !d.HasError() || !strings.Contains(d.Errors()[0].Detail(), "dashboard") {
		t.Fatalf("description: %v", d)
	}
	// Unknown environments (e.g. from another resource) are not checked.
	plan = projectPlan()
	plan.Environments, plan.Description = types.ListUnknown(types.StringType), types.StringValue("d")
	if d := checkProjectChange(plan, state); d.HasError() {
		t.Fatalf("unknown: %v", d)
	}
}

func TestProjectReadOrderStable(t *testing.T) {
	for _, tc := range []struct{ prior, server, want []string }{
		{[]string{"prod", "staging", "dev"}, []string{"dev", "prod", "staging"}, []string{"prod", "staging", "dev"}},
		{[]string{"prod", "dev"}, []string{"dev", "prod", "qa"}, []string{"prod", "dev", "qa"}}, // drift appended
		{[]string{"prod", "gone"}, []string{"prod"}, []string{"prod"}},
		{nil, []string{"dev", "prod"}, []string{"dev", "prod"}}, // import
	} {
		if got := orderEnvironments(tc.prior, tc.server); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%v + %v: got %v", tc.prior, tc.server, got)
		}
	}
}

func TestProjectDelete(t *testing.T) {
	r, calls := projectServer(t, func(projectCall) (int, string) { return 200, `{"status":"deleted"}` })
	if err := r.client.DeleteProject(context.Background(), "shop", "shop"); err != nil {
		t.Fatal(err)
	}
	want := []projectCall{{"DELETE", "/api/v1/projects/shop", map[string]any{"confirm_name": "shop"}}}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("calls %+v", *calls)
	}
}

func TestProjectSchema(t *testing.T) {
	var resp resource.SchemaResponse
	NewProjectResource().Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if d := resp.Schema.ValidateImplementation(context.Background()); d.HasError() {
		t.Fatalf("schema invalid: %v", d)
	}
}

func TestProjectDeleteWaitsForDataCleanup(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(409)
			w.Write([]byte(`{"error":{"code":"conflict","message":"reclaim their retained data first"}}`))
			return
		}
		w.Write([]byte(`{"status":"deleted"}`))
	}))
	defer srv.Close()
	c, err := client.New(srv.URL, "k", "", "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := deleteProjectWhenDrained(context.Background(), c, "shop", "shop", time.Second, time.Millisecond); err != nil || attempts != 3 {
		t.Fatalf("expected success on the third attempt, got %v after %d", err, attempts)
	}
	attempts = -1000
	if err := deleteProjectWhenDrained(context.Background(), c, "shop", "shop", 20*time.Millisecond, 5*time.Millisecond); !client.IsConflict(err) {
		t.Fatalf("expected the conflict once the limit passes, got %v", err)
	}
}
