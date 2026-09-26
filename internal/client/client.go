// Package client is a small HTTP client for the Hakopod management API.
// Request and response shapes mirror the server (tree-deploy internal/api) and
// the hakopod CLI (cmd/hakopod), which cannot be imported here.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxResponse = 8 << 20

type Client struct {
	base, key, workspace, userAgent string
	http                            *http.Client
}

// New validates baseURL (https, or http for loopback hosts only) and returns a client.
func New(baseURL, apiKey, workspace, userAgent string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid Hakopod API URL %q", baseURL)
	}
	switch u.Scheme {
	case "https":
	case "http":
		host := u.Hostname()
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return nil, fmt.Errorf("Hakopod API URL %q must use https (http is allowed only for loopback hosts)", baseURL)
		}
	default:
		return nil, fmt.Errorf("Hakopod API URL %q must use https", baseURL)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("Hakopod API URL %q must not contain a query or fragment", baseURL)
	}
	return &Client{
		base: u.String(), key: apiKey, workspace: workspace, userAgent: userAgent,
		// Redirects are refused like the CLI (cmd/hakopod/main.go:534) so the bearer key never follows one.
		http: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("API redirects are not allowed")
		}},
	}, nil
}

// APIError is a non-2xx response; Code and Message come from the server's
// {"error":{"code","message"}} body (internal/api/api.go:180).
type APIError struct {
	Status        int
	Code, Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s: %s (HTTP %d)", e.Code, e.Message, e.Status)
}

func status(err error) int {
	var e *APIError
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

func IsNotFound(err error) bool { return status(err) == http.StatusNotFound }
func IsConflict(err error) bool { return status(err) == http.StatusConflict }

// do sends one JSON request under /api/v1. Only GETs are retried (transport
// errors, 502/503/504; 3 tries); writes never are.
func (c *Client) do(ctx context.Context, method, path string, in any, idem string, out any) error {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return err
		}
	}
	tries := 1
	if method == http.MethodGet {
		tries = 3
	}
	var lastErr error
	for attempt := 0; attempt < tries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 250 * time.Millisecond):
			}
		}
		var retry bool
		retry, lastErr = c.once(ctx, method, path, body, idem, out)
		if !retry {
			return lastErr
		}
	}
	return lastErr
}

func (c *Client) once(ctx context.Context, method, path string, body []byte, idem string, out any) (bool, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+"/api/v1"+path, r)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	if c.workspace != "" {
		req.Header.Set("X-Hakopod-Workspace", c.workspace)
	}
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem) // cmd/hakopod/main.go:552, read by internal/api/api.go:711
	}
	res, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return true, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponse+1))
	if err != nil {
		return true, fmt.Errorf("%s %s: reading response: %w", method, path, err)
	}
	if len(data) > maxResponse {
		return false, fmt.Errorf("%s %s: response exceeds %d bytes", method, path, maxResponse)
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		e := &APIError{Status: res.StatusCode}
		var p struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		if json.Unmarshal(data, &p) == nil && p.Error.Code != "" {
			e.Code, e.Message = p.Error.Code, p.Error.Message
		} else {
			e.Code, e.Message = "http_error", http.StatusText(res.StatusCode)
		}
		switch res.StatusCode {
		case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true, e
		}
		return false, e
	}
	if out == nil {
		return false, nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return false, fmt.Errorf("%s %s: decoding response: %w", method, path, err)
	}
	return false, nil
}

// ---- applications and deployments ----

type PlanInput struct {
	Project, Environment string
	TOML                 string
	Spec                 json.RawMessage
	EnvFiles             map[string]string
}

// AppPlan is the POST /plan response (internal/api/api.go:696).
type AppPlan struct {
	ApplicationID    string            `json:"application_id"`
	ExpectedRevision int64             `json:"expected_revision"`
	Spec             json.RawMessage   `json:"spec"`
	Changes          []json.RawMessage `json:"changes"`
	Warnings         []string          `json:"warnings"`
	MissingSecrets   []string          `json:"missing_secrets"`
}

