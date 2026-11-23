package grpc_test

import (
	"context"
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
	mnemosv1 "go.klarlabs.de/mnemos/proto/gen/mnemos/v1"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A decision recorded with an expected outcome (ADR 0049) over gRPC: the
// expectation is checked before the decision is written, observed with
// RecordDecisionObservation, and reconciliation reports the decision, the
// beliefs its surprise is attributed to and the revision it caused.
func TestGRPC_DecisionExpectationLoop(t *testing.T) {
	client, conn, mem, issuer := startSharedBrainServer(t)
	seedTwoRuns(t, conn)
	ctx := bearerCtx(t, mintToken(t, issuer, []string{domain.ScopeClaimsWrite}, nil))

	bad := []struct {
		name string
		d    *mnemosv1.Decision
		want codes.Code
	}{
		{"rests on nothing", &mnemosv1.Decision{Id: "dc-none", Statement: "s", RiskLevel: "low", Expectation: &mnemosv1.DecisionExpectation{Predicted: 1}}, codes.InvalidArgument},
		{"unknown belief", &mnemosv1.Decision{Id: "dc-unknown", Statement: "s", RiskLevel: "low", Beliefs: []string{"cl-missing"}, Expectation: &mnemosv1.DecisionExpectation{Predicted: 1}}, codes.NotFound},
	}
	for _, tt := range bad {
		if _, err := client.AppendDecisions(ctx, &mnemosv1.AppendDecisionsRequest{Decisions: []*mnemosv1.Decision{tt.d}}); status.Code(err) != tt.want {
			t.Errorf("%s: %v, want %s", tt.name, err, tt.want)
		}
		if _, err := conn.Decisions.GetByID(context.Background(), tt.d.Id); err == nil {
			t.Errorf("%s: decision written despite a refused expectation", tt.name)
		}
	}

	if _, err := client.AppendDecisions(ctx, &mnemosv1.AppendDecisionsRequest{Decisions: []*mnemosv1.Decision{{
		Id: "dc-1", Statement: "raise the budget", RiskLevel: "medium", Beliefs: []string{"cl-mine", "cl-mine-2"},
		Expectation: &mnemosv1.DecisionExpectation{Predicted: 200, Tolerance: 50},
	}}}); err != nil {
		t.Fatalf("AppendDecisions: %v", err)
	}
	if _, err := client.RecordDecisionObservation(ctx, &mnemosv1.RecordDecisionObservationRequest{DecisionId: "dc-missing", Observed: 1}); status.Code(err) != codes.NotFound {
		t.Errorf("observe a decision with no expectation: %v, want NotFound", err)
	}
	obs, err := client.RecordDecisionObservation(ctx, &mnemosv1.RecordDecisionObservationRequest{DecisionId: "dc-1", Observed: 900})
	if err != nil || !obs.GetHasObservation() || obs.GetObserved() != 900 {
		t.Fatalf("RecordDecisionObservation = %+v, %v", obs, err)
	}

	pe, err := client.PredictiveError(ctx, &mnemosv1.PredictiveErrorRequest{})
	if err != nil {
		t.Fatalf("PredictiveError: %v", err)
	}
	if len(pe.GetAttributed()) != 2 {
		t.Errorf("attributed = %+v, want both rested-on beliefs", pe.GetAttributed())
	}
	lib, err := mem.PredictiveError(context.Background())
	if err != nil || len(lib.Attributed) != len(pe.GetAttributed()) {
		t.Errorf("library attributed %+v (%v) vs gRPC %+v", lib.Attributed, err, pe.GetAttributed())
	}

	rec, err := client.ReconcileExpectations(ctx, &mnemosv1.ReconcileExpectationsRequest{})
	if err != nil {
		t.Fatalf("ReconcileExpectations: %v", err)
	}
	rs := rec.GetReconciliations()
	if len(rs) != 1 || rs[0].GetDecisionId() != "dc-1" || rs[0].GetConfirmed() || len(rs[0].GetAttributedTo()) != 2 {
		t.Fatalf("reconciliations = %+v, want dc-1 refuted, attributed to both beliefs", rs)
	}
	if rv := rs[0].GetRevisions(); len(rv) != 2 || rv[0].GetKind() != "flag" {
		t.Errorf("revisions = %+v, want both beliefs flagged", rv)
	}
}

// A run-scoped token may attach or observe a decision expectation only when
// every rested-on belief lies in its runs.
func TestGRPC_DecisionExpectationRunScope(t *testing.T) {
	client, conn, _, issuer := startSharedBrainServer(t)
	seedTwoRuns(t, conn)
	open := bearerCtx(t, mintToken(t, issuer, []string{domain.ScopeClaimsWrite}, nil))
	scoped := bearerCtx(t, mintToken(t, issuer, []string{domain.ScopeClaimsWrite}, []string{"run-mine"}))

	cross := &mnemosv1.Decision{
		Id: "dc-cross", Statement: "s", RiskLevel: "low", Beliefs: []string{"cl-mine", "cl-theirs"},
		Expectation: &mnemosv1.DecisionExpectation{Predicted: 1},
	}
	if _, err := client.AppendDecisions(scoped, &mnemosv1.AppendDecisionsRequest{Decisions: []*mnemosv1.Decision{cross}}); status.Code(err) != codes.NotFound {
		t.Errorf("run-scoped expectation resting on another run's belief: %v, want NotFound", err)
	}
	// Recorded unscoped, then observed by the run-scoped token: refused.
	if _, err := client.AppendDecisions(open, &mnemosv1.AppendDecisionsRequest{Decisions: []*mnemosv1.Decision{cross}}); err != nil {
		t.Fatalf("unscoped AppendDecisions: %v", err)
	}
	if _, err := client.RecordDecisionObservation(scoped, &mnemosv1.RecordDecisionObservationRequest{DecisionId: "dc-cross", Observed: 1}); status.Code(err) != codes.NotFound {
		t.Errorf("run-scoped observation of a cross-run decision: %v, want NotFound", err)
	}
	mine := &mnemosv1.Decision{
		Id: "dc-mine", Statement: "s", RiskLevel: "low", Beliefs: []string{"cl-mine"},
		Expectation: &mnemosv1.DecisionExpectation{Predicted: 1, Tolerance: 1},
	}
	if _, err := client.AppendDecisions(scoped, &mnemosv1.AppendDecisionsRequest{Decisions: []*mnemosv1.Decision{mine}}); err != nil {
		t.Fatalf("run-scoped AppendDecisions inside its run: %v", err)
	}
	if _, err := client.RecordDecisionObservation(scoped, &mnemosv1.RecordDecisionObservationRequest{DecisionId: "dc-mine", Observed: 1}); err != nil {
		t.Errorf("run-scoped observation inside its run: %v", err)
	}
}
