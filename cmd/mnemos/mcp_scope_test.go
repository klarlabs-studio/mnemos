package main

import (
	"context"
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
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
	out := mergeScopedQuery(repo, global)

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
	out := mergeScopedQuery(repo, global)
	if out.Answer != "global answer" {
		t.Errorf("empty repo → global answer; got %q", out.Answer)
	}
}
