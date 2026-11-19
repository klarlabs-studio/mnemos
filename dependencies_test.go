package mnemos_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos"
)

// The library declares, lists and removes event dependencies through the same
// checks and writes as the daemon surfaces (ADR 0038), recording the
// Memory's actor and rescoring the dependent event's claims.
func TestEventDependencies_DeclareListRemove(t *testing.T) {
	mem := newRetractMemory(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, id := range []string{"ev_src", "ev_copy"} {
		if err := mem.RememberEvent(ctx, mnemos.Event{ID: id, At: now, Type: "observation", Content: "source " + id}); err != nil {
			t.Fatalf("RememberEvent %s: %v", id, err)
		}
	}
	claim, err := mem.RememberClaim(ctx, mnemos.ClaimItem{Text: "The queue drains hourly.", EventIDs: []string{"ev_src", "ev_copy"}, ValidFrom: now})
	if err != nil {
		t.Fatalf("RememberClaim: %v", err)
	}

	got, err := mem.DeclareEventDependency(ctx, "ev_copy", "ev_src", "copies", "a repost")
	if err != nil {
		t.Fatalf("DeclareEventDependency: %v", err)
	}
	if d := got.Dependency; d.Signal != "declared" || d.Kind != "copies" || d.CreatedBy != "lib-operator" || d.Reference != "a repost" || d.RemovedAt != nil {
		t.Errorf("declared = %+v", d)
	}
	if len(got.RescoredClaims) != 1 || got.RescoredClaims[0] != claim || got.SourceRetracted {
		t.Errorf("change = %+v, want %s rescored", got, claim)
	}
	if _, err := mem.DeclareEventDependency(ctx, "ev_copy", "ev_src", "cites", ""); !errors.Is(err, mnemos.ErrDependencyExists) {
		t.Errorf("second declaration: err = %v, want ErrDependencyExists", err)
	}
	if _, err := mem.DeclareEventDependency(ctx, "ev_copy", "ev_src", "paraphrases", ""); !errors.Is(err, mnemos.ErrInvalidDependency) {
		t.Errorf("bad kind: err = %v, want ErrInvalidDependency", err)
	}
	if _, err := mem.DeclareEventDependency(ctx, "ev_missing", "ev_src", "cites", ""); !errors.Is(err, mnemos.ErrEventNotFound) {
		t.Errorf("unknown event: err = %v, want ErrEventNotFound", err)
	}

	list, err := mem.EventDependencies(ctx, "ev_src", false)
	if err != nil || len(list.Dependents) != 1 || list.Dependents[0].EventID != "ev_copy" || len(list.DependsOn) != 0 {
		t.Fatalf("EventDependencies = %+v (%v)", list, err)
	}

	if _, err := mem.RemoveEventDependency(ctx, "ev_copy", "ev_src", strings.Repeat("x", 513)); !errors.Is(err, mnemos.ErrInvalidDependency) {
		t.Errorf("over-long reason: err = %v, want ErrInvalidDependency", err)
	}
	removed, err := mem.RemoveEventDependency(ctx, "ev_copy", "ev_src", "only the headline matches")
	if err != nil || removed.Dependency.RemovedAt == nil || removed.Dependency.RemovedBy != "lib-operator" || removed.Dependency.RemovalReason != "only the headline matches" {
		t.Fatalf("RemoveEventDependency = %+v (%v)", removed, err)
	}
	if _, err := mem.RemoveEventDependency(ctx, "ev_copy", "ev_src", ""); !errors.Is(err, mnemos.ErrDependencyNotFound) {
		t.Errorf("second removal: err = %v, want ErrDependencyNotFound", err)
	}
	if live, err := mem.EventDependencies(ctx, "ev_src", false); err != nil || len(live.Dependents) != 0 {
		t.Errorf("live after removal = %+v (%v)", live, err)
	}
	if all, err := mem.EventDependencies(ctx, "ev_src", true); err != nil || len(all.Dependents) != 1 || all.Dependents[0].RemovedAt == nil {
		t.Errorf("with removed = %+v (%v)", all, err)
	}
	// Declared again: the removal stays on record as its own interval.
	if _, err := mem.DeclareEventDependency(ctx, "ev_copy", "ev_src", "cites", ""); err != nil {
		t.Fatalf("revive: %v", err)
	}
	all, err := mem.EventDependencies(ctx, "ev_src", true)
	if err != nil || len(all.Dependents) != 2 || all.Dependents[0].RemovalReason != "only the headline matches" || all.Dependents[1].RemovedAt != nil {
		t.Errorf("with removed after revival = %+v (%v)", all, err)
	}
}
