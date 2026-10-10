package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func accessFixture(profile string, revision int64) DatabaseAccess {
	d := DatabaseAccess{ID: "db1", Revision: revision, Status: "ready", Spec: map[string]json.RawMessage{
		"engine": json.RawMessage(`"clickhouse"`), "mode": json.RawMessage(`"standalone"`),
		"name": json.RawMessage(`"analytics"`), "cpu": json.RawMessage(`"2"`),
		"storage_gib": json.RawMessage(`40`), "future_integer": json.RawMessage(`9007199254740993`),
		"clickhouse": json.RawMessage(fmt.Sprintf(`{"access_profile":%q,"future_option":{"enabled":true}}`, profile)),
	}}
	d.Observation.Status = "ready"
	d.Observation.Revision = revision
	d.Observation.ObservedAt = time.Now().Add(-time.Second)
	return d
}

type accessScenario struct {
	before          DatabaseAccess
	reviewChange    func(*accessReview)
	preflightChange func(*DatabaseAccess)
	operationChange func(*accessOperation)
	finalChange     func(*DatabaseAccess)
	submitStatus    int
	pollStatus      string
	writes          []string
	gets            int
	polls           int
}

func accessServer(t *testing.T, s *accessScenario) *Client {
	t.Helper()
	if s.before.ID == "" {
		s.before = accessFixture("application", 4)
	}
	proposed, err := tenantAdminSpec(s.before.Spec)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer test" {
			t.Error("missing authorization")
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/databases/db1":
			s.gets++
			d := s.before
			if s.gets == 2 && s.preflightChange != nil {
				s.preflightChange(&d)
			}
			if s.gets >= 3 {
				d = accessFixture("tenant_admin", 5)
				if s.finalChange != nil {
					s.finalChange(&d)
				}
			}
			json.NewEncoder(w).Encode(d)
		case "POST /api/v1/databases/db1/resize-plan":
			s.writes = append(s.writes, "review")
			var in struct {
				Spec map[string]json.RawMessage `json:"spec"`
			}
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			if !sameAccessSpec(in.Spec, proposed) {
				t.Error("review changed an unrelated spec field")
			}
			review := accessReview{ID: "review1"}
			review.Plan.Current = s.before.Spec
			review.Plan.Proposed = proposed
			review.Plan.ExpectedRevision = 4
			review.Plan.BlockedReasons = []string{}
			review.Plan.ExpiresAt = time.Now().Add(5 * time.Minute)
			if s.reviewChange != nil {
				s.reviewChange(&review)
			}
			json.NewEncoder(w).Encode(review)
		case "POST /api/v1/databases/db1/resize":
			s.writes = append(s.writes, "activate")
			var in struct {
				ReviewID         string                     `json:"review_id"`
				ExpectedRevision int64                      `json:"expected_revision"`
				Spec             map[string]json.RawMessage `json:"spec"`
			}
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			if in.ReviewID != "review1" || in.ExpectedRevision != 4 || !sameAccessSpec(in.Spec, proposed) {
				t.Error("activation differs from its review")
			}
			if !strings.HasPrefix(r.Header.Get("Idempotency-Key"), "terraform-access-") {
				t.Error("missing idempotency key")
			}
			if s.submitStatus != 0 {
				w.WriteHeader(s.submitStatus)
				w.Write([]byte(`{"error":{"code":"unavailable","message":"not confirmed"}}`))
				return
			}
			op := accessOperation{ID: "op1", DatabaseID: "db1", Revision: 5, Kind: "resize", Status: "queued", Spec: proposed}
			if s.operationChange != nil {
				s.operationChange(&op)
			}
			w.WriteHeader(202)
			json.NewEncoder(w).Encode(op)
		case "GET /api/v1/database-operations/op1":
			s.polls++
			status := s.pollStatus
			if status == "" {
				status = "succeeded"
			}
			json.NewEncoder(w).Encode(accessOperation{ID: "op1", DatabaseID: "db1", Revision: 5, Kind: "resize", Status: status, Spec: proposed})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", 400)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "test", "", "test")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAccessActivationPreservesSpecAndVerifiesRuntime(t *testing.T) {
	s := &accessScenario{}
	c := accessServer(t, s)
	d, err := c.activateClickHouseTenantAdmin(context.Background(), "db1", time.Second, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if d.Revision != 5 || s.polls != 1 || s.gets != 3 || strings.Join(s.writes, ",") != "review,activate" {
		t.Fatalf("unexpected transition: %+v %+v", d, s)
	}
	if string(d.Spec["future_integer"]) != "9007199254740993" {
		t.Fatal("integer lost precision")
	}
}

func TestAccessActivationRejectsUnreviewedChanges(t *testing.T) {
	cases := map[string]func(*accessScenario){
		"blocked": func(s *accessScenario) {
			s.reviewChange = func(r *accessReview) { r.Plan.BlockedReasons = []string{"unhealthy"} }
		},
		"missing blockers": func(s *accessScenario) { s.reviewChange = func(r *accessReview) { r.Plan.BlockedReasons = nil } },
		"expired": func(s *accessScenario) {
			s.reviewChange = func(r *accessReview) { r.Plan.ExpiresAt = time.Now().Add(-time.Second) }
		},
		"stale review": func(s *accessScenario) { s.reviewChange = func(r *accessReview) { r.Plan.ExpectedRevision++ } },
		"capacity": func(s *accessScenario) {
			s.reviewChange = func(r *accessReview) { r.Plan.Proposed["cpu"] = json.RawMessage(`"4"`) }
		},
		"stale preflight": func(s *accessScenario) { s.preflightChange = func(d *DatabaseAccess) { d.Revision++ } },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			s := &accessScenario{}
			change(s)
			c := accessServer(t, s)
			if _, err := c.activateClickHouseTenantAdmin(context.Background(), "db1", time.Second, time.Millisecond); err == nil {
				t.Fatal("expected refusal")
			}
			if strings.Join(s.writes, ",") != "review" {
				t.Fatalf("unexpected mutation: %v", s.writes)
			}
		})
	}
}

