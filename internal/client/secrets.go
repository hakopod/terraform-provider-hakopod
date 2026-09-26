package client

import (
	"context"
	"net/http"
	"net/url"
)

// Application secrets (tree-deploy internal/api/workload_secrets.go). Values are
// write-only: the server never returns them.

func secretPath(project, environment, application, name string) string {
	q := url.Values{"project": {project}, "environment": {environment}, "application": {application}}
	p := "/secrets"
	if name != "" {
		p += "/" + url.PathEscape(name)
	}
	return p + "?" + q.Encode()
}

// SecretNames lists the application's secret names (GET /secrets → {items:[{name,updated_at}]}).
func (c *Client) SecretNames(ctx context.Context, project, environment, application string) ([]string, error) {
	var out struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := c.do(ctx, http.MethodGet, secretPath(project, environment, application, ""), nil, "", &out); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(out.Items))
	for _, i := range out.Items {
		names = append(names, i.Name)
	}
	return names, nil
}

// CreateSecret stores value, or asks the server to generate one when value is empty
// (format "" means base64url). 409 secret_exists if it already exists.
func (c *Client) CreateSecret(ctx context.Context, project, environment, application, name, value, format string) error {
	body := map[string]any{"value": value}
	if value == "" {
		body = map[string]any{"generate": true}
		if format != "" {
			body["format"] = format
		}
	}
	return c.do(ctx, http.MethodPost, secretPath(project, environment, application, name), body, "", nil)
}

// PutSecret replaces the value and reports whether the application must restart to use it.
func (c *Client) PutSecret(ctx context.Context, project, environment, application, name, value string) (restartRequired bool, err error) {
	var out struct {
		RestartRequired bool `json:"restart_required"`
	}
	err = c.do(ctx, http.MethodPut, secretPath(project, environment, application, name), map[string]any{"value": value}, "", &out)
	return out.RestartRequired, err
}

// DeleteSecret removes the secret; 409 while a runner pool still needs it.
func (c *Client) DeleteSecret(ctx context.Context, project, environment, application, name string) error {
	return c.do(ctx, http.MethodDelete, secretPath(project, environment, application, name), nil, "", nil)
}
