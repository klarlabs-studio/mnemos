package sqlite

import (
	"context"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/page"
	"go.klarlabs.de/mnemos/internal/ports"
)

// PageEvents implements ports.EventPager. timestamp is RFC3339Nano text, so,
// as for beliefs, the keyset compares the same text the ORDER BY sorts and
// the pages stay an exact partition. idx_events_timestamp serves the order.
func (r EventRepository) PageEvents(ctx context.Context, runID string, after *page.Key, limit int) (ports.Page[domain.Event], error) {
	var out ports.Page[domain.Event]
	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM events WHERE (? = '' OR run_id = ?)`, runID, runID).Scan(&out.Total); err != nil {
		return out, fmt.Errorf("count episodes: %w", err)
	}
	args := []any{runID, runID}
	keyset := ""
	if after != nil {
		at := after.At.UTC().Format(time.RFC3339Nano)
		keyset = ` AND (timestamp < ? OR (timestamp = ? AND id < ?))`
		args = append(args, at, at, after.ID)
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id FROM events WHERE (? = '' OR run_id = ?)`+keyset+`
		ORDER BY timestamp DESC, id DESC LIMIT ?`, append(args, limit+1)...)
	if err != nil {
		return out, fmt.Errorf("page episodes: %w", err)
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
		out.More, ids = true, ids[:limit]
	}
	events, err := r.ListByIDs(ctx, ids)
	if err != nil {
		return out, err
	}
	byID := make(map[string]domain.Event, len(events))
	for _, e := range events {
		byID[e.ID] = e
	}
	for _, id := range ids {
		if e, ok := byID[id]; ok {
			out.Items = append(out.Items, e)
		}
	}
	return out, nil
}

// PageRelationships implements ports.RelationshipPager over the primary key,
// reading the same columns ListAll does.
func (r RelationshipRepository) PageRelationships(ctx context.Context, relType, afterID string, limit int) (ports.Page[domain.Relationship], error) {
	var out ports.Page[domain.Relationship]
	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM relationships WHERE (? = '' OR type = ?)`, relType, relType).Scan(&out.Total); err != nil {
		return out, fmt.Errorf("count associations: %w", err)
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id, type, from_claim_id, to_claim_id, created_at, created_by
		FROM relationships WHERE (? = '' OR type = ?) AND id > ? ORDER BY id LIMIT ?`, relType, relType, afterID, limit+1)
	if err != nil {
		return out, fmt.Errorf("page associations: %w", err)
	}
	defer closeRows(rows)
	for rows.Next() {
		var id, typ, from, to, createdStr, createdBy string
		if err := rows.Scan(&id, &typ, &from, &to, &createdStr, &createdBy); err != nil {
			return out, err
		}
		t, err := time.Parse(time.RFC3339Nano, createdStr)
		if err != nil {
			return out, fmt.Errorf("parse relationship created_at: %w", err)
		}
		out.Items = append(out.Items, domain.Relationship{ID: id, Type: domain.RelationshipType(typ),
			FromClaimID: from, ToClaimID: to, CreatedAt: t, CreatedBy: createdBy})
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	if len(out.Items) > limit {
		out.More, out.Items = true, out.Items[:limit]
	}
	return out, nil
}
