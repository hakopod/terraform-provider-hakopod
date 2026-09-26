package client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestSecretClientRequests(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.RequestURI()+" "+string(b))
		switch r.Method {
		case http.MethodGet:
			io.WriteString(w, `{"items":[{"name":"db","updated_at":"2026-01-01T00:00:00Z"}]}`)
		case http.MethodPut:
			io.WriteString(w, `{"name":"db","saved":true,"restart_required":true}`)
		default:
			io.WriteString(w, `{}`)
		}
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "k", "", "test")
	ctx := context.Background()
	names, err := c.SecretNames(ctx, "shop", "prod", "api")
	if err != nil || !reflect.DeepEqual(names, []string{"db"}) {
		t.Fatalf("names %v %v", names, err)
	}
	if err := c.CreateSecret(ctx, "shop", "prod", "api", "db", "", "hex"); err != nil {
		t.Fatal(err)
	}
	if restart, err := c.PutSecret(ctx, "shop", "prod", "api", "db", "v"); err != nil || !restart {
		t.Fatalf("put %v %v", restart, err)
	}
	if err := c.DeleteSecret(ctx, "shop", "prod", "api", "db"); err != nil {
		t.Fatal(err)
	}
	q := "?application=api&environment=prod&project=shop"
	want := []string{
		"GET /api/v1/secrets" + q + " ",
		"POST /api/v1/secrets/db" + q + ` {"format":"hex","generate":true}`,
		"PUT /api/v1/secrets/db" + q + ` {"value":"v"}`,
		"DELETE /api/v1/secrets/db" + q + " ",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("requests\n got %q\nwant %q", got, want)
	}
}
