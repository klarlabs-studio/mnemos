package memory

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
)

func openTestConn(t *testing.T) *store.Conn {
	t.Helper()
	conn, err := store.Open(context.Background(), "memory://")
	if err != nil {
		t.Fatalf("open memory store: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestActionRepository_AppendListIdempotent(t *testing.T) {
	conn := openTestConn(t)
	ctx := context.Background()
	at := time.Date(2026, 4, 30, 9, 0, 0, 0, time.UTC)
	a := domain.Action{
		ID:      "ac_1",
		RunID:   "run_a",
		Kind:    domain.ActionKindDeploy,
		Subject: "payments",
		At:      at,
	}
	if err := conn.Actions.Append(ctx, a); err != nil {
		t.Fatalf("append action: %v", err)
	}
	// Idempotent re-append.
	if err := conn.Actions.Append(ctx, a); err != nil {
		t.Fatalf("re-append action: %v", err)
	}
	count, err := conn.Actions.CountAll(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("want count=1, got %d", count)
	}
	gotByRun, err := conn.Actions.ListByRunID(ctx, "run_a")
	if err != nil {
		t.Fatalf("list by run: %v", err)
	}
	if len(gotByRun) != 1 || gotByRun[0].ID != "ac_1" {
		t.Fatalf("list by run: want ac_1, got %#v", gotByRun)
	}
	gotBySubject, err := conn.Actions.ListBySubject(ctx, "payments")
	if err != nil {
		t.Fatalf("list by subject: %v", err)
	}
	if len(gotBySubject) != 1 {
		t.Fatalf("list by subject: want 1, got %d", len(gotBySubject))
	}
}

func TestOutcomeRepository_AppendListByAction(t *testing.T) {
	conn := openTestConn(t)
	ctx := context.Background()
	at := time.Date(2026, 4, 30, 9, 0, 0, 0, time.UTC)
	if err := conn.Actions.Append(ctx, domain.Action{
		ID: "ac_1", Kind: domain.ActionKindRollback, Subject: "payments", At: at,
	}); err != nil {
		t.Fatalf("seed action: %v", err)
	}
	out := domain.Outcome{
		ID:         "oc_1",
		ActionID:   "ac_1",
		Result:     domain.OutcomeResultSuccess,
		Metrics:    map[string]float64{"latency_after_ms": 240, "latency_before_ms": 1200},
		ObservedAt: at.Add(2 * time.Minute),
	}
	if err := conn.Outcomes.Append(ctx, out); err != nil {
		t.Fatalf("append outcome: %v", err)
	}
	got, err := conn.Outcomes.ListByActionID(ctx, "ac_1")
	if err != nil {
		t.Fatalf("list by action: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 outcome, got %d", len(got))
	}
	if got[0].Result != domain.OutcomeResultSuccess {
		t.Fatalf("result: want success, got %s", got[0].Result)
	}
	if got[0].Metrics["latency_after_ms"] != 240 {
		t.Fatalf("metric latency_after_ms: want 240, got %v", got[0].Metrics["latency_after_ms"])
	}
	if got[0].Source != "push" {
		t.Fatalf("source default: want push, got %q", got[0].Source)
	}
}

func TestClaimRepository_MarkVerified(t *testing.T) {
	conn := openTestConn(t)
	ctx := context.Background()
	createdAt := time.Date(2026, 4, 15, 9, 0, 0, 0, time.UTC)
	if err := conn.Claims.Upsert(ctx, []domain.Claim{{
		ID:         "cl_1",
		Text:       "deploy succeeded",
		Type:       domain.ClaimTypeFact,
		Confidence: 0.8,
		Status:     domain.ClaimStatusActive,
		CreatedAt:  createdAt,
		ValidFrom:  createdAt,
	}}); err != nil {
		t.Fatalf("seed claim: %v", err)
	}
	verifiedAt := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	if err := conn.Claims.MarkVerified(ctx, "cl_1", verifiedAt, 30); err != nil {
		t.Fatalf("mark verified: %v", err)
	}
	got, err := conn.Claims.ListByIDs(ctx, []string{"cl_1"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 claim, got %d", len(got))
	}
	if !got[0].LastVerified.Equal(verifiedAt.UTC()) {
		t.Fatalf("LastVerified: want %v, got %v", verifiedAt.UTC(), got[0].LastVerified)
	}
	if got[0].VerifyCount != 1 {
		t.Fatalf("VerifyCount: want 1, got %d", got[0].VerifyCount)
	}
	if got[0].HalfLifeDays != 30 {
		t.Fatalf("HalfLifeDays: want 30, got %v", got[0].HalfLifeDays)
	}

	// Idempotent re-verification preserves half_life override when caller passes 0.
	verifiedAt2 := verifiedAt.Add(time.Hour)
	if err := conn.Claims.MarkVerified(ctx, "cl_1", verifiedAt2, 0); err != nil {
		t.Fatalf("re-mark: %v", err)
	}
	got, _ = conn.Claims.ListByIDs(ctx, []string{"cl_1"})
	if got[0].VerifyCount != 2 {
		t.Fatalf("VerifyCount after re-mark: want 2, got %d", got[0].VerifyCount)
	}
	if got[0].HalfLifeDays != 30 {
		t.Fatalf("HalfLifeDays should not be reset by zero arg, got %v", got[0].HalfLifeDays)
	}
}

func TestDecisionRepository_AppendListAttachOutcome(t *testing.T) {
	conn := openTestConn(t)
	ctx := context.Background()
	chosen := time.Date(2026, 4, 30, 10, 0, 0, 0, time.UTC)

	// Seed beliefs as claims so FK-style listing works.
	if err := conn.Claims.Upsert(ctx, []domain.Claim{{
		ID: "cl_a", Text: "deploys often regress payments", Type: domain.ClaimTypeFact,
		Confidence: 0.7, Status: domain.ClaimStatusActive, CreatedAt: chosen, ValidFrom: chosen,
	}, {
		ID: "cl_b", Text: "rollback recovers latency in <5min", Type: domain.ClaimTypeFact,
		Confidence: 0.85, Status: domain.ClaimStatusActive, CreatedAt: chosen, ValidFrom: chosen,
	}}); err != nil {
		t.Fatalf("seed claims: %v", err)
	}

	d := domain.Decision{
		ID: "dc_1", Statement: "Roll back payments deploy", Plan: "rollback to v1.41",
		Reasoning: "high latency correlated with deploy", RiskLevel: domain.RiskLevelHigh,
		Beliefs: []string{"cl_a", "cl_b"}, Alternatives: []string{"hotfix", "wait-and-see"},
		ChosenAt: chosen,
	}
	if err := conn.Decisions.Append(ctx, d); err != nil {
		t.Fatalf("append decision: %v", err)
	}

	got, err := conn.Decisions.GetByID(ctx, "dc_1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Beliefs) != 2 {
		t.Fatalf("want 2 beliefs, got %d", len(got.Beliefs))
	}
	if len(got.Alternatives) != 2 {
		t.Fatalf("want 2 alternatives, got %d", len(got.Alternatives))
	}
	if got.RiskLevel != domain.RiskLevelHigh {
		t.Fatalf("risk: want high, got %s", got.RiskLevel)
	}

	// Attach an outcome and confirm round-trip.
	if err := conn.Decisions.AttachOutcome(ctx, "dc_1", "oc_xyz"); err != nil {
		t.Fatalf("attach outcome: %v", err)
	}
	got, _ = conn.Decisions.GetByID(ctx, "dc_1")
	if got.OutcomeID != "oc_xyz" {
		t.Fatalf("outcome_id: want oc_xyz, got %q", got.OutcomeID)
	}

	// ListByRiskLevel.
	high, err := conn.Decisions.ListByRiskLevel(ctx, "high")
	if err != nil {
		t.Fatalf("list by risk: %v", err)
	}
	if len(high) != 1 {
		t.Fatalf("want 1 high-risk decision, got %d", len(high))
	}
}

func TestClaim_ScopeRoundTrip(t *testing.T) {
	conn := openTestConn(t)
	ctx := context.Background()
	ts := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	if err := conn.Claims.Upsert(ctx, []domain.Claim{{
		ID: "cl_payments", Text: "deploy works on payments", Type: domain.ClaimTypeFact,
		Confidence: 0.7, Status: domain.ClaimStatusActive, CreatedAt: ts, ValidFrom: ts,
		Scope: domain.Scope{Service: "payments", Env: "prod"},
	}, {
		ID: "cl_search", Text: "deploy works on search", Type: domain.ClaimTypeFact,
		Confidence: 0.7, Status: domain.ClaimStatusActive, CreatedAt: ts, ValidFrom: ts,
		Scope: domain.Scope{Service: "search", Env: "prod"},
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, err := conn.Claims.ListByIDs(ctx, []string{"cl_payments", "cl_search"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, c := range got {
		if c.ID == "cl_payments" && c.Scope.Service != "payments" {
			t.Fatalf("payments scope lost: %+v", c.Scope)
		}
		if c.ID == "cl_search" && c.Scope.Service != "search" {
			t.Fatalf("search scope lost: %+v", c.Scope)
		}
	}
	// Filter via Scope.Matches.
	want := domain.Scope{Service: "payments"}
	matched := 0
	for _, c := range got {
		if c.Scope.Matches(want) {
			matched++
		}
	}
	if matched != 1 {
		t.Fatalf("scope filter want 1 match for service=payments, got %d", matched)
	}
}

func TestLesson_VersionSnapshot(t *testing.T) {
	conn := openTestConn(t)
	ctx := context.Background()
	now := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	base := domain.Lesson{
		ID: "ls_v1", Statement: "v1 statement", Trigger: "x_trigger",
		Scope: domain.Scope{Service: "payments"}, Kind: "rollback",
		Evidence: []string{"ac_1"}, Confidence: 0.7, DerivedAt: now,
	}
	if err := conn.Lessons.Append(ctx, base); err != nil {
		t.Fatalf("append v1: %v", err)
	}
	v2 := base
	v2.Statement = "v2 statement"
	v2.Confidence = 0.85
	v2.DerivedAt = now.Add(time.Hour)
	if err := conn.Lessons.Append(ctx, v2); err != nil {
		t.Fatalf("append v2: %v", err)
	}
	versions, err := conn.Lessons.ListVersions(ctx, "ls_v1")
	if err != nil {
		t.Fatalf("list versions: %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("want 1 prior snapshot after 2 appends, got %d", len(versions))
	}
	if !contains(versions[0].PayloadJSON, "v1 statement") {
		t.Fatalf("snapshot did not capture v1 statement: %s", versions[0].PayloadJSON)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
