package govwrite_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/trust"
)

// trustOf returns the stored trust of id, and what the scorer computes from
// its current inputs: equal when the edit rescored it.
func trustOf(t *testing.T, w *govwrite.Writer, id string) (stored, current float64) {
	t.Helper()
	ctx := context.Background()
	got, err := w.Conn().Claims.ListByIDs(ctx, []string{id})
	if err != nil || len(got) != 1 {
		t.Fatalf("read %s: %v", id, err)
	}
	inputs, err := w.Conn().Claims.(ports.TrustInputLister).ListTrustInputs(ctx, []string{id})
	if err != nil {
		t.Fatalf("trust inputs of %s: %v", id, err)
	}
	return got[0].TrustScore, trust.At(inputs[id], time.Now())
}

func evidenceCount(t *testing.T, w *govwrite.Writer, id string) int {
	t.Helper()
	inputs, err := w.Conn().Claims.(ports.TrustInputLister).ListTrustInputs(context.Background(), []string{id})
	if err != nil {
		t.Fatalf("trust inputs of %s: %v", id, err)
	}
	return inputs[id].EvidenceCount
}

// ADR 0038, editing dependencies: removing the blog's detected citation of the
// advisory makes the blog independent again. Its beliefs are rescored (the
// copy counts as a second author once more), the tombstone survives
// re-detection, and retracting the advisory afterwards no longer withdraws
// the belief only the blog kept alive, nor reaches the digest derived from it.
func TestRemoveEventDependency_RestoresIndependence(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()
	seedDependenceFixture(t, w)

	if got, want := evidenceCount(t, w, "cl_false"), domain.EffectiveEvidenceCount(1, 2); got != want {
		t.Fatalf("cl_false evidence count before = %d, want %d (the blog is a copy)", got, want)
	}
	change, err := w.RemoveEventDependency(ctx, "ev_blog", "ev_adv", "dave", "")
	if err != nil {
		t.Fatalf("RemoveEventDependency: %v", err)
	}
	d := change.Dependency
	if d.EventID != "ev_blog" || d.DependsOn != "ev_adv" || d.RemovedAt == nil || d.RemovedBy != "dave" || d.Signal != domain.SignalNamedSource {
		t.Errorf("removed row = %+v", d)
	}
	if fmt.Sprint(change.RescoredBeliefs) != "[cl_false cl_mixed]" || change.SourceRetracted {
		t.Errorf("change = rescored %v, source retracted %v; want [cl_false cl_mixed], false", change.RescoredBeliefs, change.SourceRetracted)
	}
	if got, want := evidenceCount(t, w, "cl_false"), domain.EffectiveEvidenceCount(2, 2); got != want {
		t.Errorf("cl_false evidence count after the removal = %d, want %d (two independent authors)", got, want)
	}
	if stored, current := trustOf(t, w, "cl_false"); stored == 0 || stored-current > 1e-6 || current-stored > 1e-6 {
		t.Errorf("stored trust of cl_false = %v, want the rescored %v", stored, current)
	}

	// Detection running again over the blog does not bring the dependency back.
	blog, err := w.Conn().Events.ListByIDs(ctx, []string{"ev_blog"})
	if err != nil || len(blog) != 1 {
		t.Fatalf("read blog: %v", err)
	}
	if _, err := w.Events(ctx, blog); err != nil {
		t.Fatalf("re-write blog: %v", err)
	}
	if live, err := w.Conn().Dependencies.ListByEventIDs(ctx, []string{"ev_blog"}); err != nil || len(live) != 0 {
		t.Fatalf("live dependencies of the blog after re-detection = %+v (%v), want none", live, err)
	}

	if _, err := w.RetractEvent(ctx, "ev_adv", domain.ChangeNote{Reason: "advisory withdrawn", ChangedBy: "carol"}); err != nil {
		t.Fatalf("RetractEvent: %v", err)
	}
	got := statuses(t, w, "cl_false", "cl_digest", "cl_date", "cl_mixed")
	for id, want := range map[string]domain.ClaimStatus{
		"cl_false": domain.ClaimStatusActive, "cl_digest": domain.ClaimStatusActive,
		"cl_date": domain.ClaimStatusActive, "cl_mixed": domain.ClaimStatusActive,
	} {
		if got[id] != want {
			t.Errorf("%s status = %s, want %s", id, got[id], want)
		}
	}

	// Reported, not acted on: the source is retracted now.
	again, err := w.DeclareEventDependency(ctx, domain.EventDependency{EventID: "ev_blog", DependsOn: "ev_adv", Kind: domain.DependencyCopies,
		Reference: "it is a repost after all", CreatedBy: "dave"})
	if err != nil {
		t.Fatalf("DeclareEventDependency (revive): %v", err)
	}
	if !again.SourceRetracted || again.Dependency.Signal != domain.SignalDeclared || again.Dependency.Kind != domain.DependencyCopies ||
		again.Dependency.RemovedAt != nil || again.Dependency.CreatedBy != "dave" {
		t.Errorf("revived change = %+v", again)
	}
	if s := statuses(t, w, "cl_false")["cl_false"]; s != domain.ClaimStatusActive {
		t.Errorf("cl_false status after the revival = %s, want active: an edit never runs a retraction", s)
	}
}

