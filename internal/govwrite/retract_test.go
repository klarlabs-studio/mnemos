package govwrite_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/trust"
)

// seedRetractionFixture stores two events and two beliefs: cl_only rests on
// ev_retract alone, cl_shared on ev_retract and ev_keep. A decision cites
// cl_only, and an association joins the two beliefs.
func seedRetractionFixture(t *testing.T, w *govwrite.Writer) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-200 * 24 * time.Hour)
	if _, err := w.Events(ctx, []domain.Event{
		{ID: "ev_keep", RunID: "r1", Content: "old source", SchemaVersion: "1.0", SourceInputID: "src_keep", Timestamp: old, IngestedAt: old, CreatedBy: "alice"},
		{ID: "ev_retract", RunID: "r1", Content: "new source", SchemaVersion: "1.0", SourceInputID: "src_retract", Timestamp: now, IngestedAt: now, CreatedBy: "bob"},
	}); err != nil {
		t.Fatalf("seed events: %v", err)
	}
	seedClaim(t, w, "cl_shared")
	seedClaim(t, w, "cl_only")
	if _, err := w.EvidenceLinks(ctx, []domain.ClaimEvidence{
		{ClaimID: "cl_shared", EventID: "ev_keep"},
		{ClaimID: "cl_shared", EventID: "ev_retract"},
		{ClaimID: "cl_only", EventID: "ev_retract"},
	}); err != nil {
		t.Fatalf("seed links: %v", err)
	}
	if _, err := w.Relationships(ctx, []domain.Relationship{{
		ID: "rel_only_shared", Type: domain.RelationshipTypeSupports,
		FromClaimID: "cl_only", ToClaimID: "cl_shared", CreatedAt: now,
	}}); err != nil {
		t.Fatalf("seed relationship: %v", err)
	}
	if _, err := w.Decision(ctx, domain.Decision{
		ID: "dec_1", Statement: "ship it", RiskLevel: domain.RiskLevelLow,
		Beliefs: []string{"cl_only"}, ChosenAt: now, CreatedAt: now,
	}); err != nil {
		t.Fatalf("seed decision: %v", err)
	}
	scorer, ok := w.Conn().Claims.(ports.TrustScorer)
	if !ok {
		t.Fatal("memory backend lost TrustScorer")
	}
	if _, err := scorer.RecomputeTrust(ctx, trust.Scorer(now)); err != nil {
		t.Fatalf("score trust: %v", err)
	}
}

