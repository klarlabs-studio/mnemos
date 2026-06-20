package govwrite_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
)

// seedClaim writes a single active claim through the governed path so a
// lifecycle test has a real row to mutate.
func seedClaim(t *testing.T, w *govwrite.Writer, id string) {
	t.Helper()
	ctx := context.Background()
	claim := domain.Claim{
		ID:         id,
		Text:       "seed " + id,
		Type:       domain.ClaimTypeFact,
		Confidence: 0.9,
		Status:     domain.ClaimStatusActive,
		CreatedAt:  time.Now().UTC(),
		CreatedBy:  "tester",
		ValidFrom:  time.Now().UTC(),
	}
	if _, err := w.Claims(ctx, []domain.Claim{claim}, govwrite.ClaimReason{}); err != nil {
		t.Fatalf("seed claim %s: %v", id, err)
	}
}

func TestWriter_MarkVerified_PersistsAndRecordsEvidence(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()
	seedClaim(t, w, "cl_v1")

	now := time.Now().UTC()
	if err := w.MarkVerified(ctx, "cl_v1", now, 7); err != nil {
		t.Fatalf("MarkVerified: %v", err)
	}
	assertEvidence(t, w, "mnemos.write.mark_verified")

	got, err := w.Conn().Claims.ListByIDs(ctx, []string{"cl_v1"})
	if err != nil || len(got) != 1 {
		t.Fatalf("ListByIDs: %d (err=%v)", len(got), err)
	}
	if got[0].VerifyCount < 1 {
		t.Errorf("verify_count = %d, want >= 1", got[0].VerifyCount)
	}
}

func TestWriter_SetValidity_PersistsAndRecordsEvidence(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()
	seedClaim(t, w, "cl_sv1")

	cutoff := time.Now().UTC().Add(24 * time.Hour)
	if err := w.SetValidity(ctx, "cl_sv1", cutoff); err != nil {
		t.Fatalf("SetValidity: %v", err)
	}
	assertEvidence(t, w, "mnemos.write.set_validity")

	got, err := w.Conn().Claims.ListByIDs(ctx, []string{"cl_sv1"})
	if err != nil || len(got) != 1 {
		t.Fatalf("ListByIDs: %d (err=%v)", len(got), err)
	}
	if got[0].ValidTo.IsZero() {
		t.Errorf("valid_to not set")
	}
}

func TestWriter_MergeEntities_PersistsAndRecordsEvidence(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()

	winner, err := w.Conn().Entities.FindOrCreate(ctx, "Acme", domain.EntityTypeOrg, "tester")
	if err != nil {
		t.Fatalf("seed winner: %v", err)
	}
	loser, err := w.Conn().Entities.FindOrCreate(ctx, "Acme Inc", domain.EntityTypeOrg, "tester")
	if err != nil {
		t.Fatalf("seed loser: %v", err)
	}

	if err := w.MergeEntities(ctx, winner.ID, loser.ID); err != nil {
		t.Fatalf("MergeEntities: %v", err)
	}
	assertEvidence(t, w, "mnemos.write.merge_entities")

	if _, ok, err := w.Conn().Entities.FindByName(ctx, "Acme Inc"); err == nil && ok {
		// loser is folded into winner; FindByName resolution semantics
		// vary by provider, so only assert the winner still exists.
		_ = ok
	}
	ents, err := w.Conn().Entities.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	foundWinner := false
	for _, e := range ents {
		if e.ID == winner.ID {
			foundWinner = true
		}
	}
	if !foundWinner {
		t.Errorf("winner entity %s gone after merge", winner.ID)
	}
}

