package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store/sqlite/sqlcgen"
)

// RelationshipRepository provides SQLite-backed storage for claim relationships.
type RelationshipRepository struct {
	db *sql.DB
	q  *sqlcgen.Queries
}

// NewRelationshipRepository returns a RelationshipRepository backed by the given database.
func NewRelationshipRepository(db *sql.DB) RelationshipRepository {
	return RelationshipRepository{db: db, q: sqlcgen.New(db)}
}

// Upsert inserts or updates the given relationships in a single transaction.
func (r RelationshipRepository) Upsert(ctx context.Context, relationships []domain.Relationship) error {
	if len(relationships) == 0 {
		return nil
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin relationship upsert tx: %w", err)
	}
	defer rollbackTx(tx)

	qtx := r.q.WithTx(tx)

	for _, rel := range relationships {
		err := qtx.UpsertRelationship(ctx, sqlcgen.UpsertRelationshipParams{
			ID:          rel.ID,
			Type:        string(rel.Type),
			FromClaimID: rel.FromClaimID,
			ToClaimID:   rel.ToClaimID,
			CreatedAt:   rel.CreatedAt.UTC().Format(time.RFC3339Nano),
			CreatedBy:   actorOr(rel.CreatedBy),
		})
		if err != nil {
			return fmt.Errorf("upsert relationship %s: %w", rel.ID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit relationship upsert tx: %w", err)
	}

	return nil
}

// ListByClaim returns all relationships where the given claim is either the source or target.
func (r RelationshipRepository) ListByClaim(ctx context.Context, claimID string) ([]domain.Relationship, error) {
	rows, err := r.q.ListRelationshipsByClaim(ctx, sqlcgen.ListRelationshipsByClaimParams{
		FromClaimID: claimID,
		ToClaimID:   claimID,
	})
	if err != nil {
		return nil, fmt.Errorf("list relationships by claim: %w", err)
	}

	rels := make([]domain.Relationship, 0, len(rows))
	for _, row := range rows {
		t, err := time.Parse(time.RFC3339Nano, row.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("parse relationship created_at: %w", err)
		}
		rels = append(rels, domain.Relationship{
			ID:          row.ID,
			Type:        domain.RelationshipType(row.Type),
			FromClaimID: row.FromClaimID,
			ToClaimID:   row.ToClaimID,
			CreatedAt:   t,
			CreatedBy:   row.CreatedBy,
		})
	}

	return rels, nil
}

// RepointEndpoint rewrites every relationship whose from_claim_id
// or to_claim_id equals oldID to point at newID. Self-loops created
// by the rewrite (newID-newID) are dropped, and unique-edge
// conflicts collapse via UPDATE OR IGNORE — Mnemos doesn't
// distinguish duplicate edges. Used by ApplySemanticDedupe.
func (r RelationshipRepository) RepointEndpoint(ctx context.Context, oldID, newID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin repoint endpoint tx: %w", err)
	}
	defer rollbackTx(tx)
	if _, err := tx.ExecContext(ctx,
		`UPDATE OR IGNORE relationships SET from_claim_id = ? WHERE from_claim_id = ?`,
		newID, oldID,
	); err != nil {
		return fmt.Errorf("repoint from %s -> %s: %w", oldID, newID, err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE OR IGNORE relationships SET to_claim_id = ? WHERE to_claim_id = ?`,
		newID, oldID,
	); err != nil {
		return fmt.Errorf("repoint to %s -> %s: %w", oldID, newID, err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM relationships WHERE from_claim_id = ? AND to_claim_id = ?`,
		newID, newID,
	); err != nil {
		return fmt.Errorf("drop self-loops on %s: %w", newID, err)
	}
	// UPDATE OR IGNORE silently drops rows that would violate the
	// unique edge index. Clean up the orphans: any rows still
	// pointing at oldID after the rewrites are conflicts we accept
	// losing (they would have collapsed onto an already-existing
	// edge anyway).
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM relationships WHERE from_claim_id = ? OR to_claim_id = ?`,
		oldID, oldID,
	); err != nil {
		return fmt.Errorf("drop orphans on %s: %w", oldID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit repoint endpoint tx: %w", err)
	}
	return nil
}

// DeleteByClaim removes every relationship that touches the given
// claim (as source or target).
func (r RelationshipRepository) DeleteByClaim(ctx context.Context, claimID string) error {
	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM relationships WHERE from_claim_id = ? OR to_claim_id = ?`,
		claimID, claimID,
	); err != nil {
		return fmt.Errorf("delete relationships for %s: %w", claimID, err)
	}
	return nil
}

// CountAll returns the total number of relationships stored.
func (r RelationshipRepository) CountAll(ctx context.Context) (int64, error) {
	var n int64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM relationships`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count relationships: %w", err)
	}
	return n, nil
}

// CountByType returns the number of relationships with the given type.
func (r RelationshipRepository) CountByType(ctx context.Context, relType string) (int64, error) {
	var n int64
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM relationships WHERE type = ?`, relType,
	).Scan(&n); err != nil {
		return 0, fmt.Errorf("count relationships by type: %w", err)
	}
	return n, nil
}

