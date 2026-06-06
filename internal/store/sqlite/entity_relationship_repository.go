package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store/sqlite/sqlcgen"
)

// EntityRelationshipRepository persists polymorphic cross-entity
// edges (action_of, outcome_of, validates, refutes, derived_from,
// causes between non-claim endpoints). Backed by sqlc-generated
// queries (see sql/sqlite/query/entity_relationships.sql).
type EntityRelationshipRepository struct {
	db *sql.DB
	q  *sqlcgen.Queries
}

// NewEntityRelationshipRepository returns a repository backed by db.
func NewEntityRelationshipRepository(db *sql.DB) EntityRelationshipRepository {
	return EntityRelationshipRepository{db: db, q: sqlcgen.New(db)}
}

// Upsert writes edges idempotently on the unique (kind, from_type,
// from_id, to_type, to_id) tuple. Re-emitting the same edge is a
// no-op.
func (r EntityRelationshipRepository) Upsert(ctx context.Context, edges []domain.EntityRelationship) error {
	if len(edges) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin entity_relationships tx: %w", err)
	}
	defer rollbackTx(tx)
	q := r.q.WithTx(tx)
	for _, e := range edges {
		if err := e.Validate(); err != nil {
			return fmt.Errorf("invalid entity_relationship: %w", err)
		}
		createdAt := e.CreatedAt
		if createdAt.IsZero() {
			createdAt = time.Now().UTC()
		}
		if err := q.UpsertEntityRelationship(ctx, sqlcgen.UpsertEntityRelationshipParams{
			ID:        e.ID,
			Kind:      string(e.Kind),
			FromID:    e.FromID,
			FromType:  e.FromType,
			ToID:      e.ToID,
			ToType:    e.ToType,
			CreatedAt: createdAt.UTC().Format(time.RFC3339Nano),
			CreatedBy: actorOr(e.CreatedBy),
		}); err != nil {
			return fmt.Errorf("insert entity_relationship %s: %w", e.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit entity_relationships tx: %w", err)
	}
	return nil
}

// ListByEntity returns edges where the given (id, type) is either the
// from or the to endpoint, oldest first.
func (r EntityRelationshipRepository) ListByEntity(ctx context.Context, entityID, entityType string) ([]domain.EntityRelationship, error) {
	rows, err := r.q.ListEntityRelationshipsByEntity(ctx, sqlcgen.ListEntityRelationshipsByEntityParams{
		FromID:   entityID,
		FromType: entityType,
	})
	if err != nil {
		return nil, fmt.Errorf("list entity_relationships by entity: %w", err)
	}
	return entityRelationshipRowsToDomain(rows)
}

// ListByKind returns edges with the given kind, oldest first.
func (r EntityRelationshipRepository) ListByKind(ctx context.Context, kind string) ([]domain.EntityRelationship, error) {
	rows, err := r.q.ListEntityRelationshipsByKind(ctx, kind)
	if err != nil {
		return nil, fmt.Errorf("list entity_relationships by kind: %w", err)
	}
	return entityRelationshipRowsToDomain(rows)
}

// ListAll returns every edge.
func (r EntityRelationshipRepository) ListAll(ctx context.Context) ([]domain.EntityRelationship, error) {
	rows, err := r.q.ListEntityRelationships(ctx)
	if err != nil {
		return nil, fmt.Errorf("list entity_relationships: %w", err)
	}
	return entityRelationshipRowsToDomain(rows)
}

// CountAll returns the total number of edges.
func (r EntityRelationshipRepository) CountAll(ctx context.Context) (int64, error) {
	n, err := r.q.CountEntityRelationships(ctx)
	if err != nil {
		return 0, fmt.Errorf("count entity_relationships: %w", err)
	}
	return n, nil
}

// DeleteAll wipes every entity_relationships row.
func (r EntityRelationshipRepository) DeleteAll(ctx context.Context) error {
	if err := r.q.DeleteAllEntityRelationships(ctx); err != nil {
		return fmt.Errorf("delete all entity_relationships: %w", err)
	}
	return nil
}

func entityRelationshipRowsToDomain(rows []sqlcgen.EntityRelationship) ([]domain.EntityRelationship, error) {
	out := make([]domain.EntityRelationship, 0, len(rows))
	for _, row := range rows {
		e := domain.EntityRelationship{
			ID:        row.ID,
			Kind:      domain.RelationshipType(row.Kind),
			FromID:    row.FromID,
			FromType:  row.FromType,
			ToID:      row.ToID,
			ToType:    row.ToType,
			CreatedBy: row.CreatedBy,
		}
		if t, err := time.Parse(time.RFC3339Nano, row.CreatedAt); err == nil {
			e.CreatedAt = t
		}
		out = append(out, e)
	}
	return out, nil
}
