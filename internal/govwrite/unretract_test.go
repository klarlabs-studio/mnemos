package govwrite_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/retraction"
	_ "go.klarlabs.de/mnemos/internal/store/sqlite"
)

// ADR 0037 §8: un-retraction, asserted on what a real SQLite store returns.

func newSQLiteBrain(t *testing.T) *govwrite.Writer {
	t.Helper()
	w, err := govwrite.New(context.Background(), "sqlite://"+filepath.Join(t.TempDir(), "brain.db"), nil)
	if err != nil {
		t.Fatalf("govwrite.New: %v", err)
	}
	t.Cleanup(func() {
		if err := w.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return w
}

func claimByID(t *testing.T, w *govwrite.Writer, id string) domain.Claim {
	t.Helper()
	got, err := w.Conn().Claims.ListByIDs(context.Background(), []string{id})
	if err != nil || len(got) != 1 {
		t.Fatalf("read %s: %v (%d rows)", id, err, len(got))
	}
	return got[0]
}

func lastTransition(t *testing.T, w *govwrite.Writer, id string) domain.ClaimStatusTransition {
	t.Helper()
	h, err := w.Conn().Claims.ListStatusHistoryByClaimID(context.Background(), id)
	if err != nil || len(h) == 0 {
		t.Fatalf("history of %s: %v (%d rows)", id, err, len(h))
	}
	return h[len(h)-1]
}

func TestUnretractEvent_RestoresWhatTheRetractionChanged(t *testing.T) {
	t.Parallel()
	w := newSQLiteBrain(t)
	ctx := context.Background()
	seedRetractionFixture(t, w)
	conn := w.Conn()
	sharedOriginal := claimByID(t, w, "cl_shared")

	if _, err := w.RetractEvent(ctx, "ev_retract", domain.ChangeNote{Reason: "source was wrong", ChangedBy: "carol"}); err != nil {
		t.Fatalf("retract: %v", err)
	}
	sharedRetracted := claimByID(t, w, "cl_shared")

	rep, err := w.UnretractEvent(ctx, "ev_retract", domain.ChangeNote{Reason: "source was right", ChangedBy: "dave"})
	if err != nil {
		t.Fatalf("UnretractEvent: %v", err)
	}

	// The report.
	if rep.AlreadyUnretracted || rep.EventID != "ev_retract" || rep.UnretractedBy != "dave" ||
		rep.Reason != "source was right" || rep.Generation != 1 || rep.RetractedBy != "carol" {
		t.Errorf("report header = %+v", rep)
	}
	if got := rep.Impact.Restored; len(got) != 1 || got[0].ID != "cl_only" || got[0].Status != domain.ClaimStatusActive {
		t.Errorf("restored = %+v, want cl_only back to active", got)
	}
	if len(rep.Impact.Skipped) != 0 {
		t.Errorf("skipped = %+v, want none", rep.Impact.Skipped)
	}
	if got := rep.Impact.Relinked; len(got) != 1 || got[0].ID != "cl_shared" {
		t.Errorf("relinked = %+v, want cl_shared", got)
	}

	// What the store returns: cl_only is active again through a recorded
	// transition by the caller; the retraction's transition stays below it.
	if got := claimByID(t, w, "cl_only").Status; got != domain.ClaimStatusActive {
		t.Errorf("cl_only status = %s, want active", got)
	}
	last := lastTransition(t, w, "cl_only")
	if last.FromStatus != domain.ClaimStatusDeprecated || last.ToStatus != domain.ClaimStatusActive ||
		!strings.HasPrefix(last.Reason, domain.UnretractionReasonPrefix) || last.ChangedBy != "dave" {
		t.Errorf("last transition = %+v, want deprecated → active, reason %q…, by dave", last, domain.UnretractionReasonPrefix)
	}

	// cl_shared has its link back and its trust recovers.
	links, err := conn.Claims.ListEvidenceByClaimIDs(ctx, []string{"cl_shared"})
	if err != nil || len(links) != 2 {
		t.Errorf("cl_shared evidence = %+v (%v), want ev_keep and ev_retract", links, err)
	}
	shared := claimByID(t, w, "cl_shared")
	if shared.TrustScore <= sharedRetracted.TrustScore {
		t.Errorf("cl_shared trust %.3f, while retracted %.3f: want it back up with its fresh evidence", shared.TrustScore, sharedRetracted.TrustScore)
	}
	if shared.TrustScore != rep.Impact.Relinked[0].TrustAfter || sharedRetracted.TrustScore != rep.Impact.Relinked[0].TrustBefore {
		t.Errorf("report trust %+v disagrees with the store (%.3f → %.3f)", rep.Impact.Relinked[0], sharedRetracted.TrustScore, shared.TrustScore)
	}
	if shared.Status != sharedOriginal.Status {
		t.Errorf("cl_shared status moved: %s → %s", sharedOriginal.Status, shared.Status)
	}

	// The event is no longer retracted; both journal rows remain.
	if _, ok, err := govwrite.LookupRetraction(ctx, conn, "ev_retract"); err != nil || ok {
		t.Errorf("LookupRetraction after un-retraction: ok=%v err=%v, want not retracted", ok, err)
	}
	entries, err := conn.Journal.ListBySubject(ctx, "ev_retract", 0)
	if err != nil {
		t.Fatalf("journal: %v", err)
	}
	kinds := map[string]int{}
	for _, e := range entries {
		kinds[e.Kind]++
	}
	if kinds[domain.JournalKindEventRetraction] != 1 || kinds[domain.JournalKindEventUnretraction] != 1 {
		t.Errorf("journal kinds = %v, want one retraction and one un-retraction", kinds)
	}
	assertEvidence(t, w, "mnemos.write.unretract_event")
}

// A belief returns to the status it had before the retraction, not to active.
func TestUnretractEvent_RestoresThePriorStatus(t *testing.T) {
	t.Parallel()
	w := newSQLiteBrain(t)
	ctx := context.Background()
	seedRetractionFixture(t, w)
	only := claimByID(t, w, "cl_only")
	only.Status = domain.ClaimStatusContested
	if err := w.Conn().Claims.UpsertWithReasonAs(ctx, []domain.Claim{only}, "auto: contradiction detected", "system"); err != nil {
		t.Fatalf("contest: %v", err)
	}
	if _, err := w.RetractEvent(ctx, "ev_retract", domain.ChangeNote{ChangedBy: "carol"}); err != nil {
		t.Fatalf("retract: %v", err)
	}
	rep, err := w.UnretractEvent(ctx, "ev_retract", domain.ChangeNote{ChangedBy: "dave"})
	if err != nil {
		t.Fatalf("unretract: %v", err)
	}
	if got := claimByID(t, w, "cl_only").Status; got != domain.ClaimStatusContested {
		t.Errorf("cl_only status = %s, want contested (its status before the retraction)", got)
	}
	if got := rep.Impact.Restored; len(got) != 1 || got[0].Status != domain.ClaimStatusContested {
		t.Errorf("restored = %+v, want cl_only → contested", got)
	}
}

// A belief someone else changed after the retraction is theirs now.
func TestUnretractEvent_LeavesABeliefSomeoneElseRedeprecated(t *testing.T) {
	t.Parallel()
	w := newSQLiteBrain(t)
	ctx := context.Background()
	seedRetractionFixture(t, w)
	if _, err := w.RetractEvent(ctx, "ev_retract", domain.ChangeNote{Reason: "wrong", ChangedBy: "carol"}); err != nil {
		t.Fatalf("retract: %v", err)
	}
	// Erin reviews it: reinstates it, then deprecates it on her own judgement.
	only := claimByID(t, w, "cl_only")
	only.Status = domain.ClaimStatusActive
	if err := w.Conn().Claims.UpsertWithReasonAs(ctx, []domain.Claim{only}, "reviewed: reinstated", "erin"); err != nil {
		t.Fatalf("reinstate: %v", err)
	}
	only.Status = domain.ClaimStatusDeprecated
	if err := w.Conn().Claims.UpsertWithReasonAs(ctx, []domain.Claim{only}, "reviewed: wrong regardless of source", "erin"); err != nil {
		t.Fatalf("re-deprecate: %v", err)
	}
	historyBefore, _ := w.Conn().Claims.ListStatusHistoryByClaimID(ctx, "cl_only")

	rep, err := w.UnretractEvent(ctx, "ev_retract", domain.ChangeNote{ChangedBy: "dave"})
	if err != nil {
		t.Fatalf("unretract: %v", err)
	}
	if len(rep.Impact.Restored) != 0 {
		t.Errorf("restored = %+v, want none", rep.Impact.Restored)
	}
	if got := rep.Impact.Skipped; len(got) != 1 || got[0].ID != "cl_only" || got[0].Reason == "" {
		t.Errorf("skipped = %+v, want cl_only with a reason", got)
	}
	if got := claimByID(t, w, "cl_only").Status; got != domain.ClaimStatusDeprecated {
		t.Errorf("cl_only status = %s, want still deprecated", got)
	}
	historyAfter, _ := w.Conn().Claims.ListStatusHistoryByClaimID(ctx, "cl_only")
	if len(historyAfter) != len(historyBefore) {
		t.Errorf("status history grew from %d to %d for a skipped belief", len(historyBefore), len(historyAfter))
	}
	if last := historyAfter[len(historyAfter)-1]; last.ChangedBy != "erin" {
		t.Errorf("last transition by %q, want erin's to stand", last.ChangedBy)
	}
}

// A person who deprecates an already-retracted belief again, with a reason of
// their own, owns its status: the deliberate write records a reassertion row
// (deprecated → deprecated) and un-retraction leaves the belief deprecated and
// says why. The write goes through the governed writer exactly as
// memory_deprecate and forget do.
func TestUnretractEvent_LeavesABeliefSomeoneReassertedDeprecated(t *testing.T) {
	t.Parallel()
	w := newSQLiteBrain(t)
	ctx := context.Background()
	seedRetractionFixture(t, w)
	if _, err := w.RetractEvent(ctx, "ev_retract", domain.ChangeNote{Reason: "wrong", ChangedBy: "carol"}); err != nil {
		t.Fatalf("retract: %v", err)
	}
	only := claimByID(t, w, "cl_only")
	if only.Status != domain.ClaimStatusDeprecated {
		t.Fatalf("cl_only status after retraction = %s, want deprecated", only.Status)
	}
	if _, err := w.Claims(ctx, []domain.Claim{only}, govwrite.ClaimReason{
		Reason: "reviewed: wrong regardless of source", ChangedBy: "erin", AssertStatus: true,
	}); err != nil {
		t.Fatalf("re-deprecate: %v", err)
	}
	last := lastTransition(t, w, "cl_only")
	if !last.IsReassertion() || last.ToStatus != domain.ClaimStatusDeprecated ||
		last.ChangedBy != "erin" || last.Reason != "reviewed: wrong regardless of source" {
		t.Fatalf("last transition = %+v, want erin's deprecated → deprecated reassertion", last)
	}

	rep, err := w.UnretractEvent(ctx, "ev_retract", domain.ChangeNote{ChangedBy: "dave"})
	if err != nil {
		t.Fatalf("unretract: %v", err)
	}
	if len(rep.Impact.Restored) != 0 {
		t.Errorf("restored = %+v, want none", rep.Impact.Restored)
	}
	got := rep.Impact.Skipped
	if len(got) != 1 || got[0].ID != "cl_only" ||
		!strings.Contains(got[0].Reason, "reasserted as deprecated") || !strings.Contains(got[0].Reason, "erin") ||
		!strings.Contains(got[0].Reason, "reviewed: wrong regardless of source") {
		t.Errorf("skipped = %+v, want cl_only naming erin's reassertion and her reason", got)
	}
	if got := claimByID(t, w, "cl_only").Status; got != domain.ClaimStatusDeprecated {
		t.Errorf("cl_only status = %s, want still deprecated", got)
	}
	if last := lastTransition(t, w, "cl_only"); last.ChangedBy != "erin" {
		t.Errorf("last transition by %q, want erin's to stand", last.ChangedBy)
	}
}

// Asserting a status needs a reason: the reassertion row is the record of a
// decision, and a decision without its why is not one.
func TestClaims_AssertStatusRequiresAReason(t *testing.T) {
	t.Parallel()
	w := newSQLiteBrain(t)
	seedRetractionFixture(t, w)
	only := claimByID(t, w, "cl_only")
	if _, err := w.Claims(context.Background(), []domain.Claim{only}, govwrite.ClaimReason{ChangedBy: "erin", AssertStatus: true}); err == nil {
		t.Fatal("asserting a status without a reason succeeded, want an error")
	}
}

func TestUnretractEvent_SecondCallChangesNothing(t *testing.T) {
	t.Parallel()
	w := newSQLiteBrain(t)
	ctx := context.Background()
	seedRetractionFixture(t, w)
	if _, err := w.RetractEvent(ctx, "ev_retract", domain.ChangeNote{ChangedBy: "carol"}); err != nil {
		t.Fatalf("retract: %v", err)
	}
	first, err := w.UnretractEvent(ctx, "ev_retract", domain.ChangeNote{Reason: "right", ChangedBy: "dave"})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	historyBefore, _ := w.Conn().Claims.ListStatusHistoryByClaimID(ctx, "cl_only")
	second, err := w.UnretractEvent(ctx, "ev_retract", domain.ChangeNote{Reason: "again", ChangedBy: "erin"})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !second.AlreadyUnretracted {
		t.Error("second un-retraction not reported as a repeat")
	}
	if second.UnretractedBy != "dave" || second.Reason != "right" || !second.UnretractedAt.Equal(first.UnretractedAt) {
		t.Errorf("second report = %+v, want the original record", second)
	}
	historyAfter, _ := w.Conn().Claims.ListStatusHistoryByClaimID(ctx, "cl_only")
	if len(historyAfter) != len(historyBefore) {
		t.Errorf("status history grew from %d to %d on a repeat un-retraction", len(historyBefore), len(historyAfter))
	}
	entries, _ := w.Conn().Journal.ListBySubject(ctx, "ev_retract", 0)
	if len(entries) != 2 {
		t.Errorf("journal holds %d rows for the event, want 2", len(entries))
	}
}

func TestUnretractEvent_NeverRetractedIsAnError(t *testing.T) {
	t.Parallel()
	w := newSQLiteBrain(t)
	ctx := context.Background()
	seedRetractionFixture(t, w)
	if _, err := govwrite.ApplyUnretraction(ctx, w.Conn(), "ev_retract", domain.ChangeNote{}); !errors.Is(err, retraction.ErrNotRetracted) {
		t.Errorf("ApplyUnretraction of a never-retracted event: err = %v, want ErrNotRetracted", err)
	}
	if _, err := w.UnretractEvent(ctx, "ev_retract", domain.ChangeNote{}); err == nil {
		t.Error("UnretractEvent of a never-retracted event succeeded")
	}
	if _, err := w.UnretractEvent(ctx, " ", domain.ChangeNote{}); err == nil {
		t.Error("UnretractEvent of an empty id succeeded")
	}
	if got := claimByID(t, w, "cl_only").Status; got != domain.ClaimStatusActive {
		t.Errorf("cl_only status = %s after a refused un-retraction, want active", got)
	}
}

// Retraction after an un-retraction is a new generation, and so is clearing it.
func TestRetractEvent_AfterUnretractionRetractsAgain(t *testing.T) {
	t.Parallel()
	w := newSQLiteBrain(t)
	ctx := context.Background()
	seedRetractionFixture(t, w)
	if _, err := w.RetractEvent(ctx, "ev_retract", domain.ChangeNote{ChangedBy: "carol"}); err != nil {
		t.Fatalf("retract 1: %v", err)
	}
	if _, err := w.UnretractEvent(ctx, "ev_retract", domain.ChangeNote{ChangedBy: "dave"}); err != nil {
		t.Fatalf("unretract 1: %v", err)
	}
	again, err := w.RetractEvent(ctx, "ev_retract", domain.ChangeNote{Reason: "wrong after all", ChangedBy: "erin"})
	if err != nil {
		t.Fatalf("retract 2: %v", err)
	}
	if again.AlreadyRetracted || again.Generation != 2 {
		t.Errorf("second retraction = %+v, want a fresh generation-2 retraction", again)
	}
	if got := again.Impact.RetractedBeliefs; len(got) != 1 || got[0] != "cl_only" {
		t.Errorf("retracted = %v, want [cl_only]", got)
	}
	if got := claimByID(t, w, "cl_only").Status; got != domain.ClaimStatusDeprecated {
		t.Errorf("cl_only status = %s, want deprecated again", got)
	}
	rep, err := w.UnretractEvent(ctx, "ev_retract", domain.ChangeNote{ChangedBy: "dave"})
	if err != nil {
		t.Fatalf("unretract 2: %v", err)
	}
	if rep.AlreadyUnretracted || rep.Generation != 2 || rep.RetractedBy != "erin" {
		t.Errorf("second un-retraction = %+v, want generation 2 clearing erin's retraction", rep)
	}
	if got := claimByID(t, w, "cl_only").Status; got != domain.ClaimStatusActive {
		t.Errorf("cl_only status = %s, want active again", got)
	}
}
