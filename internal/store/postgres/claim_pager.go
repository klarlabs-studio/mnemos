package postgres

import (
	"context"
	"fmt"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/page"
	"go.klarlabs.de/mnemos/internal/ports"
)

// PageClaims implements ports.ClaimPager. Every filter, the time bounds
// included, is exact here: created_at, valid_from and valid_to are
// timestamptz, so SQL compares them as instants. as_of mirrors
// domain.Belief.IsValidAt: in force while valid_from <= t (no lower bound when
// NULL) and t < valid_to (no upper bound when NULL).
func (r ClaimRepository) PageClaims(ctx context.Context, f page.ClaimFilter, after *page.Key, limit int) (ports.ClaimPage, error) {
	claims := qualify(r.ns, "claims")
	evidence := qualify(r.ns, "claim_evidence")
	events := qualify(r.ns, "events")
	var asOf, recorded any
	if !f.AsOf.IsZero() {
		asOf = f.AsOf.UTC()
	}
	if !f.RecordedAsOf.IsZero() {
		recorded = f.RecordedAsOf.UTC()
	}
	where := fmt.Sprintf(`($1 = '' OR c.type = $1) AND ($2 = '' OR c.status = $2)
		AND ($3 = '' OR EXISTS (SELECT 1 FROM %s ce JOIN %s e ON e.id = ce.event_id
		                         WHERE ce.claim_id = c.id AND e.run_id = $3))
		AND ($4::timestamptz IS NULL OR ((c.valid_from IS NULL OR c.valid_from <= $4) AND (c.valid_to IS NULL OR $4 < c.valid_to)))
		AND ($5::timestamptz IS NULL OR c.created_at <= $5)`, evidence, events)
	args := []any{f.Type, f.Status, f.RunID, asOf, recorded}
	var out ports.ClaimPage
	if err := r.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT count(*) FROM %s c WHERE %s`, claims, where), args...).Scan(&out.Total); err != nil {
		return out, fmt.Errorf("count beliefs: %w", err)
	}
	keyset := ""
	if after != nil {
		keyset = ` AND (c.created_at < $6 OR (c.created_at = $6 AND c.id < $7))`
		args = append(args, after.At.UTC(), after.ID)
	}
	args = append(args, limit+1)
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`SELECT c.id FROM %s c WHERE %s%s ORDER BY c.created_at DESC, c.id DESC LIMIT $%d`,
		claims, where, keyset, len(args)), args...)
	if err != nil {
		return out, fmt.Errorf("page beliefs: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return out, err
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	if len(ids) > limit {
		out.More = true
		ids = ids[:limit]
	}
	list, err := r.ListByIDs(ctx, ids)
	if err != nil {
		return out, err
	}
	byID := make(map[string]domain.Claim, len(list))
	for _, c := range list {
		byID[c.ID] = c
	}
	for _, id := range ids {
		if c, ok := byID[id]; ok {
			out.Claims = append(out.Claims, c)
		}
	}
	return out, nil
}
