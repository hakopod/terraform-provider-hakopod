package provider

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func secretServer(t *testing.T, reply func(c projectCall) (int, string)) (*secretResource, *[]projectCall) {
	pr, calls := projectServer(t, reply)
	return &secretResource{client: pr.client}, calls
}

func secretPlan(value string, generate bool, format string) secretModel {
	m := secretModel{Project: types.StringValue("shop"), Environment: types.StringValue("prod"), Application: types.StringValue("api"),
		Name: types.StringValue("db"), Value: types.StringNull(), Generate: types.BoolNull(), Format: types.StringNull(),
		ID: types.StringUnknown(), ValueVersion: types.StringUnknown()}
	if value != "" {
		m.Value = types.StringValue(value)
	}
	if generate {
		m.Generate = types.BoolValue(true)
	}
	if format != "" {
		m.Format = types.StringValue(format)
	}
	return m
}

func TestSecretCreateWithValue(t *testing.T) {
	r, calls := secretServer(t, func(projectCall) (int, string) { return 201, `{"name":"db","saved":true}` })
	m := secretPlan("s3cret", false, "")
	var diags diag.Diagnostics
	if !r.create(context.Background(), &m, &diags) {
		t.Fatal(diags)
	}
	want := []projectCall{{Method: "POST", Path: "/api/v1/secrets/db", Body: map[string]any{"value": "s3cret"}}}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("calls %+v", *calls)
	}
	if m.ID.ValueString() != "shop/prod/api/db" || m.ValueVersion.ValueString() != valueVersion("s3cret") || len(m.ValueVersion.ValueString()) != 16 {
		t.Fatalf("model %+v", m)
	}
}

func TestSecretCreateGenerateSendsNoValue(t *testing.T) {
	r, calls := secretServer(t, func(projectCall) (int, string) { return 201, `{"name":"db","saved":true}` })
	m := secretPlan("", true, "hex")
	var diags diag.Diagnostics
	if !r.create(context.Background(), &m, &diags) {
		t.Fatal(diags)
	}
	want := []projectCall{{Method: "POST", Path: "/api/v1/secrets/db", Body: map[string]any{"generate": true, "format": "hex"}}}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("calls %+v", *calls)
	}
	if !m.Value.IsNull() || len(m.ValueVersion.ValueString()) != 16 {
		t.Fatalf("generated value leaked into state or no version: %+v", m)
	}
}

func TestSecretCreateExistsGivesImportGuidance(t *testing.T) {
	r, _ := secretServer(t, func(projectCall) (int, string) {
		return 409, `{"error":{"code":"secret_exists","message":"This secret already exists."}}`
	})
	m := secretPlan("x", false, "")
	var diags diag.Diagnostics
	if r.create(context.Background(), &m, &diags) || !strings.Contains(diags[0].Detail(), "terraform import hakopod_secret.<x> shop/prod/api/db") {
		t.Fatalf("diags %v", diags)
	}
}

func TestSecretUpdatePutsAndWarns(t *testing.T) {
	r, calls := secretServer(t, func(projectCall) (int, string) { return 200, `{"name":"db","saved":true,"restart_required":true}` })
	state := secretPlan("old", false, "")
	state.ValueVersion = types.StringValue(valueVersion("old"))
	m := secretPlan("new", false, "")
	var diags diag.Diagnostics
	if !r.update(context.Background(), &m, state, &diags) {
		t.Fatal(diags)
	}
	want := []projectCall{{Method: "PUT", Path: "/api/v1/secrets/db", Body: map[string]any{"value": "new"}}}
	if !reflect.DeepEqual(*calls, want) || m.ValueVersion.ValueString() != valueVersion("new") {
		t.Fatalf("calls %+v model %+v", *calls, m)
	}
	if len(diags) != 1 || !strings.Contains(diags[0].Detail(), "application api") {
		t.Fatalf("want restart warning, got %v", diags)
	}
}

func TestSecretReadMissingIsRemoved(t *testing.T) {
	r, calls := secretServer(t, func(projectCall) (int, string) { return 200, `{"items":[{"name":"other","updated_at":"2026-01-01T00:00:00Z"}]}` })
	found, err := r.exists(context.Background(), secretPlan("x", false, ""))
	if err != nil || found || (*calls)[0].Method != http.MethodGet {
		t.Fatalf("found=%v err=%v", found, err)
	}
}

func TestSecretValueVersionChangesWithValue(t *testing.T) {
	if valueVersion("a") == valueVersion("b") || valueVersion("a") != valueVersion("a") {
		t.Fatal("value_version must track the value")
	}
	state := secretPlan("a", false, "")
	plan := secretPlan("b", false, "")
	if planSecret(&plan, &state) != nil || plan.ValueVersion.ValueString() != valueVersion("b") {
		t.Fatalf("value change must be in place with a new version: %+v", plan)
	}
}

func TestSecretPlanReplacementRules(t *testing.T) {
	gen := secretPlan("", true, "")
	gen.ValueVersion = types.StringValue("0123456789abcdef")
	imported := secretPlan("", false, "")
	cases := []struct {
		name        string
		plan, state secretModel
		replace     path.Paths
		version     string // "" means unknown
	}{
		{"generate unchanged", secretPlan("", true, "base64url"), gen, nil, "0123456789abcdef"},
		{"format change", secretPlan("", true, "hex"), gen, path.Paths{path.Root("format")}, ""},
		{"generate to value", secretPlan("v", false, ""), gen, path.Paths{path.Root("generate")}, valueVersion("v")},
		{"value to generate", secretPlan("", true, ""), secretPlan("v", false, ""), path.Paths{path.Root("value")}, ""},
		{"import adopts value", secretPlan("v", false, ""), imported, nil, valueVersion("v")},
		{"import adopts generate", secretPlan("", true, "hex"), imported, nil, ""},
	}
	for _, c := range cases {
		got := planSecret(&c.plan, &c.state)
		if !reflect.DeepEqual(got, c.replace) || (c.version == "") != c.plan.ValueVersion.IsUnknown() || (c.version != "" && c.plan.ValueVersion.ValueString() != c.version) {
			t.Errorf("%s: replace %v version %v", c.name, got, c.plan.ValueVersion)
		}
	}
}

func TestSecretImportAdoptsGenerateWithoutWrite(t *testing.T) {
	r, calls := secretServer(t, func(projectCall) (int, string) { return 500, `{}` })
	m := secretPlan("", true, "")
	var diags diag.Diagnostics
	if !r.update(context.Background(), &m, secretPlan("", false, ""), &diags) || len(*calls) != 0 || len(m.ValueVersion.ValueString()) != 16 {
		t.Fatalf("calls %+v diags %v", *calls, diags)
	}
}