func TestAccessActivationStopsAfterAmbiguousSubmission(t *testing.T) {
	s := &accessScenario{submitStatus: 503}
	c := accessServer(t, s)
	_, err := c.activateClickHouseTenantAdmin(context.Background(), "db1", time.Second, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "may have been accepted") {
		t.Fatalf("missing ambiguity: %v", err)
	}
	if strings.Join(s.writes, ",") != "review,activate" || s.polls != 0 {
		t.Fatalf("submission was retried: %+v", s)
	}
}

func TestAccessActivationRejectsUnexpectedOperation(t *testing.T) {
	for _, field := range []string{"id", "database", "revision", "kind", "spec"} {
		t.Run(field, func(t *testing.T) {
			s := &accessScenario{operationChange: func(op *accessOperation) {
				switch field {
				case "id":
					op.ID = ""
				case "database":
					op.DatabaseID = "other"
				case "revision":
					op.Revision++
				case "kind":
					op.Kind = "delete"
				case "spec":
					op.Spec = nil
				}
			}}
			c := accessServer(t, s)
			if _, err := c.activateClickHouseTenantAdmin(context.Background(), "db1", time.Second, time.Millisecond); err == nil {
				t.Fatal("accepted unexpected operation")
			}
			if s.polls != 0 {
				t.Fatal("polled untrusted operation")
			}
		})
	}
}

func TestAccessActivationRequiresSuccessAndObservedRevision(t *testing.T) {
	for _, status := range []string{"failed", "cancelled", "unknown"} {
		t.Run(status, func(t *testing.T) {
			s := &accessScenario{pollStatus: status}
			c := accessServer(t, s)
			if _, err := c.activateClickHouseTenantAdmin(context.Background(), "db1", time.Second, time.Millisecond); err == nil {
				t.Fatal("accepted incomplete operation")
			}
		})
	}
	for _, state := range []string{"pending", "stale observation", "new revision"} {
		t.Run(state, func(t *testing.T) {
			s := &accessScenario{finalChange: func(d *DatabaseAccess) {
				switch state {
				case "pending":
					d.Status = "updating"
				case "stale observation":
					d.Observation.Revision--
				case "new revision":
					d.Revision++
				}
			}}
			c := accessServer(t, s)
			if _, err := c.activateClickHouseTenantAdmin(context.Background(), "db1", 250*time.Millisecond, time.Millisecond); err == nil {
				t.Fatal("accepted unverified runtime")
			}
		})
	}
}

func TestAccessActivationBoundedAndCancelled(t *testing.T) {
	s := &accessScenario{pollStatus: "running"}
	c := accessServer(t, s)
	start := time.Now()
	_, err := c.activateClickHouseTenantAdmin(context.Background(), "db1", 250*time.Millisecond, time.Millisecond)
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("polling was not bounded: %v", err)
	}
	if strings.Join(s.writes, ",") != "review,activate" {
		t.Fatalf("unexpected retry: %v", s.writes)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.activateClickHouseTenantAdmin(ctx, "db1", time.Second, time.Millisecond); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestAccessAlreadyEnabledDoesNotWrite(t *testing.T) {
	s := &accessScenario{before: accessFixture("tenant_admin", 4)}
	c := accessServer(t, s)
	if _, err := c.activateClickHouseTenantAdmin(context.Background(), "db1", time.Second, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if len(s.writes) != 0 {
		t.Fatal("existing profile caused a write")
	}
	s.before.Observation.ObservedAt = time.Now().Add(-time.Minute)
	if _, err := c.activateClickHouseTenantAdmin(context.Background(), "db1", time.Second, time.Millisecond); err == nil {
		t.Fatal("accepted stale health")
	}
}

func TestAccessRejectsOtherDatabaseKinds(t *testing.T) {
	for name, value := range map[string]string{"engine": `"postgresql"`, "mode": `"replicated"`} {
		t.Run(name, func(t *testing.T) {
			d := accessFixture("application", 4)
			d.Spec[name] = json.RawMessage(value)
			s := &accessScenario{before: d}
			c := accessServer(t, s)
			if _, err := c.activateClickHouseTenantAdmin(context.Background(), "db1", time.Second, time.Millisecond); err == nil {
				t.Fatal("accepted unsupported database")
			}
			if len(s.writes) != 0 {
				t.Fatal("wrote to unsupported database")
			}
		})
	}
}

func TestAccessMissingProfileAndComparisonPrecision(t *testing.T) {
	d := accessFixture("application", 4)
	delete(d.Spec, "clickhouse")
	if profile, err := d.Profile(); err != nil || profile != "application" {
		t.Fatalf("default profile: %s %v", profile, err)
	}
	proposed, err := tenantAdminSpec(d.Spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := d.Spec["clickhouse"]; exists {
		t.Fatal("mutated source spec")
	}
	if sameAccessSpec(d.Spec, proposed) {
		t.Fatal("ignored profile difference")
	}
	changed := make(map[string]json.RawMessage)
	for k, v := range proposed {
		changed[k] = v
	}
	changed["future_integer"] = json.RawMessage(`9007199254740992`)
	if sameAccessSpec(proposed, changed) {
		t.Fatal("large integer difference was lost")
	}
}
