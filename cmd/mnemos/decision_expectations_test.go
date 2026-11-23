package main

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"go.klarlabs.de/mnemos/client"
	"go.klarlabs.de/mnemos/internal/domain"
)

// A decision recorded with an expected outcome (ADR 0049) over REST + client:
// a refused expectation refuses the decision, the outcome is observed at
// /v1/decisions/{id}/observation, and reconciliation names the decision, the
// beliefs its surprise is attributed to and what revision did to them. MCP
// record_observation observes a second decision through the same function.
func TestDecisionExpectation_ClientAndMCP(t *testing.T) {
	f := newBindingFixture(t)
	f.seedBelief(t, "cl-timeout", "the payments timeout is 30 seconds")
	f.seedBelief(t, "cl-retry", "payment retries are idempotent")
	ctx := context.Background()
	w := f.client(t, domain.ScopeClaimsWrite)
	var apiErr *client.APIError

	for _, tt := range []struct {
		name string
		req  client.RecordDecisionRequest
	}{
		{"rests on nothing", client.RecordDecisionRequest{Statement: "s", RiskLevel: "low", Expectation: &client.ExpectedOutcome{Predicted: 1}}},
		{"unknown belief", client.RecordDecisionRequest{Statement: "s", RiskLevel: "low", Beliefs: []string{"cl-missing"}, Expectation: &client.ExpectedOutcome{Predicted: 1}}},
		{"bad horizon", client.RecordDecisionRequest{Statement: "s", RiskLevel: "low", Beliefs: []string{"cl-retry"}, Expectation: &client.ExpectedOutcome{Predicted: 1, Horizon: "soon"}}},
	} {
		if _, err := w.RecordDecision(ctx, tt.req); !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
			t.Errorf("%s: %v, want 400", tt.name, err)
		}
	}
	if ds, err := f.conn.Decisions.ListAll(ctx); err != nil || len(ds) != 0 {
		t.Fatalf("decisions after refusals = %d (%v), want 0", len(ds), err)
	}

	d, err := w.RecordDecision(ctx, client.RecordDecisionRequest{
		Statement: "raise the retry budget", RiskLevel: "medium", Beliefs: []string{"cl-timeout", "cl-retry"},
		Expectation: &client.ExpectedOutcome{Predicted: 200, Tolerance: 50},
	})
	if err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}
	if _, ok, _ := f.conn.Expectations.Get(ctx, domain.DecisionExpectationKey(d.ID)); !ok {
		t.Fatalf("expectation for %s not stored", d.ID)
	}
	if _, err := w.RecordDecisionObservation(ctx, "dc-missing", 1); !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Errorf("observe with no expectation: %v, want 404", err)
	}
	if _, err := f.client(t, "claims:read").RecordDecisionObservation(ctx, d.ID, 1); !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
		t.Errorf("claims:read observation: %v, want 403", err)
	}
	obs, err := w.RecordDecisionObservation(ctx, d.ID, 900)
	if err != nil || obs.DecisionID != d.ID || !obs.HasObservation || obs.Observed != 900 {
		t.Fatalf("RecordDecisionObservation = %+v, %v", obs, err)
	}

	pe, err := f.client(t, "claims:read").PredictiveError(ctx)
	if err != nil || len(pe.Attributed) != 2 {
		t.Fatalf("predictive error attributed = %+v (%v), want both beliefs", pe, err)
	}

	recs, err := w.ReconcileExpectations(ctx)
	if err != nil {
		t.Fatalf("ReconcileExpectations: %v", err)
	}
	if len(recs) != 1 || recs[0].DecisionID != d.ID || recs[0].Confirmed || len(recs[0].AttributedTo) != 2 || len(recs[0].Revisions) != 2 {
		t.Fatalf("reconciliations = %+v, want the decision refuted, attributed and both beliefs flagged", recs)
	}

	// MCP: exactly one of claimId / decisionId.
	if _, err := mcpRunRecordObservation(ctx, mcpRecordObservationInput{ClaimID: "cl-retry", DecisionID: d.ID, Observed: 1}); err == nil {
		t.Error("record_observation with both ids: want an error")
	}
	if _, err := mcpRunRecordObservation(ctx, mcpRecordObservationInput{Observed: 1}); err == nil {
		t.Error("record_observation with neither id: want an error")
	}
	out, err := mcpRunRecordDecision(ctx, "tester", mcpRecordDecisionInput{
		Statement: "keep the budget", RiskLevel: "low", Beliefs: []string{"cl-retry"},
		Expectation: &decisionExpectationInput{Predicted: 10, Tolerance: 1},
	})
	if err != nil || !out.HasExpectation {
		t.Fatalf("record_decision with expectation = %+v, %v", out, err)
	}
	mo, err := mcpRunRecordObservation(ctx, mcpRecordObservationInput{DecisionID: out.ID, Observed: 10.5})
	if err != nil || mo.DecisionID != out.ID || !mo.Validates {
		t.Fatalf("record_observation decisionId = %+v, %v; want validated", mo, err)
	}
	rec, err := mcpReconcileExpectations(ctx)
	if err != nil || len(rec.Reconciliations) != 1 || rec.Reconciliations[0].DecisionID != out.ID || !rec.Reconciliations[0].Confirmed {
		t.Fatalf("MCP reconcile = %+v, %v; want the second decision confirmed", rec, err)
	}
}
