package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// ExpectationRepository is the SQLite-backed implementation of
// [ports.ExpectationRepository].
type ExpectationRepository struct {
	db *sql.DB
}

// NewExpectationRepository returns a repository bound to the given *sql.DB.
func NewExpectationRepository(db *sql.DB) ExpectationRepository {
	return ExpectationRepository{db: db}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Upsert writes the expectation on its claim_id primary key.
func (r ExpectationRepository) Upsert(ctx context.Context, exp domain.Expectation) error {
	if exp.ClaimID == "" {
		return errors.New("expectation upsert: claim_id required")
	}
	var horizon, createdAt string
	if !exp.Horizon.IsZero() {
		horizon = exp.Horizon.UTC().Format(time.RFC3339Nano)
	}
	if !exp.CreatedAt.IsZero() {
		createdAt = exp.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO claim_expectations (claim_id, predicted, tolerance, horizon, observed, has_observation, resolved, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(claim_id) DO UPDATE SET
  predicted = excluded.predicted,
  tolerance = excluded.tolerance,
  horizon = excluded.horizon,
  observed = excluded.observed,
  has_observation = excluded.has_observation,
  resolved = excluded.resolved,
  created_at = excluded.created_at
`, exp.ClaimID, exp.Predicted, exp.Tolerance, horizon, exp.Observed, boolToInt(exp.HasObservation), boolToInt(exp.Resolved), createdAt)
	if err != nil {
		return fmt.Errorf("upsert claim_expectation %s: %w", exp.ClaimID, err)
	}
	return nil
}

// Get returns the expectation for claimID, or ok=false when none exists.
func (r ExpectationRepository) Get(ctx context.Context, claimID string) (domain.Expectation, bool, error) {
	row := r.db.QueryRowContext(ctx, `
SELECT predicted, tolerance, horizon, observed, has_observation, resolved, created_at
FROM claim_expectations WHERE claim_id = ?`, claimID)
	exp, err := scanExpectation(claimID, row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Expectation{}, false, nil
	}
	if err != nil {
		return domain.Expectation{}, false, err
	}
	return exp, true, nil
}

// ListOpen returns every unresolved expectation, claim-id ordered.
func (r ExpectationRepository) ListOpen(ctx context.Context) ([]domain.Expectation, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT claim_id, predicted, tolerance, horizon, observed, has_observation, resolved, created_at
FROM claim_expectations WHERE resolved = 0 ORDER BY claim_id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list open expectations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]domain.Expectation, 0)
	for rows.Next() {
		var (
			claimID                        string
			predicted, tolerance, observed float64
			horizon, createdAt             string
			hasObservation, resolved       int
		)
		if err := rows.Scan(&claimID, &predicted, &tolerance, &horizon, &observed, &hasObservation, &resolved, &createdAt); err != nil {
			return nil, fmt.Errorf("scan expectation: %w", err)
		}
		exp, perr := buildExpectation(claimID, predicted, tolerance, horizon, observed, hasObservation, resolved, createdAt)
		if perr != nil {
			return nil, perr
		}
		out = append(out, exp)
	}
	return out, rows.Err()
}

func scanExpectation(claimID string, row *sql.Row) (domain.Expectation, error) {
	var (
		predicted, tolerance, observed float64
		horizon, createdAt             string
		hasObservation, resolved       int
	)
	if err := row.Scan(&predicted, &tolerance, &horizon, &observed, &hasObservation, &resolved, &createdAt); err != nil {
		return domain.Expectation{}, err
	}
	return buildExpectation(claimID, predicted, tolerance, horizon, observed, hasObservation, resolved, createdAt)
}

func buildExpectation(claimID string, predicted, tolerance float64, horizon string, observed float64, hasObservation, resolved int, createdAt string) (domain.Expectation, error) {
	exp := domain.Expectation{
		ClaimID:        claimID,
		Predicted:      predicted,
		Tolerance:      tolerance,
		Observed:       observed,
		HasObservation: hasObservation != 0,
		Resolved:       resolved != 0,
	}
	for _, f := range []struct {
		s string
		t *time.Time
	}{{horizon, &exp.Horizon}, {createdAt, &exp.CreatedAt}} {
		if f.s == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339Nano, f.s)
		if err != nil {
			return domain.Expectation{}, fmt.Errorf("parse expectation time: %w", err)
		}
		*f.t = parsed
	}
	return exp, nil
}
