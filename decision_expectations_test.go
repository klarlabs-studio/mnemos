package mnemos

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
)

// revisionMem is a throwaway SQLite brain: revision reads status history,
// which every backend must keep, and SQLite is the one shipped by default.
func revisionMem(t *testing.T) *memory {
	t.Helper()
	for _, k := range []string{"MNEMOS_STORAGE", "MNEMOS_MODE", "MNEMOS_LLM_PROVIDER", "MNEMOS_API_KEY"} {
		t.Setenv(k, "")
	}
	mem, err := New(WithStorage("sqlite://"+filepath.Join(t.TempDir(), "brain.db")), WithPassiveMode())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	return mem.(*memory)
}

func seedBelief(t *testing.T, m *memory, id, text string, created time.Time) {
	t.Helper()
	if err := m.conn.Claims.Upsert(context.Background(), []domain.Claim{{
		ID: id, Text: text, Type: domain.ClaimTypeFact, Confidence: 0.8,
		Status: domain.ClaimStatusActive, CreatedAt: created, ValidFrom: created,
	}}); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func beliefState(t *testing.T, m *memory, id string) domain.Claim {
	t.Helper()
	got, err := m.conn.Claims.ListByIDs(context.Background(), []string{id})
	if err != nil || len(got) != 1 {
		t.Fatalf("read %s: %v (%d)", id, err, len(got))
	}
	return got[0]
}

func TestRecordDecision_ExpectationValidation(t *testing.T) {
	m := revisionMem(t)
	ctx := context.Background()
	seedBelief(t, m, "cl_a", "the payments timeout is 30 seconds", time.Now().UTC())

	tests := []struct {
		name string
		d    Decision
		want error
	}{
		{"rests on nothing", Decision{Statement: "deploy", Expectation: &ExpectedOutcome{Predicted: 1}}, govwrite.ErrExpectationRestsOnNothing},
		{"unknown belief", Decision{Statement: "deploy", Beliefs: []string{"cl_a", "cl_x"}, Expectation: &ExpectedOutcome{Predicted: 1}}, govwrite.ErrBeliefNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := m.RecordDecision(ctx, tt.d); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
	// A refused expectation refuses the decision too: nothing was written.
	all, err := m.ListDecisions(ctx, 0)
	if err != nil || len(all) != 0 {
		t.Fatalf("decisions after refusals = %d (%v), want 0", len(all), err)
	}
	if err := m.RecordDecisionObservation(ctx, "dec_none", 1); !errors.Is(err, ErrExpectationNotFound) {
		t.Fatalf("observe without expectation: %v, want ErrExpectationNotFound", err)
	}
}

// The full loop on the library surface: a decision records its expected
// outcome, the outcome surprises, the surprise is attributed to the rested-on
// beliefs (PredictiveError, credit), and revision supersedes the belief a
// newer belief contradicts while leaving its partner, and every belief that
// was only recalled, alone.
func TestDecisionExpectation_SurpriseAttributedAndRevised(t *testing.T) {
	m := revisionMem(t)
	ctx := context.Background()
	old := time.Now().UTC().Add(-48 * time.Hour)
	seedBelief(t, m, "cl_stale", "the payments timeout is 30 seconds", old)
	seedBelief(t, m, "cl_stable", "payment retries are idempotent", old)
	seedBelief(t, m, "cl_recalled", "the ledger runs on PostgreSQL", old)
	seedBelief(t, m, "cl_new", "the payments timeout is 5 seconds", time.Now().UTC().Add(-time.Hour))
	edge(t, m, "rel_c", "cl_new", "cl_stale", domain.RelationshipTypeContradicts)

	// Recall alone never revises.
	if _, err := m.Recall(ctx, Query{Text: "what runs the ledger and what is the payments timeout", Limit: 10, AgentConsumer: true}); err != nil {
		t.Fatalf("Recall: %v", err)
	}

	id, err := m.RecordDecision(ctx, Decision{
		Statement: "raise the retry budget", Beliefs: []string{"cl_stale", "cl_stable"},
		Expectation: &ExpectedOutcome{Predicted: 200, Tolerance: 50},
	})
	if err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}
	if err := m.RecordDecisionObservation(ctx, id, 900); err != nil {
		t.Fatalf("RecordDecisionObservation: %v", err)
	}

	pe, err := m.PredictiveError(ctx)
	if err != nil {
		t.Fatalf("PredictiveError: %v", err)
	}
	if pe.Levels[0].Level != "outcome" || pe.Levels[0].Samples != 1 || pe.Levels[0].Error != 1 {
		t.Errorf("outcome level = %+v, want one saturated sample", pe.Levels[0])
	}
	gotAttr := map[string]AttributedSurprise{}
	for _, a := range pe.Attributed {
		gotAttr[a.ClaimID] = a
	}
	for _, b := range []string{"cl_stale", "cl_stable"} {
		if a := gotAttr[b]; a.Predictions != 1 || a.Refuted != 1 || a.Surprise != 14 {
			t.Errorf("attributed[%s] = %+v, want one refuted prediction of surprise 14", b, a)
		}
	}
	if _, ok := gotAttr["cl_recalled"]; ok || len(pe.Attributed) != 2 {
		t.Errorf("attributed = %+v, want only the rested-on beliefs", pe.Attributed)
	}

	recs, err := m.ReconcileExpectations(ctx)
	if err != nil {
		t.Fatalf("ReconcileExpectations: %v", err)
	}
	if len(recs) != 1 || recs[0].DecisionID != id || recs[0].Confirmed {
		t.Fatalf("recs = %+v, want the decision refuted", recs)
	}
	if len(recs[0].Revisions) != 1 || recs[0].Revisions[0] != (BeliefRevision{ClaimID: "cl_stale", Kind: "supersede", By: "cl_new"}) {
		t.Fatalf("revisions = %+v, want only cl_stale superseded by cl_new", recs[0].Revisions)
	}
	if beliefState(t, m, "cl_stale").IsCurrent() {
		t.Error("cl_stale still current after revision")
	}
	for _, b := range []string{"cl_stable", "cl_recalled", "cl_new"} {
		if c := beliefState(t, m, b); !c.IsCurrent() || c.Status != domain.ClaimStatusActive {
			t.Errorf("%s = status %s current %v, want untouched", b, c.Status, c.IsCurrent())
		}
	}

	// The surprise reaches trust through credit, and ForgetRefuted does not
	// read a decision's failure as a refutation of each belief it rested on.
	res, err := m.Consolidate(ctx, ConsolidateOptions{AssignCredit: true, ForgetRefuted: true})
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	if res.Refuted != 0 {
		t.Errorf("ForgetRefuted archived %d belief(s) on a decision-level surprise", res.Refuted)
	}
	if res.Credited == 0 {
		t.Error("credit assignment ignored the decision's expected outcome")
	}
	stable := beliefState(t, m, "cl_stable")
	if v := stable.ConfidenceComponents[domain.CreditComponentPrefix+id+":outcome"]; v >= 0 {
		t.Errorf("cl_stable credit for the surprised decision = %v, want blame", v)
	}
}

// A confirmed decision leaves its beliefs alone and credits them.
func TestDecisionExpectation_ConfirmedLeavesBeliefsAlone(t *testing.T) {
	m := revisionMem(t)
	ctx := context.Background()
	seedBelief(t, m, "cl_a", "the payments timeout is 30 seconds", time.Now().UTC().Add(-time.Hour))
	id, err := m.RecordDecision(ctx, Decision{
		Statement: "keep the timeout", Beliefs: []string{"cl_a"},
		Expectation: &ExpectedOutcome{Predicted: 200, Tolerance: 50},
	})
	if err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}
	if err := m.RecordDecisionObservation(ctx, id, 210); err != nil {
		t.Fatalf("observe: %v", err)
	}
	recs, err := m.ReconcileExpectations(ctx)
	if err != nil || len(recs) != 1 || !recs[0].Confirmed || len(recs[0].Revisions) != 0 {
		t.Fatalf("recs = %+v (%v), want one confirmed with no revision", recs, err)
	}
	if c := beliefState(t, m, "cl_a"); c.Status != domain.ClaimStatusActive || !c.IsCurrent() {
		t.Errorf("confirmed belief changed: status %s current %v", c.Status, c.IsCurrent())
	}
}