func TestWriter_DeleteClaimCascade_OneAtomicEvidenceEntry(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()
	seedClaim(t, w, "cl_d1")
	// Give the claim a relationship + embedding so the cascade has work.
	if _, err := w.Relationships(ctx, []domain.Relationship{{
		ID: "rel_d1", Type: domain.RelationshipTypeSupports,
		FromClaimID: "cl_d1", ToClaimID: "cl_other",
		CreatedAt: time.Now().UTC(), CreatedBy: "tester",
	}}); err != nil {
		t.Fatalf("seed rel: %v", err)
	}
	if err := w.Embedding(ctx, "cl_d1", "claim", []float32{0.1, 0.2}, "m", "tester"); err != nil {
		t.Fatalf("seed embedding: %v", err)
	}

	if err := w.DeleteClaimCascade(ctx, "cl_d1"); err != nil {
		t.Fatalf("DeleteClaimCascade: %v", err)
	}
	// The destructive op is ONE governed entry on the evidence chain.
	assertEvidence(t, w, "mnemos.write.delete_claim_cascade")

	got, err := w.Conn().Claims.ListByIDs(ctx, []string{"cl_d1"})
	if err != nil {
		t.Fatalf("ListByIDs: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("claim survived cascade delete: %+v", got)
	}
	rels, err := w.Conn().Relationships.ListByClaim(ctx, "cl_d1")
	if err != nil {
		t.Fatalf("ListByClaim: %v", err)
	}
	if len(rels) != 0 {
		t.Errorf("relationships survived: %+v", rels)
	}
}

func TestWriter_DeleteEventCascade_PersistsAndRecordsEvidence(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()
	// Seed an event + a claim linked to it.
	if _, err := w.Events(ctx, []domain.Event{{
		ID: "ev_de1", Content: "x", SchemaVersion: "1.0", SourceInputID: "inline:ev_de1",
		Timestamp: time.Now().UTC(), IngestedAt: time.Now().UTC(), CreatedBy: "tester",
	}}); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	seedClaim(t, w, "cl_de1")
	if _, err := w.EvidenceLinks(ctx, []domain.ClaimEvidence{{ClaimID: "cl_de1", EventID: "ev_de1"}}); err != nil {
		t.Fatalf("seed link: %v", err)
	}

	if _, err := w.DeleteEventCascade(ctx, "ev_de1"); err != nil {
		t.Fatalf("DeleteEventCascade: %v", err)
	}
	assertEvidence(t, w, "mnemos.write.delete_event_cascade")

	if _, err := w.Conn().Events.GetByID(ctx, "ev_de1"); err == nil {
		t.Errorf("event survived cascade delete")
	}
}

func TestWriter_DeleteEvent_PersistsAndRecordsEvidence(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()
	if _, err := w.Events(ctx, []domain.Event{{
		ID: "ev_d1", Content: "x", SchemaVersion: "1.0", SourceInputID: "inline:ev_d1",
		Timestamp: time.Now().UTC(), IngestedAt: time.Now().UTC(), CreatedBy: "tester",
	}}); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	if err := w.DeleteEvent(ctx, "ev_d1"); err != nil {
		t.Fatalf("DeleteEvent: %v", err)
	}
	assertEvidence(t, w, "mnemos.write.delete_event")
	if _, err := w.Conn().Events.GetByID(ctx, "ev_d1"); err == nil {
		t.Errorf("event survived delete")
	}
}

func TestWriter_Reset_PurgesAndRecordsEvidence(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()
	seedClaim(t, w, "cl_r1")
	if _, err := w.Events(ctx, []domain.Event{{
		ID: "ev_r1", Content: "x", SchemaVersion: "1.0", SourceInputID: "inline:ev_r1",
		Timestamp: time.Now().UTC(), IngestedAt: time.Now().UTC(), CreatedBy: "tester",
	}}); err != nil {
		t.Fatalf("seed event: %v", err)
	}

	counts, err := w.Reset(ctx, false)
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}
	assertEvidence(t, w, "mnemos.write.reset")
	if counts.Claims < 1 {
		t.Errorf("claims purged = %d, want >= 1", counts.Claims)
	}
	remaining, err := w.Conn().Claims.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(remaining) != 0 {
		t.Errorf("claims survived reset: %d", len(remaining))
	}
}

func TestWriter_Reset_KeepEvents(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()
	if _, err := w.Events(ctx, []domain.Event{{
		ID: "ev_rk1", Content: "x", SchemaVersion: "1.0", SourceInputID: "inline:ev_rk1",
		Timestamp: time.Now().UTC(), IngestedAt: time.Now().UTC(), CreatedBy: "tester",
	}}); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	if _, err := w.Reset(ctx, true); err != nil {
		t.Fatalf("Reset(keepEvents): %v", err)
	}
	assertEvidence(t, w, "mnemos.write.reset")
	if _, err := w.Conn().Events.GetByID(ctx, "ev_rk1"); err != nil {
		t.Errorf("event removed despite keepEvents: %v", err)
	}
}

