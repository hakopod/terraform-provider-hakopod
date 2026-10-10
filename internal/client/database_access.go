package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"time"
)

// DatabaseAccess retains every spec field, including fields unknown to this provider.
// It never requests database credentials.
type DatabaseAccess struct {
	ID          string                     `json:"id"`
	Revision    int64                      `json:"revision"`
	Spec        map[string]json.RawMessage `json:"spec"`
	Status      string                     `json:"status"`
	Observation struct {
		Status     string    `json:"status"`
		Revision   int64     `json:"revision"`
		ObservedAt time.Time `json:"observed_at"`
	} `json:"observation"`
}

func (d DatabaseAccess) Profile() (string, error) {
	var engine, mode string
	if json.Unmarshal(d.Spec["engine"], &engine) != nil || json.Unmarshal(d.Spec["mode"], &mode) != nil || engine != "clickhouse" || mode != "standalone" {
		return "", fmt.Errorf("the database must use standalone ClickHouse")
	}
	raw, ok := d.Spec["clickhouse"]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "application", nil
	}
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return "", fmt.Errorf("the ClickHouse configuration is invalid")
	}
	var profile string
	if err := json.Unmarshal(cfg["access_profile"], &profile); err != nil {
		return "", fmt.Errorf("the ClickHouse access profile is missing")
	}
	if profile != "application" && profile != "tenant_admin" {
		return "", fmt.Errorf("the ClickHouse access profile is unsupported")
	}
	return profile, nil
}

func (d DatabaseAccess) ready(now time.Time) bool {
	return d.Status == "ready" && d.Observation.Status == "ready" && d.Observation.Revision == d.Revision && !d.Observation.ObservedAt.IsZero() && !d.Observation.ObservedAt.After(now) && now.Sub(d.Observation.ObservedAt) <= 30*time.Second
}

func (c *Client) GetDatabaseAccess(ctx context.Context, id string) (DatabaseAccess, error) {
	var d DatabaseAccess
	if id == "" {
		return d, fmt.Errorf("the database ID is required")
	}
	if err := c.do(ctx, http.MethodGet, "/databases/"+url.PathEscape(id), nil, "", &d); err != nil {
		return d, err
	}
	if d.ID != id || d.Revision < 1 || d.Spec == nil {
		return d, fmt.Errorf("the database response has an invalid identity, revision or spec")
	}
	if _, err := d.Profile(); err != nil {
		return d, err
	}
	return d, nil
}

type accessReview struct {
	ID   string `json:"id"`
	Plan struct {
		Current          map[string]json.RawMessage `json:"current"`
		Proposed         map[string]json.RawMessage `json:"proposed"`
		ExpectedRevision int64                      `json:"expected_revision"`
		BlockedReasons   []string                   `json:"blocked_reasons"`
		ExpiresAt        time.Time                  `json:"expires_at"`
	} `json:"plan"`
}

type accessOperation struct {
	ID         string                     `json:"id"`
	DatabaseID string                     `json:"database_id"`
	Revision   int64                      `json:"revision"`
	Kind       string                     `json:"kind"`
	Status     string                     `json:"status"`
	Spec       map[string]json.RawMessage `json:"spec"`
}

// ActivateClickHouseTenantAdmin changes only the access profile. It sends each POST once.
// A timeout after submission requires inspection. The caller must not retry blindly.
func (c *Client) ActivateClickHouseTenantAdmin(ctx context.Context, id string) (DatabaseAccess, error) {
	return c.activateClickHouseTenantAdmin(ctx, id, 10*time.Minute, 2*time.Second)
}

