package mnemos_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/mnemos"
)

func newRetractMemory(t *testing.T) mnemos.Memory {
	t.Helper()
	clearMnemosEnv(t)
	mem, err := mnemos.New(
		mnemos.WithSQLite(filepath.Join(t.TempDir(), "retract.db")),
		mnemos.WithPassiveMode(),
		mnemos.WithActor("lib-operator"),
	)
	if err != nil {
		t.Fatalf("New sqlite: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	return mem
}

// RetractEvent retracts a claim the event alone supported, rescores a claim
// with other evidence, records the reason and actor, and is idempotent.
func TestRetractEvent_RetractsAndRescores(t *testing.T) {
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

	rep, err := mem.RetractEvent(ctx, " ev_retract ", "  wrong runbook ")
	if err != nil {
		t.Fatalf("RetractEvent: %v", err)
	}
	if rep.EventID != "ev_retract" || rep.AlreadyRetracted {
		t.Fatalf("report = %+v, want a first retraction of ev_retract", rep)
	}
	if rep.Reason != "wrong runbook" || rep.RetractedBy != "lib-operator" {
		t.Errorf("reason/actor = %q/%q, want the trimmed reason and the Memory's actor", rep.Reason, rep.RetractedBy)
	}
	if got := rep.Impact.RetractedClaims; len(got) != 1 || got[0] != only {
		t.Errorf("retracted claims = %v, want [%s]", got, only)
	}
	if got := rep.Impact.Rescored; len(got) != 1 || got[0].ID != shared || got[0].RemainingEvidence != 1 {
		t.Errorf("rescored = %+v, want %s kept on one remaining event", got, shared)
	}
	if len(rep.Impact.Uncertainty) == 0 {
		t.Error("report carries no uncertainty statements")
	}
	if mem.LastWriteSession() == nil {
		t.Error("retraction left no governed write session")
	}

	again, err := mem.RetractEvent(ctx, "ev_retract", "again")
	if err != nil {
		t.Fatalf("repeat RetractEvent: %v", err)
	}
	if !again.AlreadyRetracted {
		t.Error("repeat retraction not reported as already retracted")
	}
	if again.Reason != "wrong runbook" || !again.RetractedAt.Equal(rep.RetractedAt) {
		t.Errorf("repeat returned %+v, want the stored first record", again)
	}
}

func TestRetractEvent_UnknownAndEmptyEvents(t *testing.T) {
	mem := newRetractMemory(t)
	ctx := context.Background()
	if _, err := mem.RetractEvent(ctx, "ev_missing", ""); !errors.Is(err, mnemos.ErrEventNotFound) {
		t.Errorf("unknown event: err = %v, want ErrEventNotFound", err)
	}
	if _, err := mem.RetractEvent(ctx, "  ", ""); err == nil || errors.Is(err, mnemos.ErrEventNotFound) {
		t.Errorf("empty id: err = %v, want a required-id error", err)
	}
}