// DeleteAll wipes the relationships table.
func (r RelationshipRepository) DeleteAll(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM relationships`); err != nil {
		return fmt.Errorf("delete all relationships: %w", err)
	}
	return nil
}

// ListAll returns every relationship stored, ordered by created_at
// ascending.
func (r RelationshipRepository) ListAll(ctx context.Context) ([]domain.Relationship, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, type, from_claim_id, to_claim_id, created_at, created_by
		 FROM relationships
		 ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list all relationships: %w", err)
	}
	defer closeRows(rows)

	out := make([]domain.Relationship, 0)
	for rows.Next() {
		var (
			id, typ, from, to, createdStr, createdBy string
		)
		if err := rows.Scan(&id, &typ, &from, &to, &createdStr, &createdBy); err != nil {
			return nil, fmt.Errorf("scan relationship row: %w", err)
		}
		t, err := time.Parse(time.RFC3339Nano, createdStr)
		if err != nil {
			return nil, fmt.Errorf("parse relationship created_at: %w", err)
		}
		out = append(out, domain.Relationship{
			ID:          id,
			Type:        domain.RelationshipType(typ),
			FromClaimID: from,
			ToClaimID:   to,
			CreatedAt:   t,
			CreatedBy:   createdBy,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate relationship rows: %w", err)
	}
	return out, nil
}

// ListByClaimIDs returns every relationship that touches any of the given
// claim IDs (as source OR target). Used by hop-expansion in the query
// engine — N IDs in one round trip rather than N round trips.
func (r RelationshipRepository) ListByClaimIDs(ctx context.Context, claimIDs []string) ([]domain.Relationship, error) {
	if len(claimIDs) == 0 {
		return []domain.Relationship{}, nil
	}

	placeholders := make([]string, 0, len(claimIDs))
	args := make([]any, 0, len(claimIDs)*2)
	for _, id := range claimIDs {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	for _, id := range claimIDs {
		args = append(args, id)
	}
	in := strings.Join(placeholders, ",")

	q := "SELECT id, type, from_claim_id, to_claim_id, created_at, created_by FROM relationships WHERE from_claim_id IN (" + in + ") OR to_claim_id IN (" + in + ")"
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list relationships by claim ids: %w", err)
	}
	defer closeRows(rows)

	out := make([]domain.Relationship, 0)
	for rows.Next() {
		var (
			id, typ, from, to, createdStr, createdBy string
		)
		if err := rows.Scan(&id, &typ, &from, &to, &createdStr, &createdBy); err != nil {
			return nil, fmt.Errorf("scan relationship row: %w", err)
		}
		t, err := time.Parse(time.RFC3339Nano, createdStr)
		if err != nil {
			return nil, fmt.Errorf("parse relationship created_at: %w", err)
		}
		out = append(out, domain.Relationship{
			ID:          id,
			Type:        domain.RelationshipType(typ),
			FromClaimID: from,
			ToClaimID:   to,
			CreatedAt:   t,
			CreatedBy:   createdBy,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate relationship rows: %w", err)
	}
	return out, nil
}
