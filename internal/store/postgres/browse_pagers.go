package postgres

import (
	"context"
	"fmt"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/page"
	"go.klarlabs.de/mnemos/internal/ports"
)

// PageEvents implements ports.EventPager; timestamp is timestamptz, so the
// keyset compares instants.
func (r EventRepository) PageEvents(ctx context.Context, runID string, after *page.Key, limit int) (ports.Page[domain.Event], error) {
	events := qualify(r.ns, "events")
	var out ports.Page[domain.Event]
	if err := r.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT count(*) FROM %s WHERE ($1 = '' OR run_id = $1)`, events), runID).Scan(&out.Total); err != nil {
		return out, fmt.Errorf("count episodes: %w", err)
	}
	args := []any{runID}
	keyset := ""
	if after != nil {
		keyset = ` AND (timestamp < $2 OR (timestamp = $2 AND id < $3))`
		args = append(args, after.At.UTC(), after.ID)
	}
	args = append(args, limit+1)
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`SELECT id FROM %s WHERE ($1 = '' OR run_id = $1)%s
		ORDER BY timestamp DESC, id DESC LIMIT $%d`, events, keyset, len(args)), args...)
	if err != nil {
		return out, fmt.Errorf("page episodes: %w", err)
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
		out.More, ids = true, ids[:limit]
	}
	list, err := r.ListByIDs(ctx, ids)
	if err != nil {
		return out, err
	}
	byID := make(map[string]domain.Event, len(list))
	for _, e := range list {
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
	rels := qualify(r.ns, "relationships")
	var out ports.Page[domain.Relationship]
	if err := r.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT count(*) FROM %s WHERE ($1 = '' OR type = $1)`, rels), relType).Scan(&out.Total); err != nil {
		return out, fmt.Errorf("count associations: %w", err)
	}
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`SELECT id, type, from_claim_id, to_claim_id, created_at, created_by, strength, derived_by
		FROM %s WHERE ($1 = '' OR type = $1) AND id > $2 ORDER BY id LIMIT $3`, rels), relType, afterID, limit+1)
	if err != nil {
		return out, fmt.Errorf("page associations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var rel domain.Relationship
		var typ string
		if err := rows.Scan(&rel.ID, &typ, &rel.FromClaimID, &rel.ToClaimID, &rel.CreatedAt, &rel.CreatedBy, &rel.Strength, &rel.DerivedBy); err != nil {
			return out, err
		}
		rel.Type = domain.RelationshipType(typ)
		out.Items = append(out.Items, rel)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	if len(out.Items) > limit {
		out.More, out.Items = true, out.Items[:limit]
	}
	return out, nil
}
