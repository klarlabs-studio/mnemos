package main

import (
	"context"
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/query"
)

func TestDSNOverride_ContextRoundTripAndPrecedence(t *testing.T) {
	base := context.Background()
	if _, ok := dsnOverrideFromContext(base); ok {
		t.Fatal("no override on a bare context")
	}
	ctx := withDSNOverride(base, "sqlite:///tmp/repo.db")
	got, ok := dsnOverrideFromContext(ctx)
	if !ok || got != "sqlite:///tmp/repo.db" {
		t.Fatalf("override round-trip: got (%q,%v)", got, ok)
	}
	// The override wins in resolveDSNForContext, bypassing tenant scoping.
	t.Setenv("MNEMOS_DB_URL", "sqlite:///global.db")
	dsn, err := resolveDSNForContext(ctx)
	if err != nil || dsn != "sqlite:///tmp/repo.db" {
		t.Fatalf("resolveDSNForContext with override = (%q, %v), want the override", dsn, err)
	}
	// Empty override is a no-op.
	if c := withDSNOverride(base, ""); c != base {
		t.Error("empty override should return the same context")
	}
}

func TestMergeScopedQuery_RepoWinsAndTagsProvenance(t *testing.T) {
	repo := mcpQueryOutput{
		Answer: "repo answer",
		Claims: []domain.Claim{
			{ID: "r1", Text: "uses Kafka"},
			{ID: "r2", Text: "shared fact"},
		},
		Contradictions: []domain.Relationship{{FromClaimID: "r1", ToClaimID: "r2"}},
		Timeline:       []string{"ev-repo"},
	}
	global := mcpQueryOutput{
		Answer: "global answer",
		Claims: []domain.Claim{
			{ID: "g1", Text: "shared fact"}, // duplicate text → repo wins
			{ID: "g2", Text: "general pref"},
		},
		Timeline: []string{"ev-global"},
	}
	out := mergeScopedQuery(repo, global, query.PrecedenceTenantWins)

	if len(out.Claims) != 3 {
		t.Fatalf("want 3 merged claims, got %d: %+v", len(out.Claims), out.Claims)
	}
	// Repo claims lead.
	if out.Claims[0].ID != "r1" || out.Claims[1].ID != "r2" {
		t.Errorf("repo claims should lead: %+v", out.Claims)
	}
	// The duplicate resolves to the repo claim; g1 is dropped.
	for _, c := range out.Claims {
		if c.ID == "g1" {
			t.Error("global duplicate should be dropped in favor of the repo claim")
		}
	}
	// Provenance tags each surviving claim by tier.
	if out.ClaimProvenance["r1"] != "repo" || out.ClaimProvenance["g2"] != "global" {
		t.Errorf("provenance mis-tagged: %v", out.ClaimProvenance)
	}
	// Repo answer preferred when repo had claims.
	if out.Answer != "repo answer" {
		t.Errorf("answer = %q, want repo answer", out.Answer)
	}
	if len(out.Timeline) != 2 {
		t.Errorf("timeline should be unioned: %v", out.Timeline)
	}
}

func TestMergeScopedQuery_FallsBackToGlobalAnswer(t *testing.T) {
	repo := mcpQueryOutput{} // repo found nothing
	global := mcpQueryOutput{Answer: "global answer", Claims: []domain.Claim{{ID: "g1", Text: "x"}}}
	out := mergeScopedQuery(repo, global, query.PrecedenceTenantWins)
	if out.Answer != "global answer" {
		t.Errorf("empty repo → global answer; got %q", out.Answer)
	}
}

// TestMergeScopedQuery_PrecedencePolicies exercises the ADR 0011 Phase C policy
// at the MCP scoped-query merge point.
func TestMergeScopedQuery_PrecedencePolicies(t *testing.T) {
	repoFn := func() mcpQueryOutput {
		return mcpQueryOutput{
			Answer: "repo answer",
			Claims: []domain.Claim{
				{ID: "r1", Text: "shared fact"},       // duplicate of g1
				{ID: "r2", Text: "the API is stable"}, // conflicts with g2
			},
		}
	}
	globalFn := func() mcpQueryOutput {
		return mcpQueryOutput{
			Answer: "global answer",
			Claims: []domain.Claim{
				{ID: "g1", Text: "shared fact"},
				{ID: "g2", Text: "the API is not stable"},
			},
		}
	}

	t.Run("tenant-wins: repo leads, repo answer, no dissonance", func(t *testing.T) {
		out := mergeScopedQuery(repoFn(), globalFn(), query.PrecedenceTenantWins)
		if out.Claims[0].ID != "r1" {
			t.Errorf("repo should lead: %+v", out.Claims)
		}
		if hasClaimID(out.Claims, "g1") {
			t.Error("duplicate g1 should be dropped in favor of r1")
		}
		if out.Answer != "repo answer" {
			t.Errorf("answer = %q, want repo answer", out.Answer)
		}
		if len(out.Contradictions) != 0 {
			t.Errorf("tenant-wins must not synthesize dissonance: %+v", out.Contradictions)
		}
	})

	t.Run("global-wins: global leads, global answer wins the duplicate", func(t *testing.T) {
		out := mergeScopedQuery(repoFn(), globalFn(), query.PrecedenceGlobalWins)
		if out.Claims[0].ID != "g1" {
			t.Errorf("global should lead: %+v", out.Claims)
		}
		if hasClaimID(out.Claims, "r1") {
			t.Error("duplicate r1 should be dropped in favor of g1 under global-wins")
		}
		if out.ClaimProvenance["g1"] != "global" {
			t.Errorf("g1 should be tagged global: %v", out.ClaimProvenance)
		}
		if out.Answer != "global answer" {
			t.Errorf("answer = %q, want global answer", out.Answer)
		}
	})

	t.Run("surface-dissonance: keeps both, surfaces a contradiction", func(t *testing.T) {
		out := mergeScopedQuery(repoFn(), globalFn(), query.PrecedenceSurfaceDissonance)
		if !hasClaimID(out.Claims, "r2") || !hasClaimID(out.Claims, "g2") {
			t.Fatalf("both conflicting claims must survive: %+v", out.Claims)
		}
		found := false
		for _, c := range out.Contradictions {
			if c.Type == domain.RelationshipTypeContradicts &&
				((c.FromClaimID == "r2" && c.ToClaimID == "g2") || (c.FromClaimID == "g2" && c.ToClaimID == "r2")) {
				found = true
			}
		}
		if !found {
			t.Errorf("surface-dissonance should synthesize a contradicts edge r2<->g2: %+v", out.Contradictions)
		}
	})
}

func hasClaimID(claims []domain.Claim, id string) bool {
	for _, c := range claims {
		if c.ID == id {
			return true
		}
	}
	return false
}
