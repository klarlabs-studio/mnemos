package govwrite_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/store"
)

// seedClaimAt writes an active claim created at the given instant.
func seedClaimAt(t *testing.T, w *govwrite.Writer, id, text string, created time.Time) {
	t.Helper()
	if _, err := w.Claims(context.Background(), []domain.Claim{{
		ID: id, Text: text, Type: domain.ClaimTypeFact, Confidence: 0.8,
		Status: domain.ClaimStatusActive, CreatedAt: created, ValidFrom: created, CreatedBy: "tester",
	}}, govwrite.ClaimReason{}); err != nil {
		t.Fatalf("seed claim %s: %v", id, err)
	}
}

func seedDecision(t *testing.T, w *govwrite.Writer, id string, beliefs ...string) {
	t.Helper()
	if _, err := w.Decision(context.Background(), domain.Decision{
		ID: id, Statement: "act on " + strings.Join(beliefs, ","), RiskLevel: domain.RiskLevelMedium,
		Beliefs: beliefs, ChosenAt: time.Now().UTC(), CreatedBy: "tester",
	}); err != nil {
		t.Fatalf("seed decision %s: %v", id, err)
	}
}

func expectDecision(t *testing.T, w *govwrite.Writer, id string, beliefs []string, predicted, observed float64) {
	t.Helper()
	ctx := context.Background()
	if _, err := w.SetDecisionExpectation(ctx, govwrite.DecisionExpectationRequest{
		DecisionID: id, RestsOn: beliefs, Predicted: predicted, Tolerance: 1,
	}); err != nil {
		t.Fatalf("SetDecisionExpectation %s: %v", id, err)
	}
	if _, err := w.ObserveDecision(ctx, id, observed); err != nil {
		t.Fatalf("ObserveDecision %s: %v", id, err)
	}
}

func claimState(t *testing.T, conn *store.Conn, id string) domain.Claim {
	t.Helper()
	got, err := conn.Claims.ListByIDs(context.Background(), []string{id})
	if err != nil || len(got) != 1 {
		t.Fatalf("read claim %s: %v (%d rows)", id, err, len(got))
	}
	return got[0]
}

