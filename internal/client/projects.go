package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// Project is one GET /projects item (internal/api/api.go:317) with environments flattened.
type Project struct {
	ID, Name, DisplayName, Description string
	MetadataRevision                   int64
	Environments                       []string
}

// CreateProject creates a project with its first environment (internal/api/api.go:374).
// Empty displayName/description are omitted. Note: with neither set, the server
// adds the environment to an existing project instead of returning 409, so
// callers must check existence first.
func (c *Client) CreateProject(ctx context.Context, name, environment, displayName, description string) (Project, error) {
	// Always send display_name: without a display field the server silently adds the
	// environment to an existing project instead of answering 409 project_exists.
	if displayName == "" {
		displayName = name
	}
	body := map[string]any{"name": name, "environment": environment, "display_name": displayName}
	if description != "" {
		body["description"] = description
	}
	var out struct {
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
		Description string `json:"description"`
	}
	if err := c.do(ctx, http.MethodPost, "/projects", body, "", &out); err != nil {
		return Project{}, err
	}
	return Project{ID: out.Name, Name: out.Name, DisplayName: out.DisplayName, Description: out.Description, Environments: []string{environment}}, nil
}

// CreateEnvironment adds an environment (internal/api/environments.go); 409 environment_exists if present.
func (c *Client) CreateEnvironment(ctx context.Context, project, name string) error {
	return c.do(ctx, http.MethodPost, "/projects/"+url.PathEscape(project)+"/environments", map[string]any{"name": name}, "", nil)
}

// projectListingCap is the server's row limit for GET /projects (one row per environment).
const projectListingCap = 200

// ErrProjectListingTruncated means the listing was full, so absence proves nothing.
var ErrProjectListingTruncated = errors.New("the project listing reached its 200-environment limit; the project may exist beyond it")

// GetProject finds a project in GET /projects; a 404 APIError if it is absent or not visible.
// ponytail: the server caps the listing at 200 environment rows and has no single-project GET.
func (c *Client) GetProject(ctx context.Context, name string) (Project, error) {
	var out struct {
		Items []struct {
			ID               string `json:"id"`
			Name             string `json:"name"`
			DisplayName      string `json:"display_name"`
			Description      string `json:"description"`
			MetadataRevision int64  `json:"metadata_revision"`
			Environments     []struct {
				Name string `json:"name"`
			} `json:"environments"`
		} `json:"items"`
	}
	if err := c.do(ctx, http.MethodGet, "/projects", nil, "", &out); err != nil {
		return Project{}, err
	}
	rows := 0
	for _, p := range out.Items {
		rows += len(p.Environments)
		if p.Name != name {
			continue
		}
		r := Project{ID: p.ID, Name: p.Name, DisplayName: p.DisplayName, Description: p.Description, MetadataRevision: p.MetadataRevision, Environments: []string{}}
		for _, e := range p.Environments {
			r.Environments = append(r.Environments, e.Name)
		}
		return r, nil
	}
	if rows >= projectListingCap {
		return Project{}, ErrProjectListingTruncated
	}
	return Project{}, &APIError{Status: http.StatusNotFound, Code: "not_found", Message: fmt.Sprintf("project %q not found", name)}
}

// RenameProject sets the display name (internal/api/display_names.go:24); 409 if the revision is stale.
// The server has no endpoint that changes a project's description.
func (c *Client) RenameProject(ctx context.Context, id, displayName string, metadataRevision int64) error {
	return c.do(ctx, http.MethodPut, "/projects/"+url.PathEscape(id)+"/name", map[string]any{
		"display_name": displayName, "expected_metadata_revision": metadataRevision,
	}, "", nil)
}

// DeleteProject deletes an empty project (internal/api/deletion.go:16); 409 if it still has resources.
func (c *Client) DeleteProject(ctx context.Context, id, name string) error {
	return c.do(ctx, http.MethodDelete, "/projects/"+url.PathEscape(id), map[string]any{"confirm_name": name}, "", nil)
}
