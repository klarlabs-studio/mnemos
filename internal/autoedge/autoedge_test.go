package autoedge

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"

	_ "go.klarlabs.de/mnemos/internal/store/memory"
)

func openConn(t *testing.T) *store.Conn {
	t.Helper()
	conn, err := store.Open(context.Background(), "memory://")
	if err != nil {
		t.Fatalf("open memory: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestOnOutcomeAppended_EmitsActionOfBothDirections(t *testing.T) {
	conn := openConn(t)
	ctx := context.Background()
	at := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	if err := conn.Actions.Append(ctx, domain.Action{
		ID: "ac_1", Kind: domain.ActionKindRollback, Subject: "payments", At: at,
	}); err != nil {
		t.Fatalf("seed action: %v", err)
	}
	outcome := domain.Outcome{
		ID: "oc_1", ActionID: "ac_1", Result: domain.OutcomeResultSuccess, ObservedAt: at,
	}
	if err := conn.Outcomes.Append(ctx, outcome); err != nil {
		t.Fatalf("append outcome: %v", err)
	}
	if err := OnOutcomeAppended(ctx, conn.EntityRels, outcome, "alice"); err != nil {
		t.Fatalf("auto-edge: %v", err)
	}
	edges, err := conn.EntityRels.ListByEntity(ctx, "ac_1", domain.RelEntityAction)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(edges) != 2 {
		t.Fatalf("want 2 edges (action_of + outcome_of), got %d", len(edges))
	}
	kinds := map[domain.RelationshipType]bool{}
	for _, e := range edges {
		kinds[e.Kind] = true
	}
	if !kinds[domain.RelationshipTypeActionOf] || !kinds[domain.RelationshipTypeOutcomeOf] {
		t.Fatalf("expected action_of + outcome_of, got %v", kinds)
	}

	// Idempotent re-run.
	if err := OnOutcomeAppended(ctx, conn.EntityRels, outcome, "alice"); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	count, _ := conn.EntityRels.CountAll(ctx)
	if count != 2 {
		t.Fatalf("idempotency: want 2 edges, got %d", count)
	}
}

func TestOnDecisionOutcomeAttached_ValidatesOnSuccess(t *testing.T) {
	conn := openConn(t)
	ctx := context.Background()
	at := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)

	// Seed claims, action, outcome, decision.
	if err := conn.Claims.Upsert(ctx, []domain.Claim{
		{ID: "cl_a", Text: "rollback works", Type: domain.ClaimTypeFact, Confidence: 0.7,
			Status: domain.ClaimStatusActive, CreatedAt: at, ValidFrom: at},
	}); err != nil {
		t.Fatalf("seed claim: %v", err)
	}
	if err := conn.Actions.Append(ctx, domain.Action{
		ID: "ac_1", Kind: domain.ActionKindRollback, Subject: "payments", At: at,
	}); err != nil {
		t.Fatalf("seed action: %v", err)
	}
	if err := conn.Outcomes.Append(ctx, domain.Outcome{
		ID: "oc_1", ActionID: "ac_1", Result: domain.OutcomeResultSuccess, ObservedAt: at,
	}); err != nil {
		t.Fatalf("seed outcome: %v", err)
	}
	if err := conn.Decisions.Append(ctx, domain.Decision{
		ID: "dc_1", Statement: "Roll back", RiskLevel: domain.RiskLevelHigh,
		Beliefs: []string{"cl_a"}, ChosenAt: at,
	}); err != nil {
		t.Fatalf("seed decision: %v", err)
	}

	if err := OnDecisionOutcomeAttached(ctx, conn.Decisions, conn.Outcomes, conn.EntityRels, "dc_1", "oc_1", "alice"); err != nil {
		t.Fatalf("auto-fire: %v", err)
	}
	edges, err := conn.EntityRels.ListByKind(ctx, string(domain.RelationshipTypeValidates))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("want 1 validates edge, got %d", len(edges))
	}
	if edges[0].FromID != "oc_1" || edges[0].ToID != "cl_a" {
		t.Fatalf("validates edge endpoints: %+v", edges[0])
	}
}

func TestOnDecisionOutcomeAttached_RefutesOnFailure(t *testing.T) {
	conn := openConn(t)
	ctx := context.Background()
	at := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	if err := conn.Claims.Upsert(ctx, []domain.Claim{
		{ID: "cl_b", Text: "patch will fix it", Type: domain.ClaimTypeFact, Confidence: 0.6,
			Status: domain.ClaimStatusActive, CreatedAt: at, ValidFrom: at},
	}); err != nil {
		t.Fatalf("seed claim: %v", err)
	}
	if err := conn.Actions.Append(ctx, domain.Action{
		ID: "ac_2", Kind: domain.ActionKindHotfix, Subject: "search", At: at,
	}); err != nil {
		t.Fatalf("seed action: %v", err)
	}
	if err := conn.Outcomes.Append(ctx, domain.Outcome{
		ID: "oc_2", ActionID: "ac_2", Result: domain.OutcomeResultFailure, ObservedAt: at,
	}); err != nil {
		t.Fatalf("seed outcome: %v", err)
	}
	if err := conn.Decisions.Append(ctx, domain.Decision{
		ID: "dc_2", Statement: "Patch", RiskLevel: domain.RiskLevelMedium,
		Beliefs: []string{"cl_b"}, ChosenAt: at,
	}); err != nil {
		t.Fatalf("seed decision: %v", err)
	}
	if err := OnDecisionOutcomeAttached(ctx, conn.Decisions, conn.Outcomes, conn.EntityRels, "dc_2", "oc_2", ""); err != nil {
		t.Fatalf("auto-fire: %v", err)
	}
	edges, err := conn.EntityRels.ListByKind(ctx, string(domain.RelationshipTypeRefutes))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("want 1 refutes edge, got %d", len(edges))
	}
}

func TestOnDecisionOutcomeAttached_NoEdgeForPartialOrUnknown(t *testing.T) {
	conn := openConn(t)
	ctx := context.Background()
	at := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	if err := conn.Claims.Upsert(ctx, []domain.Claim{
		{ID: "cl_c", Text: "x", Type: domain.ClaimTypeFact, Confidence: 0.5,
			Status: domain.ClaimStatusActive, CreatedAt: at, ValidFrom: at},
	}); err != nil {
		t.Fatalf("seed claim: %v", err)
	}
	if err := conn.Actions.Append(ctx, domain.Action{
		ID: "ac_3", Kind: domain.ActionKindRestart, Subject: "x", At: at,
	}); err != nil {
		t.Fatalf("seed action: %v", err)
	}
	if err := conn.Outcomes.Append(ctx, domain.Outcome{
		ID: "oc_3", ActionID: "ac_3", Result: domain.OutcomeResultPartial, ObservedAt: at,
	}); err != nil {
		t.Fatalf("seed outcome: %v", err)
	}
	if err := conn.Decisions.Append(ctx, domain.Decision{
		ID: "dc_3", Statement: "Restart", RiskLevel: domain.RiskLevelLow,
		Beliefs: []string{"cl_c"}, ChosenAt: at,
	}); err != nil {
		t.Fatalf("seed decision: %v", err)
	}
	if err := OnDecisionOutcomeAttached(ctx, conn.Decisions, conn.Outcomes, conn.EntityRels, "dc_3", "oc_3", ""); err != nil {
		t.Fatalf("auto-fire: %v", err)
	}
	count, _ := conn.EntityRels.CountAll(ctx)
	if count != 0 {
		t.Fatalf("partial outcome should not emit edges, got %d", count)
	}
}
