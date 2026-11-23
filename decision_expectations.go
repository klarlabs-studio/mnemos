package mnemos

import (
	"context"
	"fmt"
	"sort"
	"time"

	"go.klarlabs.de/mnemos/internal/clock"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
)

// Expectations tied to decisions (ADR 0049). A decision records the beliefs
// an agent acted on; its ExpectedOutcome is what the agent expected acting on
// them to produce. The checks and writes are govwrite's
// (CheckDecisionExpectation / CheckDecisionObservation, then the existing
// set_expectation / observe_expectation governed writes under the decision's
// key), the one implementation every transport shares.

// checkDecisionExpectation validates d's expectation before anything is
// written, so a decision with an unusable expectation is refused whole
// instead of being recorded without it.
func (m *memory) checkDecisionExpectation(ctx context.Context, id string, d Decision) (domain.Expectation, error) {
	return govwrite.CheckDecisionExpectation(ctx, m.conn, govwrite.DecisionExpectationRequest{
		DecisionID: id, RestsOn: d.Beliefs,
		Predicted: d.Expectation.Predicted, Tolerance: d.Expectation.Tolerance, Horizon: d.Expectation.Horizon,
	}, clock.Now().UTC())
}

// RecordDecisionObservation implements [Memory.RecordDecisionObservation].
func (m *memory) RecordDecisionObservation(ctx context.Context, decisionID string, observed float64) error {
	key, err := govwrite.CheckDecisionObservation(ctx, m.conn, decisionID)
	if err != nil {
		return fmt.Errorf("mnemos: RecordDecisionObservation: %w", err)
	}
	in := observeExpectationInput{ClaimID: key, Observed: observed}
	if _, err := dispatchWrite[domain.Expectation](ctx, m, actionObserveExpectation, in); err != nil {
		return fmt.Errorf("mnemos: RecordDecisionObservation: %w", err)
	}
	return nil
}

// outcomeObservation is one observed prediction and the beliefs its surprise
// is attributed to.
type outcomeObservation struct {
	at       time.Time
	surprise float64
	refuted  bool
	restsOn  []string
}

// outcomeObservations returns every observed prediction that rests on a
// decision's beliefs, oldest first: each belief's own expectation (once per
// belief, however many decisions rested on it) and each decision's expected
// outcome (attributed to all of its beliefs).
func (m *memory) outcomeObservations(ctx context.Context) ([]outcomeObservation, error) {
	if m.conn.Expectations == nil || m.conn.Decisions == nil {
		return nil, nil
	}
	decisions, err := m.conn.Decisions.ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("list decisions: %w", err)
	}
	var out []outcomeObservation
	add := func(key string, restsOn []string) error {
		exp, ok, err := m.conn.Expectations.Get(ctx, key)
		if err != nil {
			return fmt.Errorf("get expectation %s: %w", key, err)
		}
		if !ok {
			return nil
		}
		s, ok := exp.Surprise()
		if !ok {
			return nil // unobserved prediction — no error yet
		}
		confirmed, _ := exp.Confirmed()
		out = append(out, outcomeObservation{at: exp.CreatedAt, surprise: s, refuted: !confirmed, restsOn: restsOn})
		return nil
	}
	seen := map[string]bool{}
	for _, d := range decisions {
		if len(d.Beliefs) == 0 {
			continue
		}
		if err := add(domain.DecisionExpectationKey(d.ID), d.Beliefs); err != nil {
			return nil, err
		}
		for _, b := range d.Beliefs {
			if seen[b] {
				continue
			}
			seen[b] = true
			if err := add(b, []string{b}); err != nil {
				return nil, err
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].at.Before(out[j].at) })
	return out, nil
}

// attributedSurprises folds observations into the per-belief attribution
// PredictiveError reports, largest surprise first, then by claim id.
func attributedSurprises(obs []outcomeObservation) []AttributedSurprise {
	by := map[string]*AttributedSurprise{}
	for _, o := range obs {
		for _, b := range o.restsOn {
			a := by[b]
			if a == nil {
				a = &AttributedSurprise{ClaimID: b}
				by[b] = a
			}
			a.Predictions++
			if o.refuted {
				a.Refuted++
			}
			if o.surprise > a.Surprise {
				a.Surprise = o.surprise
			}
		}
	}
	out := make([]AttributedSurprise, 0, len(by))
	for _, a := range by {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Surprise != out[j].Surprise {
			return out[i].Surprise > out[j].Surprise
		}
		return out[i].ClaimID < out[j].ClaimID
	})
	if len(out) == 0 {
		return nil
	}
	return out
}

// toPublicReconciliations maps govwrite's reconciliations onto the public type.
func toPublicReconciliations(recs []govwrite.Reconciliation) []Reconciliation {
	var out []Reconciliation
	for _, r := range recs {
		pub := Reconciliation{
			ClaimID: r.ClaimID, Predicted: r.Predicted, Observed: r.Observed, Surprise: r.Surprise, Confirmed: r.Confirmed,
			DecisionID: r.DecisionID, AttributedTo: r.AttributedTo, Rechecked: r.Rechecked,
		}
		for _, rv := range r.Revisions {
			pub.Revisions = append(pub.Revisions, BeliefRevision{ClaimID: rv.ClaimID, Kind: rv.Kind, By: rv.By})
		}
		out = append(out, pub)
	}
	return out
}
