package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

type call struct {
	Method, Path, Query string
	Header              http.Header
	Body                map[string]any
}

// server records each request and replies with the next (status, body) from replies
// (the last reply repeats).
func server(t *testing.T, replies ...[2]string) (*Client, *[]call) {
	t.Helper()
	var calls []call
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := call{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Header: r.Header.Clone()}
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			if err := json.Unmarshal(b, &c.Body); err != nil {
				t.Errorf("request body is not JSON: %s", b)
			}
		}
		calls = append(calls, c)
		i := int(atomic.AddInt32(&n, 1)) - 1
		if i >= len(replies) {
			i = len(replies) - 1
		}
		status, _ := strconv.Atoi(replies[i][0])
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, replies[i][1])
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL+"/", "key-1", "ws-1", "terraform-provider-hakopod/test")
	if err != nil {
		t.Fatal(err)
	}
	return c, &calls
}

func keys(m map[string]any) map[string]bool {
	out := map[string]bool{}
	for k := range m {
		out[k] = true
	}
	return out
}

func TestNewValidatesURL(t *testing.T) {
	for u, ok := range map[string]bool{
		"https://hakopod.example.com/": true, "http://127.0.0.1:8080": true, "http://localhost:1": true, "http://[::1]:9": true,
		"http://hakopod.example.com": false, "ftp://x": false, "not a url": false, "https://x/?a=b": false,
	} {
		if _, err := New(u, "", "", ""); (err == nil) != ok {
			t.Errorf("New(%q) err=%v, want ok=%v", u, err, ok)
		}
	}
}

func TestHeadersAndAPIError(t *testing.T) {
	c, calls := server(t, [2]string{"404", `{"error":{"code":"not_found","message":"resource not found"}}`})
	_, err := c.GetApplication(context.Background(), "app1")
	h := (*calls)[0].Header
	if h.Get("Authorization") != "Bearer key-1" || h.Get("X-Hakopod-Workspace") != "ws-1" || h.Get("Accept") != "application/json" || h.Get("User-Agent") != "terraform-provider-hakopod/test" {
		t.Fatalf("headers: %v", h)
	}
	var e *APIError
	if !errors.As(err, &e) || !IsNotFound(err) || IsConflict(err) || e.Error() != "not_found: resource not found (HTTP 404)" {
		t.Fatalf("err = %v", err)
	}
	if (*calls)[0].Path != "/api/v1/applications/app1" {
		t.Fatalf("path %s", (*calls)[0].Path)
	}

	c, _ = server(t, [2]string{"409", `not json`})
	_, err = c.Deploy(context.Background(), "p", "e", json.RawMessage(`{}`), 1, "")
	if !IsConflict(err) || err.Error() != "http_error: Conflict (HTTP 409)" {
		t.Fatalf("fallback err = %v", err)
	}
}

func TestPlanApplicationSendsExactlyOne(t *testing.T) {
	c, calls := server(t, [2]string{"200", `{"application_id":"a","expected_revision":3,"spec":{"name":"web"},"changes":[{"x":1}],"warnings":[],"missing_secrets":["S"]}`})
	ctx := context.Background()
	p, err := c.PlanApplication(ctx, PlanInput{Project: "p", Environment: "e", TOML: "name='web'", EnvFiles: map[string]string{".env": "A=1"}})
	if err != nil || p.ExpectedRevision != 3 || len(p.Changes) != 1 || p.MissingSecrets[0] != "S" {
		t.Fatalf("plan %+v %v", p, err)
	}
	if k := keys((*calls)[0].Body); !k["toml"] || k["spec"] || !k["env_files"] || (*calls)[0].Path != "/api/v1/plan" {
		t.Fatalf("toml body %v", (*calls)[0].Body)
	}
	if _, err = c.PlanApplication(ctx, PlanInput{Project: "p", Environment: "e", Spec: json.RawMessage(`{"name":"web"}`)}); err != nil {
		t.Fatal(err)
	}
	if k := keys((*calls)[1].Body); k["toml"] || !k["spec"] || k["env_files"] {
		t.Fatalf("spec body %v", (*calls)[1].Body)
	}
	for _, bad := range []PlanInput{{}, {TOML: "x", Spec: json.RawMessage(`{}`)}, {Spec: json.RawMessage(`{}`), EnvFiles: map[string]string{"a": "b"}}} {
		if _, err := c.PlanApplication(ctx, bad); err == nil {
			t.Errorf("expected error for %+v", bad)
		}
	}
	if len(*calls) != 2 {
		t.Fatalf("invalid input reached the server: %d calls", len(*calls))
	}
}

