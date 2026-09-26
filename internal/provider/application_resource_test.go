package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/hakopod/terraform-provider-hakopod/internal/client"
)

const appID = "0123456789abcdef0123456789abcdef"

// fakeHakopod answers the endpoints the resource uses and records writes.
type fakeHakopod struct {
	status  string // application status
	changes int
	deploys []json.RawMessage // specs sent to POST /deployments
	deleted map[string]any
}

func (f *fakeHakopod) server(t *testing.T) *applicationResource {
	spec := `{"schema_version":1,"name":"shop","services":{"web":{"image":"nginx"}},"domains":{"shop.example.com":"web"},"volumes":{"data":{"size_gib":1,"access_mode":"rwo"}},"env":{"A":"1"}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var in map[string]json.RawMessage
		_ = json.Unmarshal(body, &in)
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/v1/plan":
			s := in["spec"]
			if s == nil {
				s = json.RawMessage(spec)
			}
			changes := make([]json.RawMessage, f.changes)
			for i := range changes {
				changes[i] = json.RawMessage(`{}`)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"application_id": appID, "expected_revision": 4, "spec": s, "changes": changes})
		case r.Method == "POST" && r.URL.Path == "/api/v1/deployments":
			f.deploys = append(f.deploys, in["spec"])
			_ = json.NewEncoder(w).Encode(client.Deployment{ID: "dep1", ApplicationID: appID, Status: "queued", Revision: 5})
		case r.Method == "GET" && r.URL.Path == "/api/v1/deployments/dep1":
			_ = json.NewEncoder(w).Encode(client.Deployment{ID: "dep1", ApplicationID: appID, Status: "succeeded", Revision: 5})
		case r.Method == "GET" && r.URL.Path == "/api/v1/applications/"+appID:
			_ = json.NewEncoder(w).Encode(client.Application{ID: appID, Name: "shop", Project: "p", Environment: "e", Status: f.status, Revision: 4, Spec: json.RawMessage(spec)})
		case r.Method == "DELETE" && r.URL.Path == "/api/v1/applications/"+appID:
			_ = json.Unmarshal(body, &f.deleted)
			_, _ = w.Write([]byte(`{"status":"deleted"}`))
		default:
			http.Error(w, `{"error":{"code":"not_found","message":"no route"}}`, 404)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL, "key", "", "test")
	if err != nil {
		t.Fatal(err)
	}
	return &applicationResource{client: c}
}

func appModel() applicationModel {
	return applicationModel{
		Project: types.StringValue("p"), Environment: types.StringValue("e"),
		Config: types.StringValue("name = \"shop\""), Spec: jsonValue{StringValue: types.StringNull()}, EnvFiles: types.MapNull(types.StringType),
		Wait: types.BoolValue(true), WaitTimeout: types.StringValue("20m"), DeleteDataOnDestroy: types.BoolValue(false),
		ID: types.StringValue(appID), Name: types.StringUnknown(), Revision: types.Int64Unknown(), Status: types.StringUnknown(),
		DeploymentID: types.StringNull(), PendingChanges: types.Int64Unknown(),
	}
}

func TestApplicationSkipRule(t *testing.T) {
	for _, tc := range []struct {
		status  string
		changes int
		deploy  bool
	}{{"healthy", 0, false}, {"recovered", 0, true}, {"healthy", 2, true}, {"failed", 0, true}} {
		f := &fakeHakopod{status: tc.status, changes: tc.changes}
		r := f.server(t)
		m := appModel()
		var diags diag.Diagnostics
		saved := false
		r.apply(context.Background(), &m, appID, &diags, func() { saved = true })
		if diags.HasError() || !saved {
			t.Fatalf("%+v: diags %v saved %v", tc, diags, saved)
		}
		if got := len(f.deploys) > 0; got != tc.deploy {
			t.Errorf("%s with %d changes: deployed=%v, want %v", tc.status, tc.changes, got, tc.deploy)
		}
		if m.Name.ValueString() != "shop" || m.Revision.IsUnknown() || m.Status.IsUnknown() || m.PendingChanges.ValueInt64() != int64(tc.changes) {
			t.Errorf("%+v: unresolved model %+v", tc, m)
		}
		if tc.deploy && m.DeploymentID.ValueString() != "dep1" {
			t.Errorf("%+v: deployment_id %v", tc, m.DeploymentID)
		}
	}
}

func TestApplicationDeleteDeploysEmptyRevisionFirst(t *testing.T) {
	f := &fakeHakopod{status: "healthy"}
	r := f.server(t)
	m := appModel()
	m.DeleteDataOnDestroy = types.BoolValue(true)
	if err := r.delete(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if len(f.deploys) != 1 {
		t.Fatalf("deploys %d", len(f.deploys))
	}
	var sent map[string]json.RawMessage
	_ = json.Unmarshal(f.deploys[0], &sent)
	if string(sent["services"]) != "{}" || sent["domains"] != nil || sent["volumes"] == nil || string(sent["name"]) != `"shop"` {
		t.Errorf("empty revision %s", f.deploys[0])
	}
	if f.deleted["confirm_name"] != "shop" || f.deleted["expected_revision"] != float64(5) || f.deleted["delete_data"] != true {
		t.Errorf("delete body %v", f.deleted)
	}
}

func TestApplicationEmptySpec(t *testing.T) {
	out, err := emptySpec(json.RawMessage(`{"schema_version":1,"name":"a","services":{"x":{}},"domains":{"d":"x"},"secrets":{"S":{"ref":"s"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !jsonEqual(string(out), `{"schema_version":1,"name":"a","services":{},"secrets":{"S":{"ref":"s"}}}`) {
		t.Errorf("got %s", out)
	}
	if !servicesEmpty(out) || servicesEmpty(json.RawMessage(`{"name":"a"}`)) {
		t.Error("servicesEmpty: explicit {} only")
	}
	if _, err := emptySpec(json.RawMessage(`not json`)); err == nil {
		t.Error("bad spec must error")
	}
}

func TestApplicationSpecSemanticEquality(t *testing.T) {
	a := `{"name":"shop","services":{"web":{"image":"nginx","port":80}}}`
	if !jsonEqual(a, "{\n \"services\": {\"web\": {\"port\": 80, \"image\": \"nginx\"}},\n \"name\": \"shop\"}") {
		t.Error("key order / whitespace must not differ")
	}
	if jsonEqual(a, strings.Replace(a, "80", "81", 1)) {
		t.Error("value change must differ")
	}
}

func TestApplicationIdempotencyKeyFresh(t *testing.T) {
	a, _ := idempotencyKey()
	b, _ := idempotencyKey()
	if len(a) != 32 || a == b {
		t.Errorf("%q %q", a, b)
	}
}

func TestApplicationCreateExistingApp(t *testing.T) {
	for _, tc := range []struct {
		name, app string
		adopt     bool
	}{
		{"working app is refused", `{"id":"` + appID + `","name":"shop","status":"healthy","deployments":[{"status":"succeeded"}]}`, false},
		{"recovered app is refused", `{"id":"` + appID + `","name":"shop","status":"recovered","deployments":[{"status":"failed"}]}`, false},
		{"never-succeeded app is adopted", `{"id":"` + appID + `","name":"shop","status":"failed","revision":1,"deployments":[{"status":"failed"}]}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deployed := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.Path {
				case "POST /api/v1/plan":
					w.Write([]byte(`{"application_id":"` + appID + `","expected_revision":1,"spec":{"name":"shop"},"changes":[]}`))
				case "GET /api/v1/applications/" + appID:
					w.Write([]byte(tc.app))
				case "POST /api/v1/deployments":
					deployed++
					w.Write([]byte(`{"id":"d1","application_id":"` + appID + `","status":"queued","revision":2}`))
				default:
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
				}
			}))
			defer srv.Close()
			c, err := client.New(srv.URL, "k", "", "test")
			if err != nil {
				t.Fatal(err)
			}
			r := &applicationResource{client: c}
			m := applicationModel{Project: types.StringValue("p"), Environment: types.StringValue("e"), Config: types.StringValue("name = 'shop'"), Spec: jsonValue{StringValue: basetypes.NewStringNull()}, Wait: types.BoolValue(false), WaitTimeout: types.StringValue("1m"), Name: types.StringUnknown(), PendingChanges: types.Int64Unknown()}
			var diags diag.Diagnostics
			saved := false
			r.apply(context.Background(), &m, "", &diags, func() { saved = true })
			if tc.adopt {
				if diags.HasError() || deployed != 1 || !saved {
					t.Fatalf("expected adoption and one deployment, got deployed=%d saved=%v %v", deployed, saved, diags)
				}
				return
			}
			if !diags.HasError() || !strings.Contains(diags.Errors()[0].Detail(), "terraform import") || deployed != 0 || saved {
				t.Fatalf("expected import guidance and no writes, got deployed=%d saved=%v %v", deployed, saved, diags)
			}
		})
	}
}

func TestServiceHostnamesMatchServerNamespace(t *testing.T) {
	// Namespace("0123…") in the server is "hp-" + hex(sha256(id)[:16]).
	got := serviceHostnames(appID, json.RawMessage(`{"services":{"api":{},"db":{}}}`))
	sum := sha256.Sum256([]byte(appID))
	ns := "hp-" + hex.EncodeToString(sum[:16])
	want := map[string]string{"api": "api." + ns + ".svc.cluster.local", "db": "db." + ns + ".svc.cluster.local"}
	var m map[string]string
	got.ElementsAs(context.Background(), &m, false)
	if !reflect.DeepEqual(m, want) {
		t.Fatalf("got %v want %v", m, want)
	}
}
