package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// Closed evidence links and associations (ADR 0036 §8). A closed row leaves
// the live table in the same transaction that records it in the closed table,
// so every live read stays exactly as it was; only internal/recorded reads the
// closed tables.

// CloseEvidence implements [ports.ClaimRepository.CloseEvidence].
func (r ClaimRepository) CloseEvidence(ctx context.Context, claimID, eventID string, closedAt time.Time, note domain.ChangeNote) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin close evidence tx: %w", err)
	}
	defer rollbackTx(tx)
	res, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO closed_claim_evidence (claim_id, event_id, created_at, closed_at, closed_by, reason)
		 SELECT claim_id, event_id, created_at, ?, ?, ? FROM claim_evidence WHERE claim_id = ? AND event_id = ?`,
		closedAt.UTC().Format(time.RFC3339Nano), actorOr(note.ChangedBy), note.Reason, claimID, eventID)
	if err != nil {
		return fmt.Errorf("record closed evidence %s -> %s: %w", claimID, eventID, err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		// Not live: nothing to close. A failed count is treated the same way,
		// since the delete below is then a no-op too.
		return tx.Commit()
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
	err := inChunks(ids, func(chunk []string) error {
		rows, err := r.db.QueryContext(ctx,
			`SELECT claim_id, event_id, created_at, closed_at, closed_by, reason FROM closed_claim_evidence WHERE `+column+` IN (`+placeholders(len(chunk))+`)`, //nolint:gosec // G202: column is one of two literals, placeholders are literal "?"
			anyArgs(chunk)...)
		if err != nil {
			return fmt.Errorf("list closed evidence: %w", err)
		}
		defer closeRows(rows)
		for rows.Next() {
			var (
				l                 domain.ClosedEvidence
				created, closedAt string
			)
			if err := rows.Scan(&l.ClaimID, &l.EventID, &created, &closedAt, &l.ClosedBy, &l.Reason); err != nil {
				return fmt.Errorf("scan closed evidence: %w", err)
			}
			if l.CreatedAt, err = parseOptionalTime(created); err != nil {
				return fmt.Errorf("parse closed evidence created_at: %w", err)
			}
			if l.ClosedAt, err = time.Parse(time.RFC3339Nano, closedAt); err != nil {
				return fmt.Errorf("parse closed evidence closed_at: %w", err)
			}
			out = append(out, l)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ClosedAt.Before(out[j].ClosedAt) })
	return out, nil
}

// ListClosedByClaimIDs implements [ports.RelationshipRepository.ListClosedByClaimIDs].
func (r RelationshipRepository) ListClosedByClaimIDs(ctx context.Context, claimIDs []string) ([]domain.ClosedRelationship, error) {
	out := make([]domain.ClosedRelationship, 0)
	err := inChunks(claimIDs, func(chunk []string) error {
		marks := placeholders(len(chunk))
		args := append(anyArgs(chunk), anyArgs(chunk)...)
		part, err := r.queryClosed(ctx, ` WHERE from_claim_id IN (`+marks+`) OR to_claim_id IN (`+marks+`)`, args...)
		out = append(out, part...)
		return err
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ClosedAt.Before(out[j].ClosedAt) })
	return out, nil
}

// ListAllClosed implements [ports.RelationshipRepository.ListAllClosed].
func (r RelationshipRepository) ListAllClosed(ctx context.Context) ([]domain.ClosedRelationship, error) {
	return r.queryClosed(ctx, ` ORDER BY closed_at`)
}

func (r RelationshipRepository) queryClosed(ctx context.Context, where string, args ...any) ([]domain.ClosedRelationship, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, type, from_claim_id, to_claim_id, created_at, created_by, strength, derived_by, closed_at, closed_by, reason FROM closed_relationships`+where, //nolint:gosec // G202: where is built from literals and "?" placeholders
		args...)
	if err != nil {
		return nil, fmt.Errorf("list closed relationships: %w", err)
	}
	defer closeRows(rows)
	var out []domain.ClosedRelationship
	for rows.Next() {
		var (
			rel               domain.ClosedRelationship
			typ               string
			created, closedAt string
		)
		if err := rows.Scan(&rel.ID, &typ, &rel.FromClaimID, &rel.ToClaimID, &created, &rel.CreatedBy,
			&rel.Strength, &rel.DerivedBy, &closedAt, &rel.ClosedBy, &rel.Reason); err != nil {
			return nil, fmt.Errorf("scan closed relationship: %w", err)
		}
		rel.Type = domain.RelationshipType(typ)
		if rel.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
			return nil, fmt.Errorf("parse closed relationship created_at: %w", err)
		}
		if rel.ClosedAt, err = time.Parse(time.RFC3339Nano, closedAt); err != nil {
			return nil, fmt.Errorf("parse closed relationship closed_at: %w", err)
		}
		out = append(out, rel)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate closed relationships: %w", err)
	}
	return out, nil
}

// stageClosedEdges copies every edge a repoint of oldID onto newID can drop
// (those touching oldID, and a newID self-loop) into closed_relationships as it
// is now, closed at closedAt. The caller then rewrites and drops edges, and
// calls keepClosedEdges to discard the staged rows of edges that survived, so
// only the dropped ones stay closed, with the endpoints they had.
func stageClosedEdges(ctx context.Context, tx *sql.Tx, oldID, newID, closedAt string) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO closed_relationships (id, type, from_claim_id, to_claim_id, created_at, created_by, strength, derived_by, closed_at, closed_by, reason)
		 SELECT id, type, from_claim_id, to_claim_id, created_at, created_by, strength, derived_by, ?, ?, ? FROM relationships
		 WHERE from_claim_id = ? OR to_claim_id = ? OR (from_claim_id = ? AND to_claim_id = ?)`,
		closedAt, domain.SystemUser, domain.MergeClosedReason(oldID, newID), oldID, oldID, newID, newID); err != nil {
		return fmt.Errorf("stage closed edges of %s: %w", oldID, err)
	}
	return nil
}

// keepClosedEdges discards the staged closures of edges still live.
func keepClosedEdges(ctx context.Context, tx *sql.Tx, closedAt string) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM closed_relationships WHERE closed_at = ? AND id IN (SELECT id FROM relationships)`, closedAt,
	); err != nil {
		return fmt.Errorf("discard staged closures: %w", err)
	}
	return nil
}
