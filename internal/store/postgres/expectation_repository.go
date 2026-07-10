package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// ExpectationRepository persists structured forward expectations in the configured
// Postgres namespace. Per-tenant isolation is handled by the ADR 0007 tenant column
// + RLS, so this issues plain SQL.
type ExpectationRepository struct {
	db pgQuerier
	ns string
}

// Upsert writes the expectation on its claim_id primary key.
func (r ExpectationRepository) Upsert(ctx context.Context, exp domain.Expectation) error {
	if exp.ClaimID == "" {
		return errors.New("expectation upsert: claim_id required")
	}
	var horizon any
	if !exp.Horizon.IsZero() {
		horizon = exp.Horizon.UTC()
	}
	createdAt := exp.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	_, err := r.db.ExecContext(ctx, fmt.Sprintf(`
INSERT INTO %s (claim_id, predicted, tolerance, horizon, observed, has_observation, resolved, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (claim_id) DO UPDATE SET
  predicted = excluded.predicted,
  tolerance = excluded.tolerance,
  horizon = excluded.horizon,
  observed = excluded.observed,
  has_observation = excluded.has_observation,
  resolved = excluded.resolved`, qualify(r.ns, "claim_expectations")),
		exp.ClaimID, exp.Predicted, exp.Tolerance, horizon, exp.Observed, exp.HasObservation, exp.Resolved, createdAt.UTC())
	if err != nil {
		return fmt.Errorf("upsert claim_expectation %s: %w", exp.ClaimID, err)
	}
	return nil
}

// Get returns the expectation for claimID, or ok=false when none exists.
func (r ExpectationRepository) Get(ctx context.Context, claimID string) (domain.Expectation, bool, error) {
	var (
		exp     = domain.Expectation{ClaimID: claimID}
		horizon sql.NullTime
	)
	err := r.db.QueryRowContext(ctx, fmt.Sprintf(`
SELECT predicted, tolerance, horizon, observed, has_observation, resolved, created_at
FROM %s WHERE claim_id = $1`, qualify(r.ns, "claim_expectations")), claimID).
		Scan(&exp.Predicted, &exp.Tolerance, &horizon, &exp.Observed, &exp.HasObservation, &exp.Resolved, &exp.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Expectation{}, false, nil
	}
	if err != nil {
		return domain.Expectation{}, false, fmt.Errorf("get expectation %s: %w", claimID, err)
	}
	if horizon.Valid {
		exp.Horizon = horizon.Time
	}
	return exp, true, nil
}

// ListOpen returns every unresolved expectation, claim-id ordered.
func (r ExpectationRepository) ListOpen(ctx context.Context) ([]domain.Expectation, error) {
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`
SELECT claim_id, predicted, tolerance, horizon, observed, has_observation, resolved, created_at
FROM %s WHERE resolved = false ORDER BY claim_id ASC`, qualify(r.ns, "claim_expectations")))
	if err != nil {
		return nil, fmt.Errorf("list open expectations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]domain.Expectation, 0)
	for rows.Next() {
		var (
			exp     domain.Expectation
			horizon sql.NullTime
		)
		if err := rows.Scan(&exp.ClaimID, &exp.Predicted, &exp.Tolerance, &horizon, &exp.Observed, &exp.HasObservation, &exp.Resolved, &exp.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan expectation: %w", err)
		}
		if horizon.Valid {
			exp.Horizon = horizon.Time
		}
		out = append(out, exp)
	}
	return out, rows.Err()
}
