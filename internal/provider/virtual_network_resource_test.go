package provider

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/hakopod/terraform-provider-hakopod/internal/client"
)

func TestVirtualNetworkImportID(t *testing.T) {
	p, e, n, err := parseNetworkImportID("shop/prod/backend")
	if err != nil || p != "shop" || e != "prod" || n != "backend" {
		t.Fatalf("got %q %q %q %v", p, e, n, err)
	}
	for _, bad := range []string{"", "shop/prod", "shop//backend", "a/b/c/d", "/prod/backend"} {
		if _, _, _, err := parseNetworkImportID(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestVirtualNetworkSpecSemanticEquals(t *testing.T) {
	v := func(s string) jsonValue { return jsonValue{StringValue: basetypes.NewStringValue(s)} }
	a := v(`{"name":"backend","segments":{"db":{"applications":["api"]}}}`)
	b := v("{\n  \"segments\": {\"db\": {\"applications\": [\"api\"]}},\n  \"name\": \"backend\"\n}")
	if ok, _ := a.StringSemanticEquals(context.Background(), b); !ok {
		t.Error("key order / whitespace must not differ")
	}
	c := v(`{"name":"backend","segments":{"db":{"applications":["web"]}}}`)
	if ok, _ := a.StringSemanticEquals(context.Background(), c); ok {
		t.Error("different grants must differ")
	}
}

func networkPlan(id string, rev int64) client.NetworkPlan {
	return client.NetworkPlan{Spec: json.RawMessage(`{"schema_version":1,"name":"backend","segments":{}}`), ExpectedID: id, ExpectedRevision: rev}
}

func TestVirtualNetworkCreateWhenExists(t *testing.T) {
	if err := createCheck("shop", "prod", networkPlan("", 0)); err != nil {
		t.Fatalf("new network: %v", err)
	}
	err := createCheck("shop", "prod", networkPlan(strings.Repeat("a", 32), 3))
	want := "virtual network backend already exists in shop/prod; import it with terraform import hakopod_virtual_network.<x> shop/prod/backend"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v", err)
	}
}

func TestVirtualNetworkUpdateIDMismatch(t *testing.T) {
	id := strings.Repeat("a", 32)
	if err := updateCheck(id, 2, networkPlan(id, 2)); err != nil {
		t.Fatalf("same id: %v", err)
	}
	if err := updateCheck(id, 1, networkPlan(strings.Repeat("b", 32), 1)); err == nil || !strings.Contains(err.Error(), "recreated outside Terraform") {
		t.Fatalf("got %v", err)
	}
	if err := updateCheck(id, 0, networkPlan("", 0)); err == nil {
		t.Fatal("deleted on server must error")
	}
	if err := updateCheck(id, 2, networkPlan(id, 3)); err == nil || !strings.Contains(err.Error(), "changed on the server after it was reviewed") {
		t.Fatalf("a change between plan and apply must be refused, got %v", err)
	}
}