func (c *Client) activateClickHouseTenantAdmin(ctx context.Context, id string, timeout, interval time.Duration) (DatabaseAccess, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	d, err := c.GetDatabaseAccess(ctx, id)
	if err != nil {
		return d, err
	}
	profile, _ := d.Profile()
	if profile == "tenant_admin" {
		if !d.ready(time.Now()) {
			return d, fmt.Errorf("tenant administration is configured, but the current database revision is not confirmed healthy")
		}
		return d, nil
	}
	proposed, err := tenantAdminSpec(d.Spec)
	if err != nil {
		return d, err
	}
	var review accessReview
	if err := c.do(ctx, http.MethodPost, "/databases/"+url.PathEscape(id)+"/resize-plan", map[string]any{"spec": proposed}, "", &review); err != nil {
		return d, fmt.Errorf("the access review failed: %w", err)
	}
	// The review endpoint observes health. Do not require the preceding GET's observation to be fresh.
	if review.ID == "" || review.Plan.ExpectedRevision != d.Revision || !sameAccessSpec(review.Plan.Current, d.Spec) || !sameAccessSpec(review.Plan.Proposed, proposed) {
		return d, fmt.Errorf("the access review does not match the database revision and proposed spec. create a new Terraform plan")
	}
	if review.Plan.BlockedReasons == nil || len(review.Plan.BlockedReasons) != 0 {
		return d, fmt.Errorf("the access review is blocked or incomplete. inspect the managed database before retrying")
	}
	if review.Plan.ExpiresAt.IsZero() || !review.Plan.ExpiresAt.After(time.Now()) {
		return d, fmt.Errorf("the access review expired. create a new Terraform plan")
	}
	// Check for intervening changes without changing the approved request.
	current, err := c.GetDatabaseAccess(ctx, id)
	if err != nil {
		return d, err
	}
	if current.Revision != d.Revision || !sameAccessSpec(current.Spec, d.Spec) {
		return d, fmt.Errorf("the database changed after the access review. create a new Terraform plan")
	}
	if !review.Plan.ExpiresAt.After(time.Now()) {
		return d, fmt.Errorf("the access review expired before activation. create a new Terraform plan")
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return d, fmt.Errorf("cannot create an operation idempotency key: %w", err)
	}
	var op accessOperation
	err = c.do(ctx, http.MethodPost, "/databases/"+url.PathEscape(id)+"/resize", map[string]any{
		"review_id": review.ID, "expected_revision": d.Revision, "spec": proposed,
	}, "terraform-access-"+hex.EncodeToString(random[:]), &op)
	if err != nil {
		return d, fmt.Errorf("activation was not confirmed and may have been accepted. inspect database operations before retrying: %w", err)
	}
	if op.ID == "" || op.DatabaseID != id || op.Revision != d.Revision+1 || op.Kind != "resize" || !sameAccessSpec(op.Spec, proposed) {
		return d, fmt.Errorf("the activation response does not match the requested transition. inspect database operations before retrying")
	}
	operationID, revision := op.ID, op.Revision
	for {
		if op.ID != operationID || op.DatabaseID != id || op.Revision != revision || op.Kind != "resize" || !sameAccessSpec(op.Spec, proposed) {
			return d, fmt.Errorf("operation %s changed identity or spec. inspect the operation before retrying", operationID)
		}
		switch op.Status {
		case "succeeded":
			// A successful operation alone does not prove the observed revision is healthy.
			final, err := c.GetDatabaseAccess(ctx, id)
			if err != nil {
				return d, fmt.Errorf("operation %s completed, but database verification failed: %w", operationID, err)
			}
			if final.Revision != revision || !sameAccessSpec(final.Spec, proposed) {
				return d, fmt.Errorf("the database changed after operation %s. inspect the database before retrying", operationID)
			}
			if final.ready(time.Now()) {
				return final, nil
			}
		case "queued", "running":
		case "failed", "cancelled":
			return d, fmt.Errorf("operation %s did not succeed. inspect the operation before retrying", operationID)
		default:
			return d, fmt.Errorf("operation %s returned an unsupported status. inspect the operation before retrying", operationID)
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return d, fmt.Errorf("operation %s remains unconfirmed. inspect the operation before retrying: %w", operationID, ctx.Err())
		case <-timer.C:
		}
		var next accessOperation
		if err := c.do(ctx, http.MethodGet, "/database-operations/"+url.PathEscape(operationID), nil, "", &next); err != nil {
			return d, fmt.Errorf("operation %s remains unconfirmed. inspect the operation before retrying: %w", operationID, err)
		}
		op = next
	}
}

func tenantAdminSpec(current map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	proposed := make(map[string]json.RawMessage, len(current)+1)
	for k, v := range current {
		proposed[k] = append(json.RawMessage(nil), v...)
	}
	cfg := map[string]json.RawMessage{}
	if raw, ok := proposed["clickhouse"]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("the ClickHouse configuration is invalid")
		}
	}
	cfg["access_profile"] = json.RawMessage(`"tenant_admin"`)
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	proposed["clickhouse"] = raw
	return proposed, nil
}

// UseNumber prevents precision loss when future spec fields contain large integers.
func sameAccessSpec(a, b map[string]json.RawMessage) bool {
	decode := func(v map[string]json.RawMessage) (any, error) {
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var out any
		err = dec.Decode(&out)
		return out, err
	}
	av, ae := decode(a)
	bv, be := decode(b)
	return ae == nil && be == nil && reflect.DeepEqual(av, bv)
}