func TestRetractEvent_RetractsSoleEvidenceAndRescoresTheRest(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()
	seedRetractionFixture(t, w)
	conn := w.Conn()
	before, err := conn.Claims.ListByIDs(ctx, []string{"cl_shared"})
	if err != nil || len(before) != 1 {
		t.Fatalf("read cl_shared before: %v %v", before, err)
	}

	rep, err := w.RetractEvent(ctx, "ev_retract", domain.ChangeNote{Reason: "source was wrong", ChangedBy: "carol"})
	if err != nil {
		t.Fatalf("RetractEvent: %v", err)
	}

	// The report.
	if rep.AlreadyRetracted {
		t.Error("first retraction reported as a repeat")
	}
	if rep.EventID != "ev_retract" || rep.RetractedBy != "carol" || rep.Reason != "source was wrong" || rep.RetractedAt.IsZero() {
		t.Errorf("report header = %+v", rep)
	}
	if got := rep.Impact.RetractedBeliefs; len(got) != 1 || got[0] != "cl_only" {
		t.Errorf("retracted beliefs = %v, want [cl_only]", got)
	}
	if got := rep.Impact.Rescored; len(got) != 1 || got[0].ID != "cl_shared" || got[0].RemainingEvidence != 1 {
		t.Errorf("rescored = %+v, want cl_shared with 1 remaining", got)
	}
	if got := rep.Impact.Associations; len(got) != 1 || got[0] != "rel_only_shared" {
		t.Errorf("associations = %v, want [rel_only_shared]", got)
	}
	if got := rep.Impact.Decisions; len(got) != 1 || got[0].DecisionID != "dec_1" || len(got[0].Beliefs) != 1 || got[0].Beliefs[0] != "cl_only" {
		t.Errorf("decisions = %+v, want dec_1 citing cl_only", got)
	}
	if len(rep.Impact.Uncertainty) == 0 {
		t.Error("report states no uncertainty")
	}

	// What the store returns: the sole-evidence belief is retracted, with a
	// status transition whose reason marks it as a retraction.
	only, err := conn.Claims.ListByIDs(ctx, []string{"cl_only"})
	if err != nil || len(only) != 1 {
		t.Fatalf("read cl_only: %v %v", only, err)
	}
	if only[0].Status != domain.ClaimStatusDeprecated {
		t.Errorf("cl_only status = %s, want deprecated", only[0].Status)
	}
	history, err := conn.Claims.ListStatusHistoryByClaimID(ctx, "cl_only")
	if err != nil {
		t.Fatalf("status history: %v", err)
	}
	last := history[len(history)-1]
	if last.ToStatus != domain.ClaimStatusDeprecated || !strings.HasPrefix(last.Reason, domain.RetractionReasonPrefix) || last.ChangedBy != "carol" {
		t.Errorf("last transition = %+v, want → deprecated, reason %q…, by carol", last, domain.RetractionReasonPrefix)
	}
	// Its evidence link stays: the event still existed before the retraction,
	// and a recorded-as-of read reaches the belief through it.
	links, err := conn.Claims.ListEvidenceByClaimIDs(ctx, []string{"cl_only"})
	if err != nil || len(links) != 1 {
		t.Errorf("cl_only evidence = %+v (%v), want its link kept", links, err)
	}

	// The shared belief keeps its status, loses only the retracted link, and is
	// rescored on what remains (a 200-day-old episode, so trust falls).
	shared, err := conn.Claims.ListByIDs(ctx, []string{"cl_shared"})
	if err != nil || len(shared) != 1 {
		t.Fatalf("read cl_shared: %v %v", shared, err)
	}
	if shared[0].Status != domain.ClaimStatusActive {
		t.Errorf("cl_shared status = %s, want active", shared[0].Status)
	}
	links, err = conn.Claims.ListEvidenceByClaimIDs(ctx, []string{"cl_shared"})
	if err != nil || len(links) != 1 || links[0].EventID != "ev_keep" {
		t.Errorf("cl_shared evidence = %+v (%v), want only ev_keep", links, err)
	}
	if shared[0].TrustScore >= before[0].TrustScore {
		t.Errorf("cl_shared trust %.3f, before %.3f: want lower after losing its fresh evidence", shared[0].TrustScore, before[0].TrustScore)
	}
	if rep.Impact.Rescored[0].TrustAfter != shared[0].TrustScore || rep.Impact.Rescored[0].TrustBefore != before[0].TrustScore {
		t.Errorf("report trust %+v disagrees with store (before %.3f, after %.3f)", rep.Impact.Rescored[0], before[0].TrustScore, shared[0].TrustScore)
	}

	// The event itself is immutable and stays.
	if _, err := conn.Events.GetByID(ctx, "ev_retract"); err != nil {
		t.Errorf("retracted event was removed: %v", err)
	}

	// The retraction is recorded and readable.
	stored, ok, err := govwrite.LookupRetraction(ctx, conn, "ev_retract")
	if err != nil || !ok {
		t.Fatalf("LookupRetraction: ok=%v err=%v", ok, err)
	}
	if stored.RetractedBy != "carol" || len(stored.Impact.RetractedBeliefs) != 1 {
		t.Errorf("stored retraction = %+v", stored)
	}
	assertEvidence(t, w, "mnemos.write.retract_event")
}

