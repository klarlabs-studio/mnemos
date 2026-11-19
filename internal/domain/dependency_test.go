package domain

import (
	"testing"
	"time"
)

func TestEventDependency_ValidateAcceptsDeclared(t *testing.T) {
	d := EventDependency{EventID: "a", DependsOn: "b", Kind: DependencyCites, Signal: SignalDeclared}
	if err := d.Validate(); err != nil {
		t.Fatalf("declared dependency rejected: %v", err)
	}
	d.Signal = "guessed"
	if err := d.Validate(); err == nil {
		t.Fatal("an unknown signal was accepted")
	}
}

func TestEventDependency_LiveAt(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	removed := created.Add(time.Hour)
	d := EventDependency{CreatedAt: created}
	if d.LiveAt(created.Add(-time.Second)) {
		t.Error("live before it was recorded")
	}
	if !d.LiveAt(created) || !d.Live() {
		t.Error("not live once recorded")
	}
	d.RemovedAt = &removed
	if d.Live() {
		t.Error("a removed dependency reads live")
	}
	if !d.LiveAt(removed.Add(-time.Second)) {
		t.Error("not live before its removal")
	}
	if d.LiveAt(removed) || d.LiveAt(removed.Add(time.Second)) {
		t.Error("live at or after its removal")
	}
}
