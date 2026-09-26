package client

import (
	"context"
	"reflect"
	"testing"
)

func TestProjectRequests(t *testing.T) {
	c, calls := server(t,
		[2]string{"201", `{"name":"shop","environment":"prod","display_name":"Shop","description":""}`},
		[2]string{"201", `{"project":"shop","name":"staging"}`},
		[2]string{"200", `{"items":[{"id":"other","name":"other","environments":[]},{"id":"shop","name":"shop","display_name":"Shop","description":"d","metadata_revision":4,"environments":[{"name":"prod"},{"name":"staging"}]}]}`},
		[2]string{"200", `{"status":"renamed"}`},
		[2]string{"200", `{"status":"deleted"}`},
	)
	ctx := context.Background()
	if _, err := c.CreateProject(ctx, "shop", "prod", "Shop", ""); err != nil {
		t.Fatal(err)
	}
	if err := c.CreateEnvironment(ctx, "shop", "staging"); err != nil {
		t.Fatal(err)
	}
	p, err := c.GetProject(ctx, "shop")
	want := Project{ID: "shop", Name: "shop", DisplayName: "Shop", Description: "d", MetadataRevision: 4, Environments: []string{"prod", "staging"}}
	if err != nil || !reflect.DeepEqual(p, want) {
		t.Fatalf("get %+v %v", p, err)
	}
	if err := c.RenameProject(ctx, "shop", "Store", 4); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteProject(ctx, "shop", "shop"); err != nil {
		t.Fatal(err)
	}
	cs := *calls
	check := func(i int, method, path string, body map[string]any) {
		t.Helper()
		if cs[i].Method != method || cs[i].Path != path || !reflect.DeepEqual(cs[i].Body, body) {
			t.Errorf("call %d: %+v", i, cs[i])
		}
	}
	check(0, "POST", "/api/v1/projects", map[string]any{"name": "shop", "environment": "prod", "display_name": "Shop"})
	check(1, "POST", "/api/v1/projects/shop/environments", map[string]any{"name": "staging"})
	check(2, "GET", "/api/v1/projects", nil)
	check(3, "PUT", "/api/v1/projects/shop/name", map[string]any{"display_name": "Store", "expected_metadata_revision": float64(4)})
	check(4, "DELETE", "/api/v1/projects/shop", map[string]any{"confirm_name": "shop"})
}

func TestGetProjectNotFound(t *testing.T) {
	c, _ := server(t, [2]string{"200", `{"items":[]}`})
	if _, err := c.GetProject(context.Background(), "shop"); !IsNotFound(err) {
		t.Fatalf("got %v", err)
	}
}
