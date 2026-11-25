package mnemos_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.klarlabs.de/mnemos"
)

func seedGovernedClaim(t *testing.T, mem mnemos.Memory, eventID, text string) string {
	t.Helper()
	ctx := context.Background()
	if err := mem.RememberEvent(ctx, mnemos.Event{ID: eventID, At: time.Now().UTC(), Type: "observation", Content: "source " + eventID, RunID: "run-gov"}); err != nil {
		t.Fatalf("RememberEvent: %v", err)
	}
	id, err := mem.RememberClaim(ctx, mnemos.ClaimItem{Text: text, EventIDs: []string{eventID}, Confidence: 0.8})
	if err != nil {
		t.Fatalf("RememberClaim: %v", err)
	}
	return id
}

// The governance edits change what Get reads back, record a write session,
// and refuse unknown or malformed requests with the exported sentinels.
func TestClaimGovernance_EditsPersistAndAreGoverned(t *testing.T) {
	mem := newRetractMemory(t)
	ctx := context.Background()
	a := seedGovernedClaim(t, mem, "ev_a", "The cache is warm.")
	b := seedGovernedClaim(t, mem, "ev_b", "The cache is cold.")
	c := seedGovernedClaim(t, mem, "ev_c", "Deploys run on Tuesdays.")

	before := mem.LastWriteSession()
	ch, err := mem.DeprecateClaim(ctx, c, "stale runbook")
	if err != nil || ch.OldStatus != "active" || ch.NewStatus != "deprecated" {
		t.Fatalf("DeprecateClaim = %+v, %v", ch, err)
	}
	if after := mem.LastWriteSession(); after == nil || after == before {
		t.Error("DeprecateClaim left no write session")
	}

	// RestoreClaim undoes it (#393); a second restore changes nothing.
	rs, err := mem.RestoreClaim(ctx, c, "runbook still applies")
	if err != nil || rs.OldStatus != "deprecated" || rs.NewStatus != "active" || rs.HeldBy != "" {
		t.Fatalf("RestoreClaim = %+v, %v", rs, err)
	}
	if again, err := mem.RestoreClaim(ctx, c, ""); err != nil || again.OldStatus != "active" || again.NewStatus != "active" {
		t.Errorf("RestoreClaim of a live claim = %+v, %v; want unchanged", again, err)
	}
	if _, err := mem.RestoreClaim(ctx, "cl_missing", ""); !errors.Is(err, mnemos.ErrClaimNotFound) {
		t.Errorf("RestoreClaim(unknown) = %v, want ErrClaimNotFound", err)
	}
	if _, err := mem.DeprecateClaim(ctx, c, "stale runbook"); err != nil {
		t.Fatalf("DeprecateClaim again: %v", err)
	}

	rev, err := mem.UpdateClaim(ctx, a, "The cache is warm after 9am.", 0.7, "")
	if err != nil || rev.OldText != "The cache is warm." {
		t.Fatalf("UpdateClaim = %+v, %v", rev, err)
	}
	got, err := mem.Get(ctx, a)
	if err != nil || got.Statement != "The cache is warm after 9am." || got.Confidence != 0.7 {
		t.Errorf("Get after update = %+v, %v", got, err)
	}

	if err := mem.ResolveContradiction(ctx, a, b, "observed at 10am"); err != nil {
		t.Fatalf("ResolveContradiction: %v", err)
	}
	history, err := mem.ClaimHistory(ctx, b)
	if err != nil || len(history) == 0 || history[0].Status != "deprecated" {
		t.Errorf("ClaimHistory(loser) = %+v, %v; want newest version deprecated", history, err)
	}

	at, err := mem.VerifyClaim(ctx, a)
	if err != nil || at.IsZero() {
		t.Fatalf("VerifyClaim = %v, %v", at, err)
	}
	esc, err := mem.EscalateClaim(ctx, a, "needs a human")
	if err != nil || esc.EscalationReason != "needs a human" {
		t.Errorf("EscalateClaim = %+v, %v", esc, err)
	}
	prov, err := mem.ClaimProvenance(ctx, a)
	if err != nil || prov.ClaimID != a || prov.Text != "The cache is warm after 9am." || len(prov.Signals) == 0 {
		t.Errorf("ClaimProvenance = %+v, %v", prov, err)
	}

	if _, err := mem.DeprecateClaim(ctx, "cl_missing", ""); !errors.Is(err, mnemos.ErrClaimNotFound) {
		t.Errorf("DeprecateClaim unknown: err = %v, want ErrClaimNotFound", err)
	}
	if _, err := mem.UpdateClaim(ctx, a, " ", 0, ""); !errors.Is(err, mnemos.ErrInvalidArgument) {
		t.Errorf("UpdateClaim blank text: err = %v, want ErrInvalidArgument", err)
	}
	if err := mem.ResolveContradiction(ctx, a, a, ""); !errors.Is(err, mnemos.ErrInvalidArgument) {
		t.Errorf("ResolveContradiction self: err = %v, want ErrInvalidArgument", err)
	}
	if _, err := mem.ClaimProvenance(ctx, "cl_missing"); !errors.Is(err, mnemos.ErrClaimNotFound) {
		t.Errorf("ClaimProvenance unknown: err = %v, want ErrClaimNotFound", err)
	}
}

