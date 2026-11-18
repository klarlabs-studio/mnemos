package grpc_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	mnemosv1 "go.klarlabs.de/mnemos/proto/gen/mnemos/v1"
)

// ADR 0038 §5: the RPC carries the dependent episodes and the path that
// reached each belief, as the governed writer reported them.
func TestGRPC_RetractEpisode_ReportsDependence(t *testing.T) {
	client, conn, issuer := startSQLiteAuthServer(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, ev := range []domain.Event{
		{ID: "ev-src", Content: "original", SchemaVersion: "1.0", SourceInputID: "advisory", RunID: "r", Timestamp: now, IngestedAt: now},
		{ID: "ev-copy", Content: "copy", SchemaVersion: "1.0", SourceInputID: "blog", RunID: "r", Timestamp: now, IngestedAt: now},
	} {
		if err := conn.Events.Append(ctx, ev); err != nil {
			t.Fatalf("seed event %s: %v", ev.ID, err)
		}
	}
	if err := conn.Dependencies.Append(ctx, []domain.EventDependency{{
		EventID: "ev-copy", DependsOn: "ev-src", Kind: domain.DependencyCopies, Signal: domain.SignalMetadata,
		Reference: "copied_from: ev-src", CreatedAt: now, CreatedBy: "tester",
	}}); err != nil {
		t.Fatalf("seed dependency: %v", err)
	}
	if err := conn.Claims.Upsert(ctx, []domain.Claim{
		{ID: "cl-copied", Text: "copied", Type: domain.ClaimTypeFact, Confidence: 0.9, Status: domain.ClaimStatusActive, CreatedAt: now},
	}); err != nil {
		t.Fatalf("seed claim: %v", err)
	}
	if err := conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{
		{ClaimID: "cl-copied", EventID: "ev-src"}, {ClaimID: "cl-copied", EventID: "ev-copy"},
	}); err != nil {
		t.Fatalf("seed evidence: %v", err)
	}

	got, err := client.RetractEpisode(bearerCtx(t, mintToken(t, issuer, []string{domain.ScopeClaimsWrite}, nil)),
		&mnemosv1.RetractEpisodeRequest{EpisodeId: "ev-src", Reason: "withdrawn"})
	if err != nil {
		t.Fatalf("RetractEpisode: %v", err)
	}
	im := got.GetImpact()
	if b := im.GetRetractedBeliefs(); len(b) != 1 || b[0] != "cl-copied" {
		t.Errorf("retracted beliefs = %v, want the belief its copy kept alive", b)
	}
	hops := im.GetDependentEpisodes()
	if len(hops) != 1 || hops[0].GetEpisodeId() != "ev-copy" || hops[0].GetDependsOn() != "ev-src" ||
		hops[0].GetKind() != "copies" || hops[0].GetSignal() != "metadata" || hops[0].GetReference() != "copied_from: ev-src" {
		t.Errorf("dependent episodes = %v", hops)
	}
	paths := im.GetBeliefPaths()
	if len(paths) != 1 || paths[0].GetBeliefId() != "cl-copied" || paths[0].GetOutcome() != "retracted" ||
		strings.Join(paths[0].GetVia(), ">") != "ev-src" {
		t.Errorf("belief paths = %v, want cl-copied retracted via its direct link", paths)
	}
}
