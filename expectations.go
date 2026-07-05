package mnemos

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// Expect implements [Memory.Expect].
func (m *memory) Expect(ctx context.Context, claimID string, predicted, tolerance float64, horizon time.Time) error {
	if m.conn.Expectations == nil {
		return ErrExpectationsUnsupported
	}
	claimID = strings.TrimSpace(claimID)
	if claimID == "" {
		return errors.New("mnemos: Expect: claimID is required")
	}
	if tolerance < 0 {
		tolerance = -tolerance
	}
	return m.conn.Expectations.Upsert(ctx, domain.Expectation{
		ClaimID:   claimID,
		Predicted: predicted,
		Tolerance: tolerance,
		Horizon:   horizon,
		CreatedAt: time.Now().UTC(),
	})
}

// RecordObservation implements [Memory.RecordObservation].
func (m *memory) RecordObservation(ctx context.Context, claimID string, observed float64) error {
	if m.conn.Expectations == nil {
		return ErrExpectationsUnsupported
	}
	claimID = strings.TrimSpace(claimID)
	exp, ok, err := m.conn.Expectations.Get(ctx, claimID)
	if err != nil {
		return fmt.Errorf("mnemos: RecordObservation: %w", err)
	}
	if !ok {
		return fmt.Errorf("mnemos: RecordObservation: no expectation for claim %s", claimID)
	}
	exp.Observed = observed
	exp.HasObservation = true
	return m.conn.Expectations.Upsert(ctx, exp)
}

// ReconcileExpectations implements [Memory.ReconcileExpectations]: close every
// observed-but-open expectation into a verdict + surprise, emitting the
// validates/refutes edge that the surprise routing consumes.
func (m *memory) ReconcileExpectations(ctx context.Context) ([]Reconciliation, error) {
	if m.conn.Expectations == nil {
		return nil, nil
	}
	open, err := m.conn.Expectations.ListOpen(ctx)
	if err != nil {
		return nil, fmt.Errorf("mnemos: ReconcileExpectations: list open: %w", err)
	}
	now := time.Now().UTC()
	var out []Reconciliation
	for _, exp := range open {
		if !exp.HasObservation {
			continue // unobserved (even past horizon) stays unconfirmed — never refuted
		}
		diff := math.Abs(exp.Predicted - exp.Observed)
		confirmed := diff <= exp.Tolerance
		scale := exp.Tolerance
		if scale <= 0 {
			if scale = math.Abs(exp.Predicted); scale == 0 {
				scale = 1
			}
		}
		// Emit the verdict edge so the existing routing (ForgetRefuted /
		// ReinforceValidated) acts on it — the prediction loop closing on itself.
		kind := domain.RelationshipTypeRefutes
		if confirmed {
			kind = domain.RelationshipTypeValidates
		}
		if m.conn.EntityRels != nil {
			edge := domain.EntityRelationship{
				ID:        "expverdict_" + exp.ClaimID,
				Kind:      kind,
				FromID:    "exp_" + exp.ClaimID,
				FromType:  domain.RelEntityOutcome,
				ToID:      exp.ClaimID,
				ToType:    domain.RelEntityClaim,
				CreatedAt: now,
			}
			if err := m.conn.EntityRels.Upsert(ctx, []domain.EntityRelationship{edge}); err != nil {
				return out, fmt.Errorf("mnemos: ReconcileExpectations: emit verdict for %s: %w", exp.ClaimID, err)
			}
		}
		exp.Resolved = true
		if err := m.conn.Expectations.Upsert(ctx, exp); err != nil {
			return out, fmt.Errorf("mnemos: ReconcileExpectations: mark resolved %s: %w", exp.ClaimID, err)
		}
		out = append(out, Reconciliation{
			ClaimID:   exp.ClaimID,
			Predicted: exp.Predicted,
			Observed:  exp.Observed,
			Surprise:  diff / scale,
			Confirmed: confirmed,
		})
	}
	return out, nil
}