func TestWriter_DecisionExpectationChecks(t *testing.T) {
	w, _ := newSQLiteWriter(t)
	ctx := context.Background()
	seedClaimAt(t, w, "cl_a", "the payments timeout is 30 seconds", time.Now().UTC())

	tests := []struct {
		name string
		req  govwrite.DecisionExpectationRequest
		want error
	}{
		{"no decision", govwrite.DecisionExpectationRequest{RestsOn: []string{"cl_a"}}, govwrite.ErrDecisionIDRequired},
		{"rests on nothing", govwrite.DecisionExpectationRequest{DecisionID: "dc_1"}, govwrite.ErrExpectationRestsOnNothing},
		{"rests on blanks only", govwrite.DecisionExpectationRequest{DecisionID: "dc_1", RestsOn: []string{" ", ""}}, govwrite.ErrExpectationRestsOnNothing},
		{"unknown belief", govwrite.DecisionExpectationRequest{DecisionID: "dc_1", RestsOn: []string{"cl_a", "cl_missing"}}, govwrite.ErrBeliefNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := w.SetDecisionExpectation(ctx, tt.req); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
	if _, err := w.ObserveDecision(ctx, "dc_none", 1); !errors.Is(err, govwrite.ErrExpectationNotFound) {
		t.Fatalf("observe without expectation: %v, want ErrExpectationNotFound", err)
	}
	// The decision namespace is reserved: a belief-keyed call cannot reach it.
	for _, call := range []func() error{
		func() error {
			_, err := w.SetExpectation(ctx, govwrite.ExpectationRequest{ClaimID: domain.DecisionExpectationKey("dc_1"), Predicted: 1})
			return err
		},
		func() error {
			_, err := w.ObserveExpectation(ctx, domain.DecisionExpectationKey("dc_1"), 1)
			return err
		},
	} {
		if err := call(); !errors.Is(err, govwrite.ErrReservedExpectationKey) {
			t.Fatalf("reserved key: %v, want ErrReservedExpectationKey", err)
		}
	}
}

// A decision's surprise is attributed to every belief it rested on, and with
// no contrary evidence each is flagged contested. The verdict edge targets
// the decision, never a belief, so the per-belief verdict consumers
// (ForgetRefuted, calibration) do not read a conjunction's failure as a
// refutation of each conjunct.
func TestReconcile_UnexplainedSurpriseFlagsRestedOnBeliefs(t *testing.T) {
	w, conn := newSQLiteWriter(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)
	seedClaimAt(t, w, "cl_timeout", "the payments timeout is 30 seconds", base)
	seedClaimAt(t, w, "cl_retry", "payment retries are idempotent", base)
	seedClaimAt(t, w, "cl_bystander", "the ledger runs on PostgreSQL", base)
	seedDecision(t, w, "dc_1", "cl_timeout", "cl_retry")
	expectDecision(t, w, "dc_1", []string{"cl_timeout", "cl_retry"}, 200, 900)

	recs, err := w.ReconcileExpectations(ctx)
	if err != nil {
		t.Fatalf("ReconcileExpectations: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("reconciliations = %+v, want one", recs)
	}
	r := recs[0]
	if r.DecisionID != "dc_1" || r.Confirmed || r.Rechecked {
		t.Fatalf("reconciliation = %+v, want dc_1 refuted", r)
	}
	if strings.Join(r.AttributedTo, ",") != "cl_retry,cl_timeout" && strings.Join(r.AttributedTo, ",") != "cl_timeout,cl_retry" {
		t.Fatalf("attributed to %v, want both rested-on beliefs", r.AttributedTo)
	}
	if len(r.Revisions) != 2 {
		t.Fatalf("revisions = %+v, want both flagged", r.Revisions)
	}
	for _, id := range []string{"cl_timeout", "cl_retry"} {
		if s := claimState(t, conn, id).Status; s != domain.ClaimStatusContested {
			t.Errorf("%s status = %s, want contested", id, s)
		}
		hist, err := conn.Claims.ListStatusHistoryByClaimID(ctx, id)
		if err != nil || len(hist) == 0 || !govwrite.IsSurpriseFlag(hist[len(hist)-1]) {
			t.Errorf("%s history = %+v (%v), want a surprise flag last", id, hist, err)
		}
	}
	if s := claimState(t, conn, "cl_bystander").Status; s != domain.ClaimStatusActive {
		t.Errorf("bystander status = %s, want untouched", s)
	}
	for _, id := range []string{"cl_timeout", "cl_retry"} {
		edges, err := conn.EntityRels.ListByEntity(ctx, id, domain.RelEntityClaim)
		if err != nil {
			t.Fatalf("edges: %v", err)
		}
		for _, e := range edges {
			if e.Kind == domain.RelationshipTypeRefutes || e.Kind == domain.RelationshipTypeValidates {
				t.Errorf("%s carries a per-belief verdict edge %+v", id, e)
			}
		}
	}
	edges, err := conn.EntityRels.ListByEntity(ctx, "dc_1", domain.RelEntityDecision)
	if err != nil {
		t.Fatalf("decision edges: %v", err)
	}
	refuted := false
	for _, e := range edges {
		refuted = refuted || (e.Kind == domain.RelationshipTypeRefutes && e.ToID == "dc_1")
	}
	if !refuted {
		t.Errorf("no refutes edge on the decision: %+v", edges)
	}
	assertNoCausalEdges(t, conn)

	// Re-reconciling with nothing new writes nothing and revises nothing.
	again, err := w.ReconcileExpectations(ctx)
	if err != nil || len(again) != 0 {
		t.Fatalf("second pass = %+v, %v; want nothing", again, err)
	}
}

// Confirmation leaves the rested-on beliefs alone.
func TestReconcile_ConfirmedDecisionRevisesNothing(t *testing.T) {
	w, conn := newSQLiteWriter(t)
	ctx := context.Background()
	seedClaimAt(t, w, "cl_a", "the payments timeout is 30 seconds", time.Now().UTC())
	seedDecision(t, w, "dc_ok", "cl_a")
	expectDecision(t, w, "dc_ok", []string{"cl_a"}, 200, 200.5)

	recs, err := w.ReconcileExpectations(ctx)
	if err != nil || len(recs) != 1 || !recs[0].Confirmed || len(recs[0].Revisions) != 0 {
		t.Fatalf("recs = %+v, %v; want one confirmed, no revisions", recs, err)
	}
	if c := claimState(t, conn, "cl_a"); c.Status != domain.ClaimStatusActive || !c.IsCurrent() {
		t.Errorf("confirmed belief changed: %+v", c)
	}
}

// A surprise with no contrary evidence flags; once a newer contradicting
// belief arrives, the next pass re-checks the earlier surprise, supersedes the
// stale belief and clears the flag on its partner (explaining away).
func TestReconcile_LaterEvidenceSupersedesAndExplainsAway(t *testing.T) {
	w, conn := newSQLiteWriter(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-2 * time.Hour)
	seedClaimAt(t, w, "cl_stale", "the payments timeout is 30 seconds", base)
	seedClaimAt(t, w, "cl_stable", "payment retries are idempotent", base)
	seedDecision(t, w, "dc_1", "cl_stale", "cl_stable")
	expectDecision(t, w, "dc_1", []string{"cl_stale", "cl_stable"}, 200, 900)
	if _, err := w.ReconcileExpectations(ctx); err != nil {
		t.Fatalf("first pass: %v", err)
	}

	newer := time.Now().UTC().Add(-time.Hour)
	seedClaimAt(t, w, "cl_new", "the payments timeout is 5 seconds", newer)
	if _, err := w.Relationships(ctx, []domain.Relationship{{
		ID: "rel_1", Type: domain.RelationshipTypeContradicts, FromClaimID: "cl_new", ToClaimID: "cl_stale",
		CreatedAt: newer, CreatedBy: "tester",
	}}); err != nil {
		t.Fatalf("seed contradiction: %v", err)
	}

	recs, err := w.ReconcileExpectations(ctx)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if len(recs) != 1 || !recs[0].Rechecked || recs[0].DecisionID != "dc_1" {
		t.Fatalf("recs = %+v, want dc_1 rechecked", recs)
	}
	kinds := map[string]string{}
	for _, rv := range recs[0].Revisions {
		kinds[rv.ClaimID] = rv.Kind
	}
	if kinds["cl_stale"] != govwrite.RevisionSupersede || kinds["cl_stable"] != govwrite.RevisionClear {
		t.Fatalf("revisions = %+v, want stale superseded, stable cleared", recs[0].Revisions)
	}
	stale := claimState(t, conn, "cl_stale")
	if stale.IsCurrent() || stale.ValidTo.Sub(newer).Abs() > time.Second {
		t.Errorf("stale valid_to = %v, want the successor's valid_from %v", stale.ValidTo, newer)
	}
	if s := claimState(t, conn, "cl_stable").Status; s != domain.ClaimStatusActive {
		t.Errorf("stable status = %s, want cleared to active", s)
	}
	if !claimState(t, conn, "cl_new").IsCurrent() {
		t.Error("the superseding belief was retired")
	}
	assertNoCausalEdges(t, conn)

	if again, err := w.ReconcileExpectations(ctx); err != nil || len(again) != 0 {
		t.Fatalf("third pass = %+v, %v; want nothing", again, err)
	}
}

// Recall never revises: only a reconciled decision expectation does. A
// recalled belief whose decision has an expectation that is still open (no
// observation) is untouched by a reconcile pass.
func TestReconcile_UnobservedDecisionRevisesNothing(t *testing.T) {
	w, conn := newSQLiteWriter(t)
	ctx := context.Background()
	seedClaimAt(t, w, "cl_a", "the payments timeout is 30 seconds", time.Now().UTC())
	seedDecision(t, w, "dc_open", "cl_a")
	if _, err := w.SetDecisionExpectation(ctx, govwrite.DecisionExpectationRequest{
		DecisionID: "dc_open", RestsOn: []string{"cl_a"}, Predicted: 200, Tolerance: 1,
	}); err != nil {
		t.Fatalf("SetDecisionExpectation: %v", err)
	}
	if recs, err := w.ReconcileExpectations(ctx); err != nil || len(recs) != 0 {
		t.Fatalf("recs = %+v, %v; want nothing", recs, err)
	}
	if s := claimState(t, conn, "cl_a").Status; s != domain.ClaimStatusActive {
		t.Errorf("status = %s, want active", s)
	}
}

// Causal edges need evidence (issue #446): reconciliation and revision write
// verdicts, status and validity, never a causal relationship.
func assertNoCausalEdges(t *testing.T, conn *store.Conn) {
	t.Helper()
	rels, err := conn.Relationships.ListAll(context.Background())
	if err != nil {
		t.Fatalf("list relationships: %v", err)
	}
	for _, r := range rels {
		if r.Type == domain.RelationshipTypeCauses || r.Type == domain.RelationshipTypeCausedBy {
			t.Errorf("causal relationship written: %+v", r)
		}
	}
	for _, kind := range []domain.RelationshipType{domain.RelationshipTypeCauses, domain.RelationshipTypeCausedBy} {
		edges, err := conn.EntityRels.ListByKind(context.Background(), string(kind))
		if err != nil {
			t.Fatalf("list %s: %v", kind, err)
		}
		if len(edges) > 0 {
			t.Errorf("causal entity edges written: %+v", edges)
		}
	}
}
