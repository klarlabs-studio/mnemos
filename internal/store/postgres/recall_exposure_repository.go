package postgres

import (
	"context"
	"fmt"

	"go.klarlabs.de/mnemos/internal/domain"
)

// RecordRecallExposures implements [ports.RecallExposureRepository].
// recall_exposures is in the RLS scoped list, so a tenant reads and writes
// only its own rows. The first exposure of a (session, belief) is kept.
func (r ClaimRepository) RecordRecallExposures(ctx context.Context, exposures []domain.RecallExposure) error {
	for _, x := range exposures {
		if err := x.Validate(); err != nil {
			return fmt.Errorf("invalid recall exposure: %w", err)
		}
	}
	for _, x := range exposures {
		if _, err := r.db.ExecContext(ctx, fmt.Sprintf(`
INSERT INTO %s (session_id, claim_id, exposed_at) VALUES ($1, $2, $3)
ON CONFLICT (session_id, claim_id) DO NOTHING`, qualify(r.ns, "recall_exposures")),
			x.SessionID, x.ClaimID, x.ExposedAt.UTC(),
		); err != nil {
			return fmt.Errorf("insert recall exposure %s %s: %w", x.SessionID, x.ClaimID, err)
		}
	}
	return nil
}

// ListRecallExposures implements [ports.RecallExposureRepository].
func (r ClaimRepository) ListRecallExposures(ctx context.Context, claimIDs []string) ([]domain.RecallExposure, error) {
	out := make([]domain.RecallExposure, 0)
	if len(claimIDs) == 0 {
		return out, nil
	}
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`
SELECT session_id, claim_id, exposed_at FROM %s WHERE claim_id = ANY($1)`, qualify(r.ns, "recall_exposures")), pgArray(claimIDs))
	if err != nil {
		return nil, fmt.Errorf("list recall exposures: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var x domain.RecallExposure
		if err := rows.Scan(&x.SessionID, &x.ClaimID, &x.ExposedAt); err != nil {
			return nil, fmt.Errorf("scan recall exposure: %w", err)
		}
		x.ExposedAt = x.ExposedAt.UTC()
		out = append(out, x)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recall exposures: %w", err)
	}
	domain.SortRecallExposures(out)
	return out, nil
}