// PlanApplication previews a revision. The server rejects both/neither of
// toml and spec, and env_files with spec (internal/api/api.go:538-548).
func (c *Client) PlanApplication(ctx context.Context, in PlanInput) (AppPlan, error) {
	body := map[string]any{"project": in.Project, "environment": in.Environment}
	hasSpec := len(in.Spec) > 0 && string(in.Spec) != "null"
	switch {
	case hasSpec == (in.TOML != ""):
		return AppPlan{}, errors.New("provide exactly one of toml or spec")
	case hasSpec && len(in.EnvFiles) > 0:
		return AppPlan{}, errors.New("env_files require toml")
	case hasSpec:
		body["spec"] = in.Spec
	default:
		body["toml"] = in.TOML
		if len(in.EnvFiles) > 0 {
			body["env_files"] = in.EnvFiles
		}
	}
	var p AppPlan
	err := c.do(ctx, http.MethodPost, "/plan", body, "", &p)
	return p, err
}

// Deployment holds the fields of store.Deployment (internal/store/store.go:445) the provider uses.
type Deployment struct {
	ID            string `json:"id"`
	ApplicationID string `json:"application_id"`
	Status        string `json:"status"`
	Error         string `json:"error"`
	Revision      int64  `json:"revision"`
}

// Deploy submits the reviewed spec at the reviewed revision, like the CLI's
// submitDeployment (cmd/hakopod/tree_deploy.go:40).
func (c *Client) Deploy(ctx context.Context, project, environment string, spec json.RawMessage, expectedRevision int64, idempotencyKey string) (Deployment, error) {
	var d Deployment
	err := c.do(ctx, http.MethodPost, "/deployments", map[string]any{
		"project": project, "environment": environment, "spec": spec, "expected_revision": expectedRevision,
	}, idempotencyKey, &d)
	return d, err
}

func (c *Client) GetDeployment(ctx context.Context, id string) (Deployment, error) {
	var d Deployment
	err := c.do(ctx, http.MethodGet, "/deployments/"+url.PathEscape(id), nil, "", &d)
	return d, err
}

// WaitDeployment polls until the deployment leaves queued/running — the only
// non-terminal states (cmd/hakopod/main.go:589, internal/store/actions.go:159);
// terminal ones are succeeded, failed, cancelled and superseded.
func (c *Client) WaitDeployment(ctx context.Context, id string, poll time.Duration) (Deployment, error) {
	if poll <= 0 {
		poll = 2 * time.Second
	}
	for {
		d, err := c.GetDeployment(ctx, id)
		if err != nil {
			return d, err
		}
		if d.Status != "queued" && d.Status != "running" {
			return d, nil
		}
		select {
		case <-ctx.Done():
			return d, fmt.Errorf("stopped waiting for deployment %s (%s); it continues on the server: %w", id, d.Status, ctx.Err())
		case <-time.After(poll):
		}
	}
}

// Application holds the fields of store.Application (internal/store/store.go:413) the provider uses.
type Application struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Project     string          `json:"project"`
	Environment string          `json:"environment"`
	Status      string          `json:"status"`
	Revision    int64           `json:"revision"`
	Spec        json.RawMessage `json:"spec"`
	// Recent deployments, newest first (store.DeploymentSummary in the server).
	Deployments []struct {
		Status string `json:"status"`
	} `json:"deployments"`
}

// NeverSucceeded reports whether no release of the application has ever succeeded:
// "recovered" means an earlier release was restored, so it counts as a success.
func (a Application) NeverSucceeded() bool {
	if a.Status == "healthy" || a.Status == "recovered" || len(a.Deployments) >= 30 {
		// The server returns at most 30 recent deployments; a full list may hide an old success.
		return false
	}
	for _, d := range a.Deployments {
		if d.Status == "succeeded" {
			return false
		}
	}
	return true
}

func (c *Client) GetApplication(ctx context.Context, id string) (Application, error) {
	var a Application
	err := c.do(ctx, http.MethodGet, "/applications/"+url.PathEscape(id), nil, "", &a)
	return a, err
}

