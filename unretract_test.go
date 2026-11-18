package mnemos_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.klarlabs.de/mnemos"
)

// UnretractEvent restores the claim the retraction took, relinks the claim it
// rescored, records the actor, and is idempotent; the event can then be
// retracted again (ADR 0037 §8).
func TestUnretractEvent_RestoresAndRelinks(t *testing.T) {
	mem := newRetractMemory(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, id := range []string{"ev_retract", "ev_keep"} {
		if err := mem.RememberEvent(ctx, mnemos.Event{ID: id, At: now, Type: "observation", Content: "source " + id}); err != nil {
			t.Fatalf("RememberEvent %s: %v", id, err)
		}
	}
	only, err := mem.RememberClaim(ctx, mnemos.ClaimItem{Text: "The queue drains hourly.", EventIDs: []string{"ev_retract"}, ValidFrom: now})
	if err != nil {
		t.Fatalf("RememberClaim only: %v", err)
	}
	shared, err := mem.RememberClaim(ctx, mnemos.ClaimItem{Text: "Billing runs in eu-west-1.", EventIDs: []string{"ev_retract", "ev_keep"}, ValidFrom: now})
	if err != nil {
		t.Fatalf("RememberClaim shared: %v", err)
	}
	if _, err := mem.RetractEvent(ctx, "ev_retract", "wrong runbook"); err != nil {
		t.Fatalf("RetractEvent: %v", err)
	}

	rep, err := mem.UnretractEvent(ctx, " ev_retract ", " runbook was right ")
	if err != nil {
		t.Fatalf("UnretractEvent: %v", err)
	}
	if rep.EventID != "ev_retract" || rep.AlreadyUnretracted || rep.Generation != 1 {
		t.Fatalf("report = %+v, want a first un-retraction of ev_retract", rep)
	}
	if rep.Reason != "runbook was right" || rep.UnretractedBy != "lib-operator" {
		t.Errorf("reason/actor = %q/%q, want the trimmed reason and the Memory's actor", rep.Reason, rep.UnretractedBy)
	}
	if got := rep.Impact.Restored; len(got) != 1 || got[0].ID != only || got[0].Status != "active" {
		t.Errorf("restored = %+v, want %s back to active", got, only)
	}
	if got := rep.Impact.Relinked; len(got) != 1 || got[0].ID != shared {
		t.Errorf("relinked = %+v, want %s", got, shared)
	}
	if mem.LastWriteSession() == nil {
		t.Error("un-retraction left no governed write session")
	}

	again, err := mem.UnretractEvent(ctx, "ev_retract", "again")
	if err != nil || !again.AlreadyUnretracted || !again.UnretractedAt.Equal(rep.UnretractedAt) {
		t.Errorf("repeat = %+v, %v; want the stored first record", again, err)
	}

	// The claim is live again, so a new retraction takes it again.
	re, err := mem.RetractEvent(ctx, "ev_retract", "wrong after all")
	if err != nil {
		t.Fatalf("re-retract: %v", err)
	}
	if re.AlreadyRetracted || len(re.Impact.RetractedClaims) != 1 || re.Impact.RetractedClaims[0] != only {
		t.Errorf("re-retraction = %+v, want a fresh retraction of %s", re, only)
	}
}

func TestUnretractEvent_Refusals(t *testing.T) {
	mem := newRetractMemory(t)
	ctx := context.Background()
	if err := mem.RememberEvent(ctx, mnemos.Event{ID: "ev_fine", At: time.Now().UTC(), Type: "observation", Content: "fine"}); err != nil {
		t.Fatalf("RememberEvent: %v", err)
	}
	if _, err := mem.UnretractEvent(ctx, "ev_missing", ""); !errors.Is(err, mnemos.ErrEventNotFound) {
		t.Errorf("unknown event: err = %v, want ErrEventNotFound", err)
	}
	if _, err := mem.UnretractEvent(ctx, "ev_fine", ""); !errors.Is(err, mnemos.ErrNotRetracted) {
		t.Errorf("never retracted: err = %v, want ErrNotRetracted", err)
	}
	if _, err := mem.UnretractEvent(ctx, "  ", ""); err == nil {
		t.Error("empty id succeeded")
	}
}
