package mysql

import (
	"context"
	"database/sql"
	"fmt"

	"go.klarlabs.de/mnemos/internal/domain"
)

// RelationshipRepository implements ports.RelationshipRepository.
type RelationshipRepository struct {
	db *sql.DB
}

// Upsert inserts or replaces relationships keyed by id.
func (r RelationshipRepository) Upsert(ctx context.Context, relationships []domain.Relationship) error {
	if len(relationships) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin relationship upsert tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt := `
INSERT INTO relationships (id, type, from_claim_id, to_claim_id, created_at, created_by)
VALUES (?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
  type = VALUES(type),
  from_claim_id = VALUES(from_claim_id),
  to_claim_id = VALUES(to_claim_id)`
	for _, rel := range relationships {
		if err := rel.Validate(); err != nil {
			return fmt.Errorf("invalid relationship %s: %w", rel.ID, err)
		}
		if _, err := tx.ExecContext(ctx, stmt,
			rel.ID, string(rel.Type), rel.FromClaimID, rel.ToClaimID,
			rel.CreatedAt.UTC(), actorOr(rel.CreatedBy),
		); err != nil {
			return fmt.Errorf("upsert relationship %s: %w", rel.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit relationship upsert tx: %w", err)
	}
	return nil
}

// ListByClaim returns relationships touching the given claim.
func (r RelationshipRepository) ListByClaim(ctx context.Context, claimID string) ([]domain.Relationship, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, type, from_claim_id, to_claim_id, created_at, created_by
FROM relationships WHERE from_claim_id = ? OR to_claim_id = ?`, claimID, claimID)
	if err != nil {
		return nil, fmt.Errorf("list relationships by claim: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectRelationshipRows(rows)
}

// RepointEndpoint rewrites relationship endpoints from oldID to
// newID. Like Postgres, MySQL has no UPDATE OR IGNORE; we
// pre-emptively delete the rows that would conflict on the unique
// (type, from_claim_id, to_claim_id) index, then update what's
// left, then drop the resulting self-loops.
func (r RelationshipRepository) RepointEndpoint(ctx context.Context, oldID, newID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin repoint endpoint tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	conflictFrom := `
DELETE a FROM relationships a
JOIN relationships b
  ON b.type = a.type
 AND b.from_claim_id = ?
 AND b.to_claim_id = a.to_claim_id
WHERE a.from_claim_id = ?`
	conflictTo := `
DELETE a FROM relationships a
JOIN relationships b
  ON b.type = a.type
 AND b.from_claim_id = a.from_claim_id
 AND b.to_claim_id = ?
WHERE a.to_claim_id = ?`
	if _, err := tx.ExecContext(ctx, conflictFrom, newID, oldID); err != nil {
		return fmt.Errorf("clear conflicting from-edges: %w", err)
	}
	if _, err := tx.ExecContext(ctx, conflictTo, newID, oldID); err != nil {
		return fmt.Errorf("clear conflicting to-edges: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE relationships SET from_claim_id = ? WHERE from_claim_id = ?`,
		newID, oldID,
	); err != nil {
		return fmt.Errorf("repoint from %s -> %s: %w", oldID, newID, err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE relationships SET to_claim_id = ? WHERE to_claim_id = ?`,
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
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit repoint endpoint tx: %w", err)
	}
	return nil
}

// DeleteByClaim removes every relationship touching the claim.
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

// ListAll returns every relationship ordered by created_at ascending.
func (r RelationshipRepository) ListAll(ctx context.Context) ([]domain.Relationship, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, type, from_claim_id, to_claim_id, created_at, created_by
FROM relationships ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list all relationships: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectRelationshipRows(rows)
}

// ListByClaimIDs returns relationships touching any of the given claims.
func (r RelationshipRepository) ListByClaimIDs(ctx context.Context, claimIDs []string) ([]domain.Relationship, error) {
	if len(claimIDs) == 0 {
		return []domain.Relationship{}, nil
	}
	placeholders, args := inPlaceholders(claimIDs)
	// Same args twice for from_claim_id and to_claim_id IN clauses.
	args2 := append(append([]any{}, args...), args...)
	//nolint:gosec // G202: placeholders are literal "?" tokens, not user input
	q := `
SELECT id, type, from_claim_id, to_claim_id, created_at, created_by
FROM relationships
WHERE from_claim_id IN (` + placeholders + `) OR to_claim_id IN (` + placeholders + `)`
	rows, err := r.db.QueryContext(ctx, q, args2...)
	if err != nil {
		return nil, fmt.Errorf("list relationships by claim ids: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectRelationshipRows(rows)
}

func collectRelationshipRows(rows *sql.Rows) ([]domain.Relationship, error) {
	out := make([]domain.Relationship, 0)
	for rows.Next() {
		var rel domain.Relationship
		var typ string
		if err := rows.Scan(&rel.ID, &typ, &rel.FromClaimID, &rel.ToClaimID, &rel.CreatedAt, &rel.CreatedBy); err != nil {
			return nil, fmt.Errorf("scan relationship row: %w", err)
		}
		rel.Type = domain.RelationshipType(typ)
		out = append(out, rel)
	}
	return out, rows.Err()
}
