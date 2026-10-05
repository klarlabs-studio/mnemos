package govwrite_test

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/trust"
)

// Deleting one episode must not destroy a belief that other episodes still
// support. The cascade used to delete every claim linked to the event,
// regardless of what else backed it.
func TestDeleteEventCascade_KeepsClaimsWithOtherEvidence(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-200 * 24 * time.Hour)

	events := []domain.Event{
		{ID: "ev_old", Content: "old source", SchemaVersion: "1.0", SourceInputID: "src_old", Timestamp: old, IngestedAt: old, CreatedBy: "alice"},
		{ID: "ev_new", Content: "new source", SchemaVersion: "1.0", SourceInputID: "src_new", Timestamp: now, IngestedAt: now, CreatedBy: "bob"},
	}
	if _, err := w.Events(ctx, events); err != nil {
		t.Fatalf("seed events: %v", err)
	}
	seedClaim(t, w, "cl_shared") // supported by both episodes
	seedClaim(t, w, "cl_only")   // supported only by the episode being deleted
	if _, err := w.EvidenceLinks(ctx, []domain.ClaimEvidence{
		{ClaimID: "cl_shared", EventID: "ev_old"},
		{ClaimID: "cl_shared", EventID: "ev_new"},
		{ClaimID: "cl_only", EventID: "ev_new"},
	}); err != nil {
		t.Fatalf("seed links: %v", err)
	}
	conn := w.Conn()
	scorer, ok := conn.Claims.(ports.TrustScorer)
	if !ok {
		t.Fatal("memory backend lost TrustScorer")
	}
	if _, err := scorer.RecomputeTrust(ctx, trust.Scorer(now)); err != nil {
		t.Fatalf("score trust: %v", err)
	}
	before, err := conn.Claims.ListByIDs(ctx, []string{"cl_shared"})
	if err != nil || len(before) != 1 {
		t.Fatalf("read cl_shared before: %v %v", before, err)
	}

	n, err := w.DeleteEventCascade(ctx, "ev_new")
	if err != nil {
		t.Fatalf("DeleteEventCascade: %v", err)
	}
	if n != 1 {
		t.Errorf("deleted %d claims, want 1 (only the claim whose sole evidence was the event)", n)
	}

	got, err := conn.Claims.ListByIDs(ctx, []string{"cl_shared", "cl_only"})
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(got) != 1 || got[0].ID != "cl_shared" {
		t.Fatalf("surviving claims = %+v, want exactly cl_shared", got)
	}
	if got[0].Status != domain.ClaimStatusActive {
		t.Errorf("kept claim status = %s, want active (it still has evidence)", got[0].Status)
	}

	links, err := conn.Claims.ListEvidenceByClaimIDs(ctx, []string{"cl_shared"})
	if err != nil {
		t.Fatalf("list evidence: %v", err)
	}
	if len(links) != 1 || links[0].EventID != "ev_old" {
		t.Errorf("cl_shared evidence = %+v, want only ev_old", links)
	}

	// The kept claim lost its fresh episode and stands on a 200-day-old one,
	// so its rescored trust must fall: proof that it was rescored, not left on
	// a cache computed from evidence that no longer exists.
	if before[0].TrustScore <= 0 {
		t.Fatalf("fixture trust is %.3f; the rescore assertion would be vacuous", before[0].TrustScore)
	}
	if got[0].TrustScore >= before[0].TrustScore {
		t.Errorf("trust after losing fresh evidence = %.3f, before = %.3f; want lower", got[0].TrustScore, before[0].TrustScore)
	}
	if _, err := conn.Events.GetByID(ctx, "ev_new"); err == nil {
		t.Error("deleted event survived")
	}
	if _, err := conn.Events.GetByID(ctx, "ev_old"); err != nil {
		t.Errorf("unrelated event was removed: %v", err)
	}
}
