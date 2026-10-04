package pipeline

import (
	"context"
	"sort"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
)

// guardClaim is one claim in a dedupe-guard fixture. Every vector in these
// fixtures is near-identical, so similarity alone would merge all of them: any
// claim that stays separate is held apart by a guard, which is the point.
type guardClaim struct {
	id     string
	text   string
	typ    domain.ClaimType
	status domain.ClaimStatus
	scope  domain.Scope
	vec    []float32
	closed bool // valid time closed (forgotten / superseded)
}

func seedGuardFixture(t *testing.T, claims []guardClaim, rels []domain.Relationship) *store.Conn {
	t.Helper()
	_, conn := openDedupeDB(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for _, g := range claims {
		c := domain.Claim{
			ID: g.id, Text: g.text, Type: g.typ, Confidence: 0.8,
			Status: g.status, Scope: g.scope, CreatedAt: now,
		}
		if c.Type == "" {
			c.Type = domain.ClaimTypeFact
		}
		if c.Status == "" {
			c.Status = domain.ClaimStatusActive
		}
		if err := conn.Claims.Upsert(ctx, []domain.Claim{c}); err != nil {
			t.Fatalf("upsert %s: %v", g.id, err)
		}
		if g.closed {
			if err := conn.Claims.SetValidity(ctx, g.id, now.Add(time.Hour)); err != nil {
				t.Fatalf("close validity %s: %v", g.id, err)
			}
		}
		vec := g.vec
		if vec == nil {
			vec = []float32{1, 0.01, 0}
		}
		if err := conn.Embeddings.Upsert(ctx, g.id, "claim", vec, "test", ""); err != nil {
			t.Fatalf("embed %s: %v", g.id, err)
		}
	}
	for _, r := range rels {
		r.CreatedAt = now
		if err := conn.Relationships.Upsert(ctx, []domain.Relationship{r}); err != nil {
			t.Fatalf("relationship %s: %v", r.ID, err)
		}
	}
	return conn
}

// mergedSets renders a plan as sorted member sets, so assertions do not depend
// on which member won.
func mergedSets(plan SemanticDedupePlan) [][]string {
	var out [][]string
	for _, m := range plan.Merges {
		set := append([]string{m.WinnerID}, m.DuplicateIDs...)
		sort.Strings(set)
		out = append(out, set)
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

func planFor(t *testing.T, conn *store.Conn) SemanticDedupePlan {
	t.Helper()
	plan, err := PlanSemanticDedupe(context.Background(), conn, 0.92)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return plan
}

func inSameMerge(plan SemanticDedupePlan, a, b string) bool {
	for _, set := range mergedSets(plan) {
		hasA, hasB := false, false
		for _, id := range set {
			hasA = hasA || id == a
			hasB = hasB || id == b
		}
		if hasA && hasB {
			return true
		}
	}
	return false
}

func TestPlanSemanticDedupe_NeverMergesARecordedContradiction(t *testing.T) {
	for _, typ := range []domain.RelationshipType{domain.RelationshipTypeContradicts, domain.RelationshipTypeRefutes} {
		conn := seedGuardFixture(t, []guardClaim{
			{id: "a", text: "The payments service retries failed webhooks three times"},
			{id: "b", text: "The payments service retries failed webhooks five times"},
		}, []domain.Relationship{{ID: "r1", Type: typ, FromClaimID: "b", ToClaimID: "a"}})
		if plan := planFor(t, conn); len(plan.Merges) != 0 {
			t.Fatalf("%s edge: two claims the brain records as disagreeing were merged: %v", typ, mergedSets(plan))
		}
	}
}

func TestPlanSemanticDedupe_NeverMergesAStatementWithItsDenial(t *testing.T) {
	conn := seedGuardFixture(t, []guardClaim{
		{id: "pos", text: "The staging database is safe to reset"},
		{id: "neg", text: "The staging database is not safe to reset"},
	}, nil)
	if plan := planFor(t, conn); len(plan.Merges) != 0 {
		t.Fatalf("a statement and its denial were merged: %v", mergedSets(plan))
	}
}

func TestPlanSemanticDedupe_KeepsScopedAndTypedVariantsApart(t *testing.T) {
	conn := seedGuardFixture(t, []guardClaim{
		{id: "prod", text: "Connection pool size is 50", scope: domain.Scope{Service: "api", Env: "prod"}},
		{id: "stage", text: "Connection pool size is 50", scope: domain.Scope{Service: "api", Env: "staging"}},
		{id: "fact", text: "We use Postgres for the ledger", typ: domain.ClaimTypeFact},
		{id: "decision", text: "We use Postgres for the ledger.", typ: domain.ClaimTypeDecision},
	}, nil)
	plan := planFor(t, conn)
	if inSameMerge(plan, "prod", "stage") {
		t.Fatal("the same statement in two scopes was merged into one belief")
	}
	if inSameMerge(plan, "fact", "decision") {
		t.Fatal("a decision was merged into a fact with the same wording")
	}
}

func TestPlanSemanticDedupe_RetiredClaimsAreNeverCandidates(t *testing.T) {
	conn := seedGuardFixture(t, []guardClaim{
		{id: "live", text: "Deploys go out on Tuesdays"},
		{id: "deprecated", text: "Deploys go out on Tuesdays.", status: domain.ClaimStatusDeprecated},
		{id: "forgotten", text: "Deploys go out on Tuesdays!", closed: true},
	}, nil)
	plan := planFor(t, conn)
	if len(plan.Merges) != 0 {
		t.Fatalf("retired claims were merged into a live one: %v", mergedSets(plan))
	}
	if plan.SkippedRetired != 2 {
		t.Fatalf("SkippedRetired = %d, want 2", plan.SkippedRetired)
	}
}

// Single-linkage chaining merged A and C through B even when A and C were not
// near-duplicates of each other. Complete linkage requires every pair.
func TestPlanSemanticDedupe_DoesNotChainThroughAMiddleClaim(t *testing.T) {
	// cos(a,b) ≈ 0.94, cos(b,c) ≈ 0.94, cos(a,c) ≈ 0.77 at threshold 0.92.
	conn := seedGuardFixture(t, []guardClaim{
		{id: "a", text: "alpha", vec: []float32{1, 0, 0}},
		{id: "b", text: "beta", vec: []float32{0.94, 0.341, 0}},
		{id: "c", text: "gamma", vec: []float32{0.77, 0.638, 0}},
	}, nil)
	plan := planFor(t, conn)
	if inSameMerge(plan, "a", "c") {
		t.Fatalf("a and c merged through b although they are not near-duplicates: %v", mergedSets(plan))
	}
	if len(plan.Merges) != 1 {
		t.Fatalf("want exactly one pair merged, got %v", mergedSets(plan))
	}
}

// A guard that blocks everything would pass every test above. Plain
// paraphrases with nothing separating them must still merge.
func TestPlanSemanticDedupe_StillMergesPlainParaphrases(t *testing.T) {
	conn := seedGuardFixture(t, []guardClaim{
		{id: "p1", text: "The team owns the billing pipeline"},
		{id: "p2", text: "The team owns the billing pipeline."},
		{id: "p3", text: "the team owns the billing pipeline"},
	}, nil)
	plan := planFor(t, conn)
	if got := mergedSets(plan); len(got) != 1 || len(got[0]) != 3 {
		t.Fatalf("three plain paraphrases should form one cluster, got %v", got)
	}
}