func TestDeployBodyAndIdempotency(t *testing.T) {
	c, calls := server(t, [2]string{"202", `{"id":"d1","application_id":"a1","status":"queued","revision":4,"error":""}`})
	d, err := c.Deploy(context.Background(), "p", "e", json.RawMessage(`{"name":"web"}`), 3, "idem-1")
	if err != nil || d.ID != "d1" || d.Revision != 4 || d.Status != "queued" {
		t.Fatalf("%+v %v", d, err)
	}
	got := (*calls)[0]
	if got.Method != "POST" || got.Path != "/api/v1/deployments" || got.Header.Get("Idempotency-Key") != "idem-1" {
		t.Fatalf("%+v", got)
	}
	if got.Body["expected_revision"] != float64(3) || got.Body["spec"].(map[string]any)["name"] != "web" || got.Body["project"] != "p" || len(got.Body) != 4 {
		t.Fatalf("body %v", got.Body)
	}
}

func TestWaitDeployment(t *testing.T) {
	c, calls := server(t,
		[2]string{"200", `{"id":"d1","status":"queued"}`},
		[2]string{"200", `{"id":"d1","status":"running"}`},
		[2]string{"200", `{"id":"d1","status":"failed","error":"boom"}`})
	d, err := c.WaitDeployment(context.Background(), "d1", time.Millisecond)
	if err != nil || d.Status != "failed" || d.Error != "boom" || len(*calls) != 3 {
		t.Fatalf("%+v %v calls=%d", d, err, len(*calls))
	}
	c, _ = server(t, [2]string{"200", `{"id":"d1","status":"running"}`})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := c.WaitDeployment(ctx, "d1", 5*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline, got %v", err)
	}
}

func TestFindApplicationPagesAndNotFound(t *testing.T) {
	c, calls := server(t,
		[2]string{"200", `{"items":[{"id":"1","name":"a"}],"next_cursor":"c2"}`},
		[2]string{"200", `{"items":[{"id":"2","name":"web","revision":7,"spec":{"name":"web"}}],"next_cursor":""}`})
	a, err := c.FindApplication(context.Background(), "p", "e", "web")
	if err != nil || a.ID != "2" || a.Revision != 7 || (*calls)[1].Query != "cursor=c2&environment=e&project=p" {
		t.Fatalf("%+v %v %v", a, err, *calls)
	}
	c, _ = server(t, [2]string{"200", `{"items":[],"next_cursor":""}`})
	if _, err := c.FindApplication(context.Background(), "p", "e", "web"); !IsNotFound(err) {
		t.Fatalf("want 404, got %v", err)
	}
}

func TestDeleteBodies(t *testing.T) {
	c, calls := server(t, [2]string{"200", `{"status":"deleted"}`})
	if err := c.DeleteApplication(context.Background(), "a1", "web", 5, true); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteNetwork(context.Background(), "p", "e", "mesh", "0123456789abcdef0123456789abcdef", 2); err != nil {
		t.Fatal(err)
	}
	a, n := (*calls)[0], (*calls)[1]
	if a.Method != "DELETE" || a.Path != "/api/v1/applications/a1" || a.Body["confirm_name"] != "web" || a.Body["expected_revision"] != float64(5) || a.Body["delete_data"] != true || len(a.Body) != 3 {
		t.Fatalf("app delete %+v", a)
	}
	if n.Method != "DELETE" || n.Path != "/api/v1/virtual-networks/mesh" || n.Body["confirmation"] != "mesh" || n.Body["expected_id"] != "0123456789abcdef0123456789abcdef" || n.Body["expected_revision"] != float64(2) || n.Body["project"] != "p" || n.Body["environment"] != "e" || len(n.Body) != 5 {
		t.Fatalf("network delete %+v", n)
	}
}