// Feedback decays confidence and contests a claim on the threshold, as on
// every other surface.
func TestRecordFeedback_DecaysAndContests(t *testing.T) {
	t.Setenv("MNEMOS_FEEDBACK_DECAY", "0.5")
	t.Setenv("MNEMOS_FEEDBACK_CONTEST_THRESHOLD", "2")
	mem := newRetractMemory(t)
	ctx := context.Background()
	id := seedGovernedClaim(t, mem, "ev_fb", "The queue drains hourly.")

	if _, err := mem.RecordFeedback(ctx, id, false, "off topic"); err != nil {
		t.Fatalf("RecordFeedback: %v", err)
	}
	res, err := mem.RecordFeedback(ctx, id, false, "")
	if err != nil || !res.AutoContested || res.NegativeStreak != 2 {
		t.Fatalf("second vote = %+v, %v", res, err)
	}
	got, err := mem.Get(ctx, id)
	if err != nil || got.Confidence != 0.2 {
		t.Errorf("Get after two unhelpful votes: confidence %.3f, %v; want 0.200", got.Confidence, err)
	}
	if _, err := mem.RecordFeedback(ctx, "cl_missing", true, ""); !errors.Is(err, mnemos.ErrClaimNotFound) {
		t.Errorf("unknown claim: err = %v, want ErrClaimNotFound", err)
	}
}

// ListEvents pages newest first and honours the run filter.
func TestListEvents_NewestFirstByRun(t *testing.T) {
	mem := newRetractMemory(t)
	ctx := context.Background()
	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for i, id := range []string{"ev_1", "ev_2", "ev_3"} {
		run := "run-a"
		if id == "ev_2" {
			run = "run-b"
		}
		if err := mem.RememberEvent(ctx, mnemos.Event{ID: id, At: base.Add(time.Duration(i) * time.Hour), Type: "deploy", Content: id, RunID: run}); err != nil {
			t.Fatal(err)
		}
	}
	all, err := mem.ListEvents(ctx, mnemos.EventListQuery{})
	if err != nil || len(all) != 3 || all[0].ID != "ev_3" || all[0].Type != "deploy" {
		t.Fatalf("ListEvents = %+v, %v", all, err)
	}
	runA, err := mem.ListEvents(ctx, mnemos.EventListQuery{RunID: "run-a", Limit: 1})
	if err != nil || len(runA) != 1 || runA[0].ID != "ev_3" {
		t.Errorf("ListEvents(run-a, 1) = %+v, %v", runA, err)
	}
	next, err := mem.ListEvents(ctx, mnemos.EventListQuery{RunID: "run-a", Limit: 1, Offset: 1})
	if err != nil || len(next) != 1 || next[0].ID != "ev_1" {
		t.Errorf("ListEvents(run-a, offset 1) = %+v, %v", next, err)
	}
}
