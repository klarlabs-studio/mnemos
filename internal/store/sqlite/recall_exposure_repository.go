package sqlite

import (
	"context"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// RecordRecallExposures implements [ports.RecallExposureRepository]. The first
// exposure of a (session, belief) is kept: INSERT OR IGNORE.
func (r ClaimRepository) RecordRecallExposures(ctx context.Context, exposures []domain.RecallExposure) error {
	for _, x := range exposures {
		if err := x.Validate(); err != nil {
			return fmt.Errorf("invalid recall exposure: %w", err)
		}
	}
	if len(exposures) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin recall exposure tx: %w", err)
	}
	defer rollbackTx(tx)
	for _, x := range exposures {
		// RFC3339Nano like events.timestamp; the trust aggregate compares the
		// two through julianday(), never as text (fractional digits vary).
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO recall_exposures (session_id, claim_id, exposed_at) VALUES (?, ?, ?)`,
			x.SessionID, x.ClaimID, x.ExposedAt.UTC().Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("insert recall exposure %s %s: %w", x.SessionID, x.ClaimID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit recall exposure tx: %w", err)
	}
	return nil
}

// ListRecallExposures implements [ports.RecallExposureRepository].
func (r ClaimRepository) ListRecallExposures(ctx context.Context, claimIDs []string) ([]domain.RecallExposure, error) {
	out := make([]domain.RecallExposure, 0)
	err := inChunks(claimIDs, func(chunk []string) error {
		rows, err := r.db.QueryContext(ctx,
			`SELECT session_id, claim_id, exposed_at FROM recall_exposures WHERE claim_id IN (`+placeholders(len(chunk))+`)`, //nolint:gosec // G202: placeholders are literal "?"
			anyArgs(chunk)...)
		if err != nil {
			return fmt.Errorf("list recall exposures: %w", err)
		}
		defer closeRows(rows)
		for rows.Next() {
			var x domain.RecallExposure
			var at string
			if err := rows.Scan(&x.SessionID, &x.ClaimID, &at); err != nil {
				return fmt.Errorf("scan recall exposure: %w", err)
			}
			if x.ExposedAt, err = time.Parse(time.RFC3339Nano, at); err != nil {
				return fmt.Errorf("parse recall exposure time %q: %w", at, err)
			}
			out = append(out, x)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	domain.SortRecallExposures(out)
	return out, nil
}