// FindApplication pages GET /applications ({items, next_cursor}, internal/api/api.go:471)
// and matches by name.
func (c *Client) FindApplication(ctx context.Context, project, environment, name string) (Application, error) {
	cursor := ""
	for {
		var page struct {
			Items      []Application `json:"items"`
			NextCursor string        `json:"next_cursor"`
		}
		q := url.Values{"project": {project}, "environment": {environment}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		if err := c.do(ctx, http.MethodGet, "/applications?"+q.Encode(), nil, "", &page); err != nil {
			return Application{}, err
		}
		for _, a := range page.Items {
			if a.Name == name {
				return a, nil
			}
		}
		if page.NextCursor == "" || page.NextCursor == cursor {
			return Application{}, &APIError{Status: http.StatusNotFound, Code: "not_found", Message: fmt.Sprintf("application %q not found in %s/%s", name, project, environment)}
		}
		cursor = page.NextCursor
	}
}

// DeleteApplication body per internal/api/deletion.go:35-39.
func (c *Client) DeleteApplication(ctx context.Context, id, confirmName string, expectedRevision int64, deleteData bool) error {
	return c.do(ctx, http.MethodDelete, "/applications/"+url.PathEscape(id), map[string]any{
		"delete_data": deleteData, "confirm_name": confirmName, "expected_revision": expectedRevision,
	}, "", nil)
}

// ---- virtual networks ----

// NetworkPlan is the POST /virtual-networks/plan response (internal/api/virtual_networks.go:267).
type NetworkPlan struct {
	Spec             json.RawMessage `json:"spec"`
	Previous         json.RawMessage `json:"previous"`
	TOML             string          `json:"toml"`
	ExpectedID       string          `json:"expected_id"`
	ExpectedRevision int64           `json:"expected_revision"`
}

func (p NetworkPlan) Name() string {
	var s struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(p.Spec, &s)
	return s.Name
}

// Unchanged reports whether the plan's spec equals the stored one.
func (p NetworkPlan) Unchanged() bool {
	if len(p.Previous) == 0 || string(p.Previous) == "null" {
		return false
	}
	a, errA := canonical(p.Spec)
	b, errB := canonical(p.Previous)
	return errA == nil && errB == nil && bytes.Equal(a, b)
}

func canonical(raw json.RawMessage) ([]byte, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return json.Marshal(v) // map keys are sorted on marshal
}

func (c *Client) PlanNetwork(ctx context.Context, project, environment, toml string, spec json.RawMessage) (NetworkPlan, error) {
	body := map[string]any{"project": project, "environment": environment}
	hasSpec := len(spec) > 0 && string(spec) != "null"
	if hasSpec == (toml != "") {
		return NetworkPlan{}, errors.New("provide exactly one of toml or spec")
	}
	if hasSpec {
		body["spec"] = spec
	} else {
		body["toml"] = toml
	}
	var p NetworkPlan
	err := c.do(ctx, http.MethodPost, "/virtual-networks/plan", body, "", &p)
	return p, err
}

type Network struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Revision int64           `json:"revision"`
	Spec     json.RawMessage `json:"spec"`
}

// PutNetwork applies a reviewed plan. POST requires expected_revision 0 with no
// expected_id; PUT requires both (internal/api/virtual_networks.go:208-216,280).
func (c *Client) PutNetwork(ctx context.Context, project, environment string, p NetworkPlan) (Network, error) {
	body := map[string]any{"project": project, "environment": environment, "spec": p.Spec, "expected_revision": p.ExpectedRevision}
	method, path := http.MethodPost, "/virtual-networks"
	if p.ExpectedRevision != 0 {
		body["expected_id"] = p.ExpectedID
		method, path = http.MethodPut, "/virtual-networks/"+url.PathEscape(p.Name())
	}
	// The response is a summary without spec (virtual_networks.go:293-298), so the reviewed spec fills it.
	var n Network
	if err := c.do(ctx, method, path, body, "", &n); err != nil {
		return Network{}, err
	}
	n.Spec = p.Spec
	return n, nil
}

// GetNetwork maps {"network":{id,name,revision,...},"spec":{...},...}
// (internal/api/virtual_networks.go:196) onto Network.
func (c *Client) GetNetwork(ctx context.Context, project, environment, name string) (Network, error) {
	var out struct {
		Network struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Revision int64  `json:"revision"`
		} `json:"network"`
		Spec json.RawMessage `json:"spec"`
	}
	q := url.Values{"project": {project}, "environment": {environment}}
	if err := c.do(ctx, http.MethodGet, "/virtual-networks/"+url.PathEscape(name)+"?"+q.Encode(), nil, "", &out); err != nil {
		return Network{}, err
	}
	return Network{ID: out.Network.ID, Name: out.Network.Name, Revision: out.Network.Revision, Spec: out.Spec}, nil
}

// DeleteNetwork body per internal/api/virtual_networks.go:302-308.
func (c *Client) DeleteNetwork(ctx context.Context, project, environment, name, id string, revision int64) error {
	return c.do(ctx, http.MethodDelete, "/virtual-networks/"+url.PathEscape(name), map[string]any{
		"project": project, "environment": environment, "expected_id": id, "expected_revision": revision, "confirmation": name,
	}, "", nil)
}
