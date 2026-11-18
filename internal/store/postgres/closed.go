package postgres

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
// closed tables. Both tables are in the ADR-0007 RLS scoped array.

// CloseEvidence implements [ports.ClaimRepository.CloseEvidence].
func (r ClaimRepository) CloseEvidence(ctx context.Context, claimID, eventID string, closedAt time.Time, note domain.ChangeNote) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin close evidence tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
INSERT INTO %s (claim_id, event_id, created_at, closed_at, closed_by, reason)
SELECT claim_id, event_id, created_at, $3, $4, $5 FROM %s WHERE claim_id = $1 AND event_id = $2
ON CONFLICT DO NOTHING`, qualify(r.ns, "closed_claim_evidence"), qualify(r.ns, "claim_evidence")),
		claimID, eventID, closedAt.UTC(), actorOr(note.ChangedBy), note.Reason,
	); err != nil {
		return fmt.Errorf("record closed evidence %s -> %s: %w", claimID, eventID, err)
	}
	if _, err := tx.ExecContext(ctx,
		fmt.Sprintf(`DELETE FROM %s WHERE claim_id = $1 AND event_id = $2`, qualify(r.ns, "claim_evidence")),
		claimID, eventID,
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
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`
SELECT claim_id, event_id, created_at, closed_at, closed_by, reason FROM %s
WHERE %s = ANY($1) ORDER BY closed_at`, qualify(r.ns, "closed_claim_evidence"), column), pgArray(ids))
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
	return r.queryClosed(ctx, fmt.Sprintf(`SELECT %s FROM %s
WHERE from_claim_id = ANY($1) OR to_claim_id = ANY($1) ORDER BY closed_at`,
		closedRelationshipColumns, qualify(r.ns, "closed_relationships")), pgArray(claimIDs))
}

// ListAllClosed implements [ports.RelationshipRepository.ListAllClosed].
func (r RelationshipRepository) ListAllClosed(ctx context.Context) ([]domain.ClosedRelationship, error) {
	return r.queryClosed(ctx, fmt.Sprintf(`SELECT %s FROM %s ORDER BY closed_at`,
		closedRelationshipColumns, qualify(r.ns, "closed_relationships")))
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
// (any touching oldID or newID: the prune can drop either side of a collision)
// into closed_relationships as it is now, closed at closedAt. After the
// rewrite, keepClosedEdges discards the staged rows of edges that survived, so
// only the dropped ones stay closed, with the endpoints they had.
func (r RelationshipRepository) stageClosedEdges(ctx context.Context, tx *sql.Tx, oldID, newID string, closedAt time.Time) error {
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
INSERT INTO %s (%s)
SELECT id, type, from_claim_id, to_claim_id, created_at, created_by, strength, derived_by, $3, $4, $5
  FROM %s WHERE from_claim_id IN ($1, $2) OR to_claim_id IN ($1, $2)
ON CONFLICT DO NOTHING`, qualify(r.ns, "closed_relationships"), closedRelationshipColumns, qualify(r.ns, "relationships")),
		oldID, newID, closedAt, domain.SystemUser, domain.MergeClosedReason(oldID, newID),
	); err != nil {
		return fmt.Errorf("stage closed edges of %s: %w", oldID, err)
	}
	return nil
}

// keepClosedEdges discards the staged closures of edges still live.
func (r RelationshipRepository) keepClosedEdges(ctx context.Context, tx *sql.Tx, closedAt time.Time) error {
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
DELETE FROM %s WHERE closed_at = $1 AND id IN (SELECT id FROM %s)`,
		qualify(r.ns, "closed_relationships"), qualify(r.ns, "relationships")), closedAt,
	); err != nil {
		return fmt.Errorf("discard staged closures: %w", err)
	}
	return nil
}
