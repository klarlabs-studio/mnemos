package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"go.klarlabs.de/mnemos/internal/domain"
)

// RelationshipRepository persists claim → claim edges. The (id) is
// the dedup key; ON CONFLICT (id) DO UPDATE matches the SQLite
// upsert semantics.
type RelationshipRepository struct {
	db pgQuerier
	ns string
}

// Upsert satisfies the corresponding ports method.
func (r RelationshipRepository) Upsert(ctx context.Context, relationships []domain.Relationship) error {
	if len(relationships) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin relationship upsert tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt := fmt.Sprintf(`
INSERT INTO %s (id, type, from_claim_id, to_claim_id, created_at, created_by)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (id) DO UPDATE SET
  type = EXCLUDED.type,
  from_claim_id = EXCLUDED.from_claim_id,
  to_claim_id = EXCLUDED.to_claim_id`, qualify(r.ns, "relationships"))
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

// ListByClaim satisfies the corresponding ports method.
func (r RelationshipRepository) ListByClaim(ctx context.Context, claimID string) ([]domain.Relationship, error) {
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`
SELECT id, type, from_claim_id, to_claim_id, created_at, created_by
FROM %s WHERE from_claim_id = $1 OR to_claim_id = $1`, qualify(r.ns, "relationships")), claimID)
	if err != nil {
		return nil, fmt.Errorf("list relationships by claim: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectRelationshipRows(rows)
}

// RepointEndpoint rewrites every relationship whose endpoints
// equal oldID to point at newID. Duplicates (existing edges with
// the same type+from+to) drop via the unique-edge index, surfaced
// by removing every leftover row pointing at oldID after the
// rewrite. Self-loops (newID-newID) are also dropped.
func (r RelationshipRepository) RepointEndpoint(ctx context.Context, oldID, newID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin repoint endpoint tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Postgres has no UPDATE OR IGNORE; use a delete-then-update
	// dance: pre-emptively drop rows that would conflict on the
	// unique index after rewriting, then update what's left.
	conflictFrom := fmt.Sprintf(`
DELETE FROM %s WHERE id IN (
  SELECT a.id FROM %s a
  WHERE a.from_claim_id = $1
    AND EXISTS (SELECT 1 FROM %s b
                WHERE b.type = a.type
                  AND b.from_claim_id = $2
                  AND b.to_claim_id = a.to_claim_id))`,
		qualify(r.ns, "relationships"),
		qualify(r.ns, "relationships"),
		qualify(r.ns, "relationships"))
	conflictTo := fmt.Sprintf(`
DELETE FROM %s WHERE id IN (
  SELECT a.id FROM %s a
  WHERE a.to_claim_id = $1
    AND EXISTS (SELECT 1 FROM %s b
                WHERE b.type = a.type
                  AND b.from_claim_id = a.from_claim_id
                  AND b.to_claim_id = $2))`,
		qualify(r.ns, "relationships"),
		qualify(r.ns, "relationships"),
		qualify(r.ns, "relationships"))
	for _, stmt := range []string{conflictFrom, conflictTo} {
		if _, err := tx.ExecContext(ctx, stmt, oldID, newID); err != nil {
			return fmt.Errorf("clear conflicting edges: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		fmt.Sprintf(`UPDATE %s SET from_claim_id = $1 WHERE from_claim_id = $2`, qualify(r.ns, "relationships")),
		newID, oldID,
	); err != nil {
		return fmt.Errorf("repoint from %s -> %s: %w", oldID, newID, err)
	}
	if _, err := tx.ExecContext(ctx,
		fmt.Sprintf(`UPDATE %s SET to_claim_id = $1 WHERE to_claim_id = $2`, qualify(r.ns, "relationships")),
		newID, oldID,
	); err != nil {
		return fmt.Errorf("repoint to %s -> %s: %w", oldID, newID, err)
	}
	if _, err := tx.ExecContext(ctx,
		fmt.Sprintf(`DELETE FROM %s WHERE from_claim_id = $1 AND to_claim_id = $1`, qualify(r.ns, "relationships")),
		newID,
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
		fmt.Sprintf(`DELETE FROM %s WHERE from_claim_id = $1 OR to_claim_id = $1`, qualify(r.ns, "relationships")),
		claimID,
	); err != nil {
		return fmt.Errorf("delete relationships for %s: %w", claimID, err)
	}
	return nil
}

// CountAll satisfies the corresponding ports method.
func (r RelationshipRepository) CountAll(ctx context.Context) (int64, error) {
	var n int64
	if err := r.db.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT COUNT(*) FROM %s`, qualify(r.ns, "relationships"),
	)).Scan(&n); err != nil {
		return 0, fmt.Errorf("count relationships: %w", err)
	}
	return n, nil
}

// CountByType satisfies the corresponding ports method.
func (r RelationshipRepository) CountByType(ctx context.Context, relType string) (int64, error) {
	var n int64
	if err := r.db.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT COUNT(*) FROM %s WHERE type = $1`, qualify(r.ns, "relationships"),
	), relType).Scan(&n); err != nil {
		return 0, fmt.Errorf("count relationships by type: %w", err)
	}
	return n, nil
}

// DeleteAll satisfies the corresponding ports method.
func (r RelationshipRepository) DeleteAll(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, fmt.Sprintf(
		`DELETE FROM %s`, qualify(r.ns, "relationships"),
	)); err != nil {
		return fmt.Errorf("delete all relationships: %w", err)
	}
	return nil
}

// ListAll satisfies the corresponding ports method.
func (r RelationshipRepository) ListAll(ctx context.Context) ([]domain.Relationship, error) {
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`
SELECT id, type, from_claim_id, to_claim_id, created_at, created_by
FROM %s ORDER BY created_at ASC`, qualify(r.ns, "relationships")))
	if err != nil {
		return nil, fmt.Errorf("list all relationships: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectRelationshipRows(rows)
}

// ListByClaimIDs satisfies the corresponding ports method.
func (r RelationshipRepository) ListByClaimIDs(ctx context.Context, claimIDs []string) ([]domain.Relationship, error) {
	if len(claimIDs) == 0 {
		return []domain.Relationship{}, nil
	}
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`
SELECT id, type, from_claim_id, to_claim_id, created_at, created_by
FROM %s WHERE from_claim_id = ANY($1) OR to_claim_id = ANY($1)`, qualify(r.ns, "relationships")), pgArray(claimIDs))
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