func TestRetractEvent_SecondCallChangesNothing(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()
	seedRetractionFixture(t, w)
	conn := w.Conn()

	first, err := w.RetractEvent(ctx, "ev_retract", domain.ChangeNote{Reason: "wrong", ChangedBy: "carol"})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	historyBefore, _ := conn.Claims.ListStatusHistoryByClaimID(ctx, "cl_only")
	sharedBefore, _ := conn.Claims.ListByIDs(ctx, []string{"cl_shared"})

	second, err := w.RetractEvent(ctx, "ev_retract", domain.ChangeNote{Reason: "again", ChangedBy: "dave"})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !second.AlreadyRetracted {
		t.Error("second retraction not reported as a repeat")
	}
	if second.RetractedBy != "carol" || second.Reason != "wrong" || !second.RetractedAt.Equal(first.RetractedAt) {
		t.Errorf("second report = %+v, want the original record", second)
	}
	historyAfter, _ := conn.Claims.ListStatusHistoryByClaimID(ctx, "cl_only")
	if len(historyAfter) != len(historyBefore) {
		t.Errorf("status history grew from %d to %d on a repeat retraction", len(historyBefore), len(historyAfter))
	}
	sharedAfter, _ := conn.Claims.ListByIDs(ctx, []string{"cl_shared"})
	if sharedAfter[0].TrustScore != sharedBefore[0].TrustScore {
		t.Errorf("trust moved on a repeat retraction: %.3f → %.3f", sharedBefore[0].TrustScore, sharedAfter[0].TrustScore)
	}
	entries, err := conn.Journal.ListBySubject(ctx, "ev_retract", 0)
	if err != nil {
		t.Fatalf("journal: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("journal holds %d retraction rows, want 1", len(entries))
	}
}

// A belief whose only other evidence is itself retracted has no live evidence:
// retracting its last live source retracts it.
func TestRetractEvent_EvidenceFromRetractedEventsDoesNotCount(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if _, err := w.Events(ctx, []domain.Event{
		{ID: "ev_a", Content: "a", SchemaVersion: "1.0", SourceInputID: "s", Timestamp: now, IngestedAt: now},
		{ID: "ev_b", Content: "b", SchemaVersion: "1.0", SourceInputID: "s", Timestamp: now, IngestedAt: now},
		{ID: "ev_x", Content: "x", SchemaVersion: "1.0", SourceInputID: "s", Timestamp: now, IngestedAt: now},
	}); err != nil {
		t.Fatalf("seed events: %v", err)
	}
	seedClaim(t, w, "cl_x")
	seedClaim(t, w, "cl_ab")
	if _, err := w.EvidenceLinks(ctx, []domain.ClaimEvidence{
		{ClaimID: "cl_x", EventID: "ev_x"},
		{ClaimID: "cl_ab", EventID: "ev_a"},
	}); err != nil {
		t.Fatalf("seed links: %v", err)
	}
	// ev_a retracts cl_ab (its only evidence) and keeps the link.
	if _, err := w.RetractEvent(ctx, "ev_a", domain.ChangeNote{Reason: "r"}); err != nil {
		t.Fatalf("retract ev_a: %v", err)
	}
	// A later restatement attaches ev_b to the deprecated cl_ab and ev_a to cl_x.
	if _, err := w.EvidenceLinks(ctx, []domain.ClaimEvidence{
		{ClaimID: "cl_ab", EventID: "ev_b"},
		{ClaimID: "cl_x", EventID: "ev_a"},
	}); err != nil {
		t.Fatalf("relink: %v", err)
	}
	rep, err := w.RetractEvent(ctx, "ev_x", domain.ChangeNote{Reason: "r"})
	if err != nil {
		t.Fatalf("retract ev_x: %v", err)
	}
	if got := rep.Impact.RetractedBeliefs; len(got) != 1 || got[0] != "cl_x" {
		t.Errorf("retracted = %v, want [cl_x]: its other evidence (ev_a) is retracted", got)
	}
	if len(rep.Impact.Rescored) != 0 {
		t.Errorf("rescored = %+v, want none", rep.Impact.Rescored)
	}
}

func TestRetractEvent_UnknownEventIsAnError(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	if _, err := w.RetractEvent(context.Background(), "ev_missing", domain.ChangeNote{}); err == nil {
		t.Fatal("retracting an unknown event succeeded")
	}
	if _, err := w.RetractEvent(context.Background(), "  ", domain.ChangeNote{}); err == nil {
		t.Fatal("retracting an empty id succeeded")
	}
}