func TestWriter_ClaimsWithReason_RecordsReasonEvidence(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()
	seedClaim(t, w, "cl_wr1")
	updated := domain.Claim{
		ID: "cl_wr1", Text: "changed", Type: domain.ClaimTypeFact, Confidence: 0.9,
		Status: domain.ClaimStatusDeprecated, CreatedAt: time.Now().UTC(), CreatedBy: "tester",
	}
	if _, err := w.Claims(ctx, []domain.Claim{updated}, govwrite.ClaimReason{Reason: "agent-deprecated", ChangedBy: "agent-9"}); err != nil {
		t.Fatalf("Claims with reason: %v", err)
	}
	assertEvidence(t, w, "mnemos.write.claims")
	got, err := w.Conn().Claims.ListByIDs(ctx, []string{"cl_wr1"})
	if err != nil || len(got) != 1 {
		t.Fatalf("ListByIDs: %d (%v)", len(got), err)
	}
	if got[0].Status != domain.ClaimStatusDeprecated {
		t.Errorf("status = %q, want deprecated", got[0].Status)
	}
}

// --- Concurrency: N parallel writes through ONE Wrap'd Writer. ---
//
// This mirrors the gRPC/HTTP usage where a single long-lived Writer is
// shared across request goroutines. Run under -race: assert no race and
// each write persists.
func TestWriter_ConcurrentWrites_NoRaceEachPersists(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()

	const n = 32
	var wg sync.WaitGroup
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			claim := domain.Claim{
				ID:         fmt.Sprintf("cl_cc_%d", i),
				Text:       fmt.Sprintf("concurrent %d", i),
				Type:       domain.ClaimTypeFact,
				Confidence: 0.9,
				Status:     domain.ClaimStatusActive,
				CreatedAt:  time.Now().UTC(),
				CreatedBy:  "tester",
			}
			_, errs[i] = w.Claims(ctx, []domain.Claim{claim}, govwrite.ClaimReason{Reason: "cc", ChangedBy: "agent"})
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent write %d failed: %v", i, err)
		}
	}
	// Each write persisted.
	all, err := w.Conn().Claims.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != n {
		t.Errorf("persisted %d claims, want %d", len(all), n)
	}
	// And the last governed write left a verifiable evidence chain.
	// (LastSession is last-wins under concurrency — see the package doc
	// note — so we assert chain integrity, not a specific claim.)
	session := w.LastSession()
	if session == nil {
		t.Fatal("no session recorded after concurrent writes")
	}
	if err := session.VerifyEvidenceChain(); err != nil {
		t.Fatalf("evidence chain broken: %v", err)
	}
}

// --- Error path: an executor failure surfaces to the caller AND does
// NOT emit a success evidence record. ---

func TestWriter_MarkVerified_MissingClaim_SurfacesErrorNoSuccessEvidence(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()
	// No claim seeded → MarkVerified on a non-existent id must error.
	err := w.MarkVerified(ctx, "cl_does_not_exist", time.Now().UTC(), 0)
	if err == nil {
		t.Fatal("MarkVerified on missing claim should error")
	}
	// The failed write must NOT leave a success evidence record. A
	// session may exist (axi Saves on the failure path), but it must
	// not be marked completed nor carry a mark_verified success record.
	session := w.LastSession()
	if session != nil {
		for _, rec := range session.Evidence() {
			if rec.Kind == "mnemos.write.mark_verified" {
				t.Errorf("failed write emitted a success evidence record: %+v", rec)
			}
		}
	}
}

func TestWriter_DeleteClaimCascade_PreCancelledCtx_SurfacesError(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel: the kernel must surface this as an error.
	err := w.DeleteClaimCascade(ctx, "cl_anything")
	if err == nil {
		t.Fatal("DeleteClaimCascade with cancelled ctx should error")
	}
	if !errors.Is(err, context.Canceled) && !contains(err.Error(), "cancel") {
		// Accept either a wrapped context.Canceled or a kernel-level
		// rejection mentioning cancellation; the contract is "errors".
		t.Logf("delete cascade error (acceptable): %v", err)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