// Declaring a dependency detection missed makes the release notes a copy of
// the advisory: their belief is rescored as single-sourced, and retracting the
// advisory now withdraws it.
func TestDeclareEventDependency_ReachesRetraction(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()
	seedDependenceFixture(t, w)

	if got, want := evidenceCount(t, w, "cl_date"), domain.EffectiveEvidenceCount(2, 2); got != want {
		t.Fatalf("cl_date evidence count before = %d, want %d", got, want)
	}
	change, err := w.DeclareEventDependency(ctx, domain.EventDependency{EventID: "ev_notes", DependsOn: "ev_adv",
		Kind: domain.DependencyDerivedFrom, Reference: "notes paraphrase the advisory", CreatedBy: "erin"})
	if err != nil {
		t.Fatalf("DeclareEventDependency: %v", err)
	}
	d := change.Dependency
	if d.Signal != domain.SignalDeclared || d.Reference != "notes paraphrase the advisory" || d.CreatedBy != "erin" || d.CreatedAt.IsZero() {
		t.Errorf("declared row = %+v", d)
	}
	if fmt.Sprint(change.RescoredBeliefs) != "[cl_date cl_mixed]" {
		t.Errorf("rescored = %v, want [cl_date cl_mixed]", change.RescoredBeliefs)
	}
	if got, want := evidenceCount(t, w, "cl_date"), domain.EffectiveEvidenceCount(1, 2); got != want {
		t.Errorf("cl_date evidence count after declaring = %d, want %d (the notes are a copy)", got, want)
	}
	if stored, current := trustOf(t, w, "cl_date"); stored == 0 || stored-current > 1e-6 || current-stored > 1e-6 {
		t.Errorf("stored trust of cl_date = %v, want the rescored %v", stored, current)
	}
	if _, err := w.DeclareEventDependency(ctx, domain.EventDependency{EventID: "ev_notes", DependsOn: "ev_adv", Kind: domain.DependencyCites}); err == nil {
		t.Error("a second declaration of a live pair was accepted")
	}

	if _, err := w.RetractEvent(ctx, "ev_adv", domain.ChangeNote{Reason: "advisory withdrawn", ChangedBy: "carol"}); err != nil {
		t.Fatalf("RetractEvent: %v", err)
	}
	if s := statuses(t, w, "cl_date")["cl_date"]; s != domain.ClaimStatusDeprecated {
		t.Errorf("cl_date status = %s, want deprecated: its only other evidence now depends on the advisory", s)
	}
}

func TestRemoveEventDependency_MissingIsAnError(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	seedDependenceFixture(t, w)
	if _, err := govwrite.ApplyDependencyRemoval(context.Background(), w.Conn(), "ev_notes", "ev_adv", "x", ""); err == nil {
		t.Fatal("removing a dependency that was never recorded succeeded")
	}
}

// The governed write itself reports a conflict with its type, not as text:
// declaring a live pair or removing a removed one straight through the
// writer, with no check in front of it (the way a concurrent edit reaches
// it), returns an error matching the store's sentinel.
func TestDependencyEdits_GovernedConflictsKeepTheirType(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()
	seedDependenceFixture(t, w)
	dep := domain.EventDependency{EventID: "ev_blog", DependsOn: "ev_adv", Kind: domain.DependencyCopies, CreatedBy: "erin"}

	if _, err := w.DeclareEventDependency(ctx, dep); !errors.Is(err, domain.ErrDependencyExists) {
		t.Errorf("declaring a live pair: err = %v, want ErrDependencyExists", err)
	}
	if _, err := w.RemoveEventDependency(ctx, "ev_blog", "ev_adv", "dave", ""); err != nil {
		t.Fatalf("RemoveEventDependency: %v", err)
	}
	if _, err := w.RemoveEventDependency(ctx, "ev_blog", "ev_adv", "dave", ""); !errors.Is(err, domain.ErrDependencyNotFound) {
		t.Errorf("removing a removed pair: err = %v, want ErrDependencyNotFound", err)
	}
	if _, err := w.DeclareEventDependency(ctx, dep); err != nil {
		t.Fatalf("revive: %v", err)
	}
	if _, err := w.DeclareEventDependency(ctx, dep); !errors.Is(err, domain.ErrDependencyExists) {
		t.Errorf("declaring the revived pair: err = %v, want ErrDependencyExists", err)
	}
}
