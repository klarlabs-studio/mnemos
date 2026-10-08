package sqlite

import (
	"context"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/page"
	"go.klarlabs.de/mnemos/internal/ports"
)

// claimFilterSQL is the browse filter's WHERE clause and its arguments,
// without the time bounds (see PageClaims).
func claimFilterSQL(f page.ClaimFilter) (string, []any) {
	where := `(? = '' OR c.type = ?) AND (? = '' OR c.status = ?)
		AND (? = '' OR EXISTS (SELECT 1 FROM claim_evidence ce JOIN events e ON e.id = ce.event_id
		                         WHERE ce.claim_id = c.id AND e.run_id = ?))`
	return where, []any{f.Type, f.Type, f.Status, f.Status, f.RunID, f.RunID}
}

// PageClaims implements ports.ClaimPager. created_at is stored as RFC3339Nano
// text, whose order is not exactly chronological (trailing zeros are trimmed,
// so ".28106Z" sorts after ".281062Z"). The keyset predicate therefore
// compares the same text the ORDER BY sorts, which keeps the pages an exact
// partition; and filters that compare times (as_of, recorded_as_of) are left
// to the Go path, since text comparison would answer them only approximately.
func (r ClaimRepository) PageClaims(ctx context.Context, f page.ClaimFilter, after *page.Key, limit int) (ports.ClaimPage, error) {
	if f.HasTimeBounds() {
		return ports.ClaimPage{}, ports.ErrPageUnsupported
	}
	where, args := claimFilterSQL(f)
	var out ports.ClaimPage
	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM claims c WHERE `+where, args...).Scan(&out.Total); err != nil {
		return out, fmt.Errorf("count beliefs: %w", err)
	}
	keyset, kargs := "", []any{}
	if after != nil {
		at := after.At.UTC().Format(time.RFC3339Nano)
		keyset = ` AND (c.created_at < ? OR (c.created_at = ? AND c.id < ?))`
		kargs = []any{at, at, after.ID}
	}
	rows, err := r.db.QueryContext(ctx, `SELECT c.id FROM claims c WHERE `+where+keyset+`
		ORDER BY c.created_at DESC, c.id DESC LIMIT ?`, append(append(args, kargs...), limit+1)...)
	if err != nil {
		return out, fmt.Errorf("page beliefs: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			closeRows(rows)
			return out, err
		}
		ids = append(ids, id)
	}
	closeRows(rows)
	if err := rows.Err(); err != nil {
		return out, err
	}
	if len(ids) > limit {
		out.More = true
		ids = ids[:limit]
	}
	claims, err := r.ListByIDs(ctx, ids)
	if err != nil {
		return out, err
	}
	byID := make(map[string]domain.Claim, len(claims))
	for _, c := range claims {
		byID[c.ID] = c
	}
	for _, id := range ids {
		if c, ok := byID[id]; ok {
			out.Claims = append(out.Claims, c)
		}
	}
	return out, nil
}
