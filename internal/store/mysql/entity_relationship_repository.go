package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// EntityRelationshipRepository persists polymorphic cross-entity
// edges in the MySQL backend.
type EntityRelationshipRepository struct {
	db *sql.DB
}

// Upsert writes edges idempotently on the unique edge tuple.
func (r EntityRelationshipRepository) Upsert(ctx context.Context, edges []domain.EntityRelationship) error {
	for _, e := range edges {
		if err := e.Validate(); err != nil {
			return fmt.Errorf("invalid entity_relationship: %w", err)
		}
		createdAt := e.CreatedAt
		if createdAt.IsZero() {
			createdAt = time.Now().UTC()
		}
		if _, err := r.db.ExecContext(ctx, `
INSERT IGNORE INTO entity_relationships (id, kind, from_id, from_type, to_id, to_type, created_at, created_by)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			e.ID, string(e.Kind), e.FromID, e.FromType, e.ToID, e.ToType,
			createdAt.UTC(), actorOr(e.CreatedBy),
		); err != nil {
			return fmt.Errorf("insert entity_relationship %s: %w", e.ID, err)
		}
	}
	return nil
}

// ListByEntity returns edges touching (id, type) on either side.
func (r EntityRelationshipRepository) ListByEntity(ctx context.Context, entityID, entityType string) ([]domain.EntityRelationship, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, kind, from_id, from_type, to_id, to_type, created_at, created_by
FROM entity_relationships WHERE (from_id = ? AND from_type = ?) OR (to_id = ? AND to_type = ?)
ORDER BY created_at ASC`,
		entityID, entityType, entityID, entityType,
	)
	if err != nil {
		return nil, fmt.Errorf("list entity_relationships by entity: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectEntityRelationshipRows(rows)
}

// ListByKind returns edges with the given kind.
func (r EntityRelationshipRepository) ListByKind(ctx context.Context, kind string) ([]domain.EntityRelationship, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, kind, from_id, from_type, to_id, to_type, created_at, created_by
FROM entity_relationships WHERE kind = ? ORDER BY created_at ASC`, kind)
	if err != nil {
		return nil, fmt.Errorf("list entity_relationships by kind: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectEntityRelationshipRows(rows)
}

// ListAll returns every edge.
func (r EntityRelationshipRepository) ListAll(ctx context.Context) ([]domain.EntityRelationship, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, kind, from_id, from_type, to_id, to_type, created_at, created_by
FROM entity_relationships ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list entity_relationships: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectEntityRelationshipRows(rows)
}

// CountAll returns the total number of edges.
func (r EntityRelationshipRepository) CountAll(ctx context.Context) (int64, error) {
	var n int64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM entity_relationships`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count entity_relationships: %w", err)
	}
	return n, nil
}

// DeleteAll wipes every edge row.
func (r EntityRelationshipRepository) DeleteAll(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM entity_relationships`); err != nil {
		return fmt.Errorf("delete all entity_relationships: %w", err)
	}
	return nil
}

func collectEntityRelationshipRows(rows *sql.Rows) ([]domain.EntityRelationship, error) {
	out := make([]domain.EntityRelationship, 0)
	for rows.Next() {
		var e domain.EntityRelationship
		var kind string
		if err := rows.Scan(&e.ID, &kind, &e.FromID, &e.FromType, &e.ToID, &e.ToType, &e.CreatedAt, &e.CreatedBy); err != nil {
			return nil, err
		}
		e.Kind = domain.RelationshipType(kind)
		out = append(out, e)
	}
	return out, rows.Err()
}
