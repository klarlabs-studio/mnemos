package sqlite

import (
	"context"
	"fmt"
	"slices"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
)

// liveClaimSQL is the live-belief predicate: valid time open, not deprecated.
// It must match idx_claims_live's WHERE clause exactly, and mnemos's
// isLiveBelief in meaning (TestHealthSampler_* checks both).
const liveClaimSQL = `(valid_to IS NULL OR valid_to = '') AND status <> 'deprecated'`

// liveIndex pins idx_claims_live. Left to itself the planner prefers
// idx_claims_valid_to, reads every row to check status and sorts the ids in a
// temp b-tree: 0.57 s against 0.16 s for the covering scan at 1M beliefs.
const liveIndex = `INDEXED BY idx_claims_live`

// CountLiveClaims implements ports.HealthSampler.
func (r ClaimRepository) CountLiveClaims(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM claims `+liveIndex+` WHERE `+liveClaimSQL).Scan(&n)
	return n, err
}

// CountLiveOrphans implements ports.HealthSampler.
func (r ClaimRepository) CountLiveOrphans(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM claims c `+liveIndex+` WHERE `+liveClaimSQL+`
		AND NOT EXISTS (SELECT 1 FROM claim_evidence e WHERE e.claim_id = c.id)`).Scan(&n)
	return n, err
}

// CountDanglingRelationships implements ports.HealthSampler.
func (r ClaimRepository) CountDanglingRelationships(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM relationships r
		WHERE NOT EXISTS (SELECT 1 FROM claims c WHERE c.id = r.from_claim_id)
		   OR NOT EXISTS (SELECT 1 FROM claims c WHERE c.id = r.to_claim_id)`).Scan(&n)
	return n, err
}

// SampleLiveClaims implements ports.HealthSampler: it lists the live ids from
// idx_claims_live, chooses n with a seeded partial shuffle, and loads only
// those claims. Ordered by id, so the result depends on nothing but the data
// and the seed.
func (r ClaimRepository) SampleLiveClaims(ctx context.Context, n int, seed uint64) ([]domain.Claim, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id FROM claims `+liveIndex+` WHERE `+liveClaimSQL+` ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list live claim ids: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			closeRows(rows)
			return nil, err
		}
		ids = append(ids, id)
	}
	closeRows(rows)
	if err := rows.Err(); err != nil {
		return nil, err
	}
	ids = store.ChooseSample(ids, n, seed)
	var out []domain.Claim
	for start := 0; start < len(ids); start += 5000 {
		cs, err := r.ListByIDs(ctx, ids[start:min(start+5000, len(ids))])
		if err != nil {
			return nil, err
		}
		out = append(out, cs...)
	}
	slices.SortFunc(out, func(a, b domain.Claim) int {
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		}
		return 0
	})
	return out, nil
}

// CountHypercorrections implements ports.HealthSampler. The CASE mirrors
// hypercorrectionList's establishment ranking exactly, so the count equals the
// length of the list it would build (TestBrainHealth_SampledEqualsExact...).
func (r ClaimRepository) CountHypercorrections(ctx context.Context, floor float64) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `
		WITH e AS (
			SELECT a.lifecycle AS la, a.trust_score AS ta, b.lifecycle AS lb, b.trust_score AS tb,
			       CASE WHEN a.lifecycle = 'promoted' THEN 1.0 + a.trust_score ELSE a.trust_score END AS ea,
			       CASE WHEN b.lifecycle = 'promoted' THEN 1.0 + b.trust_score ELSE b.trust_score END AS eb
			FROM relationships r
			JOIN claims a ON a.id = r.from_claim_id
			JOIN claims b ON b.id = r.to_claim_id
			WHERE r.type = 'contradicts'
			  AND (a.valid_to IS NULL OR a.valid_to = '') AND (b.valid_to IS NULL OR b.valid_to = '')
			  AND a.status <> 'deprecated' AND b.status <> 'deprecated'
			  AND a.lifecycle <> 'superseded' AND b.lifecycle <> 'superseded'
		)
		SELECT count(*) FROM e WHERE CASE WHEN eb > ea
			THEN lb = 'promoted' OR tb >= ?
			ELSE la = 'promoted' OR ta >= ? END`, floor, floor).Scan(&n)
	return n, err
}
