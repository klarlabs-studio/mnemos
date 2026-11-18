package govwrite_test

import (
	"context"
	"errors"
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
)

// The expectation loop through the governed writer: each step is checked
// before dispatch (so its sentinel reaches the caller), returns what the store
// holds, and records a governed session with its evidence.
func TestWriter_ExpectationLoop(t *testing.T) {
	w, conn := newSQLiteWriter(t)
	ctx := context.Background()
	seedClaim(t, w, "cl_pred")

	if _, err := w.SetExpectation(ctx, govwrite.ExpectationRequest{ClaimID: "cl_missing", Predicted: 1}); !errors.Is(err, govwrite.ErrBeliefNotFound) {
		t.Fatalf("unknown belief: %v, want ErrBeliefNotFound", err)
	}
	if _, err := w.SetExpectation(ctx, govwrite.ExpectationRequest{}); !errors.Is(err, govwrite.ErrBeliefIDRequired) {
		t.Fatalf("no belief id: %v, want ErrBeliefIDRequired", err)
	}
	if _, err := w.ObserveExpectation(ctx, "cl_pred", 1); !errors.Is(err, govwrite.ErrExpectationNotFound) {
		t.Fatalf("observe with none set: %v, want ErrExpectationNotFound", err)
	}

	set, err := w.SetExpectation(ctx, govwrite.ExpectationRequest{ClaimID: "cl_pred", Predicted: 100, Tolerance: -5})
	if err != nil {
		t.Fatalf("SetExpectation: %v", err)
	}
	if set.Tolerance != 5 || set.HasObservation {
		t.Errorf("set = %+v, want tolerance 5 and no observation", set)
	}
	assertExpectationEvidence(t, w, "mnemos.write.set_expectation")

	obs, err := w.ObserveExpectation(ctx, "cl_pred", 120)
	if err != nil {
		t.Fatalf("ObserveExpectation: %v", err)
	}
	if !obs.HasObservation || obs.Observed != 120 {
		t.Errorf("observed = %+v", obs)
	}
	assertExpectationEvidence(t, w, "mnemos.write.observe_expectation")

	recs, err := w.ReconcileExpectations(ctx)
	if err != nil {
		t.Fatalf("ReconcileExpectations: %v", err)
	}
	if len(recs) != 1 || recs[0].ClaimID != "cl_pred" || recs[0].Confirmed {
		t.Fatalf("reconciliations = %+v, want cl_pred refuted", recs)
	}
	assertExpectationEvidence(t, w, "mnemos.write.reconcile_expectations")
	stored, ok, err := conn.Expectations.Get(ctx, "cl_pred")
	if err != nil || !ok || !stored.Resolved {
		t.Errorf("stored = %+v ok=%v err=%v, want resolved", stored, ok, err)
	}
	edges, err := conn.EntityRels.ListByEntity(ctx, "cl_pred", string(domain.RelEntityClaim))
	if err != nil {
		t.Fatalf("edges: %v", err)
	}
	refuted := false
	for _, e := range edges {
		refuted = refuted || e.Kind == domain.RelationshipTypeRefutes
	}
	if !refuted {
		t.Errorf("no refutes edge on cl_pred: %+v", edges)
	}
}

func assertExpectationEvidence(t *testing.T, w *govwrite.Writer, kind string) {
	t.Helper()
	s := w.LastSession()
	if s == nil {
		t.Fatalf("%s: no governed session", kind)
	}
	for _, r := range s.Evidence() {
		if r.Kind == kind {
			return
		}
	}
	t.Errorf("session evidence lacks %s: %+v", kind, s.Evidence())
}
