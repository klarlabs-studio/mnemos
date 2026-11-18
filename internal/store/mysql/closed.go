package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// Closed evidence links and associations (ADR 0036 §8). A closed row leaves
// the live table in the same transaction that records it in the closed table,
// so every live read stays exactly as it was; only internal/recorded reads the
// closed tables. Tenants are isolated by namespace database, as for every
// other table.

// CloseEvidence implements [ports.ClaimRepository.CloseEvidence].
func (r ClaimRepository) CloseEvidence(ctx context.Context, claimID, eventID string, closedAt time.Time, note domain.ChangeNote) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin close evidence tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
INSERT IGNORE INTO closed_claim_evidence (claim_id, event_id, created_at, closed_at, closed_by, reason)
SELECT claim_id, event_id, created_at, ?, ?, ? FROM claim_evidence WHERE claim_id = ? AND event_id = ?`,
		closedAt.UTC().Truncate(time.Microsecond), actorOr(note.ChangedBy), note.Reason, claimID, eventID,
	); err != nil {
		return fmt.Errorf("record closed evidence %s -> %s: %w", claimID, eventID, err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM claim_evidence WHERE claim_id = ? AND event_id = ?`, claimID, eventID,
	); err != nil {
		return fmt.Errorf("close evidence %s -> %s: %w", claimID, eventID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit close evidence tx: %w", err)
	}
	return nil
}

// ListClosedEvidenceByClaimIDs implements [ports.ClaimRepository.ListClosedEvidenceByClaimIDs].
func (r ClaimRepository) ListClosedEvidenceByClaimIDs(ctx context.Context, claimIDs []string) ([]domain.ClosedEvidence, error) {
	return r.listClosedEvidence(ctx, "claim_id", claimIDs)
}

// ListClosedEvidenceByEventIDs implements [ports.ClaimRepository.ListClosedEvidenceByEventIDs].
func (r ClaimRepository) ListClosedEvidenceByEventIDs(ctx context.Context, eventIDs []string) ([]domain.ClosedEvidence, error) {
	return r.listClosedEvidence(ctx, "event_id", eventIDs)
}

func (r ClaimRepository) listClosedEvidence(ctx context.Context, column string, ids []string) ([]domain.ClosedEvidence, error) {
	out := make([]domain.ClosedEvidence, 0)
	if len(ids) == 0 {
		return out, nil
	}
	placeholders, args := inPlaceholders(ids)
	//nolint:gosec // G202: column is one of two literals, placeholders are literal "?" tokens
	rows, err := r.db.QueryContext(ctx, `SELECT claim_id, event_id, created_at, closed_at, closed_by, reason
FROM closed_claim_evidence WHERE `+column+` IN (`+placeholders+`) ORDER BY closed_at`, args...)
	if err != nil {
		return nil, fmt.Errorf("list closed evidence: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			l       domain.ClosedEvidence
			created sql.NullTime
		)
		if err := rows.Scan(&l.ClaimID, &l.EventID, &created, &l.ClosedAt, &l.ClosedBy, &l.Reason); err != nil {
			return nil, fmt.Errorf("scan closed evidence: %w", err)
		}
		if created.Valid {
			l.CreatedAt = created.Time.UTC()
		}
		l.ClosedAt = l.ClosedAt.UTC()
		out = append(out, l)
	}
	return out, rows.Err()
}

const closedRelationshipColumns = `id, type, from_claim_id, to_claim_id, created_at, created_by, strength, derived_by, closed_at, closed_by, reason`

// ListClosedByClaimIDs implements [ports.RelationshipRepository.ListClosedByClaimIDs].
func (r RelationshipRepository) ListClosedByClaimIDs(ctx context.Context, claimIDs []string) ([]domain.ClosedRelationship, error) {
	if len(claimIDs) == 0 {
		return []domain.ClosedRelationship{}, nil
	}
	placeholders, args := inPlaceholders(claimIDs)
	//nolint:gosec // G202: placeholders are literal "?" tokens
	return r.queryClosed(ctx, `SELECT `+closedRelationshipColumns+` FROM closed_relationships
WHERE from_claim_id IN (`+placeholders+`) OR to_claim_id IN (`+placeholders+`) ORDER BY closed_at`,
		append(args, args...)...)
}

// ListAllClosed implements [ports.RelationshipRepository.ListAllClosed].
func (r RelationshipRepository) ListAllClosed(ctx context.Context) ([]domain.ClosedRelationship, error) {
	return r.queryClosed(ctx, `SELECT `+closedRelationshipColumns+` FROM closed_relationships ORDER BY closed_at`)
}

func (r RelationshipRepository) queryClosed(ctx context.Context, query string, args ...any) ([]domain.ClosedRelationship, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list closed relationships: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]domain.ClosedRelationship, 0)
	for rows.Next() {
		var (
			rel domain.ClosedRelationship
			typ string
		)
		if err := rows.Scan(&rel.ID, &typ, &rel.FromClaimID, &rel.ToClaimID, &rel.CreatedAt, &rel.CreatedBy,
			&rel.Strength, &rel.DerivedBy, &rel.ClosedAt, &rel.ClosedBy, &rel.Reason); err != nil {
			return nil, fmt.Errorf("scan closed relationship: %w", err)
		}
		rel.Type = domain.RelationshipType(typ)
		rel.CreatedAt = rel.CreatedAt.UTC()
		rel.ClosedAt = rel.ClosedAt.UTC()
		out = append(out, rel)
	}
	return out, rows.Err()
}

// stageClosedEdges copies every edge a repoint of oldID onto newID can drop
// (any touching oldID or newID: a conflict drops one side of a collision) into
// closed_relationships as it is now, closed at closedAt. After the rewrite,
// keepClosedEdges discards the staged rows of edges that survived, so only the
// dropped ones stay closed, with the endpoints they had.
func stageClosedEdges(ctx context.Context, tx *sql.Tx, oldID, newID string, closedAt time.Time) error {
	if _, err := tx.ExecContext(ctx, `
INSERT IGNORE INTO closed_relationships (`+closedRelationshipColumns+`)
SELECT id, type, from_claim_id, to_claim_id, created_at, created_by, strength, derived_by, ?, ?, ?
  FROM relationships WHERE from_claim_id IN (?, ?) OR to_claim_id IN (?, ?)`,
		closedAt, domain.SystemUser, domain.MergeClosedReason(oldID, newID), oldID, newID, oldID, newID,
	); err != nil {
		return fmt.Errorf("stage closed edges of %s: %w", oldID, err)
	}
	return nil
}

// keepClosedEdges discards the staged closures of edges still live.
func keepClosedEdges(ctx context.Context, tx *sql.Tx, closedAt time.Time) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM closed_relationships WHERE closed_at = ? AND id IN (SELECT id FROM relationships)`, closedAt,
	); err != nil {
		return fmt.Errorf("discard staged closures: %w", err)
	}
	return nil
}