func TestNetworks(t *testing.T) {
	c, calls := server(t, [2]string{"200", `{"network":{"id":"nid","name":"mesh","revision":2,"segments":["a"]},"spec":{"name":"mesh","segments":{}},"toml":"x","connections":[],"truncated":false,"can_manage":true}`})
	n, err := c.GetNetwork(context.Background(), "p", "e", "mesh")
	if err != nil || n.ID != "nid" || n.Name != "mesh" || n.Revision != 2 || string(n.Spec) != `{"name":"mesh","segments":{}}` {
		t.Fatalf("%+v %v", n, err)
	}
	if got := (*calls)[0]; got.Path != "/api/v1/virtual-networks/mesh" || got.Query != "environment=e&project=p" {
		t.Fatalf("%+v", got)
	}

	c, calls = server(t, [2]string{"200", `{"id":"nid","name":"mesh","revision":1}`})
	spec := json.RawMessage(`{"name":"mesh","segments":{}}`)
	n, err = c.PutNetwork(context.Background(), "p", "e", NetworkPlan{Spec: spec})
	if err != nil || n.Revision != 1 || string(n.Spec) != string(spec) {
		t.Fatalf("%+v %v", n, err)
	}
	create := (*calls)[0]
	if create.Method != "POST" || create.Path != "/api/v1/virtual-networks" || create.Body["expected_revision"] != float64(0) || keys(create.Body)["expected_id"] {
		t.Fatalf("create %+v", create)
	}
	if _, err = c.PutNetwork(context.Background(), "p", "e", NetworkPlan{Spec: spec, ExpectedID: "nid", ExpectedRevision: 1}); err != nil {
		t.Fatal(err)
	}
	update := (*calls)[1]
	if update.Method != "PUT" || update.Path != "/api/v1/virtual-networks/mesh" || update.Body["expected_revision"] != float64(1) || update.Body["expected_id"] != "nid" || update.Body["spec"] == nil {
		t.Fatalf("update %+v", update)
	}
}

func TestNetworkPlanHelpers(t *testing.T) {
	p := NetworkPlan{Spec: json.RawMessage(`{"name":"mesh","segments":{"a":{}}}`)}
	if p.Name() != "mesh" || p.Unchanged() {
		t.Fatal("new network")
	}
	p.Previous = json.RawMessage(`null`)
	if p.Unchanged() {
		t.Fatal("null previous")
	}
	p.Previous = json.RawMessage(` {"segments": {"a": {}}, "name": "mesh"}`)
	if !p.Unchanged() {
		t.Fatal("equal specs")
	}
	p.Previous = json.RawMessage(`{"name":"mesh","segments":{}}`)
	if p.Unchanged() {
		t.Fatal("changed specs")
	}
}

func TestRetryOnlyGET(t *testing.T) {
	unavailable := [2]string{"503", `{"error":{"code":"unavailable","message":"retry"}}`}
	c, calls := server(t, unavailable, unavailable, [2]string{"200", `{"id":"d1","status":"succeeded"}`})
	d, err := c.GetDeployment(context.Background(), "d1")
	if err != nil || d.Status != "succeeded" || len(*calls) != 3 {
		t.Fatalf("GET: %+v %v calls=%d", d, err, len(*calls))
	}
	c, calls = server(t, unavailable)
	if _, err := c.GetDeployment(context.Background(), "d1"); status(err) != 503 || len(*calls) != 3 {
		t.Fatalf("GET gives up after 3: %v calls=%d", err, len(*calls))
	}
	c, calls = server(t, unavailable)
	if _, err := c.Deploy(context.Background(), "p", "e", json.RawMessage(`{}`), 0, "k"); status(err) != 503 || len(*calls) != 1 {
		t.Fatalf("POST must not retry: %v calls=%d", err, len(*calls))
	}
}
