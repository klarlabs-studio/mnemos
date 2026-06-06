package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// ClaimVersionRepository persists the append-only version chain for
// claims (Refs #38). One row per Upsert — text, confidence, status
// snapshot plus the actor that wrote it.
type ClaimVersionRepository struct {
	db *sql.DB
}

// NewClaimVersionRepository binds the repository to *sql.DB.
func NewClaimVersionRepository(db *sql.DB) ClaimVersionRepository {
	return ClaimVersionRepository{db: db}
}

// Append writes the next version row. Version numbers are 1-based
// per claim id; the implementation reads the current max and
// increments. A single statement avoids a round-trip for the read,
// using the COALESCE-MAX subquery as the version value.
func (r ClaimVersionRepository) Append(ctx context.Context, v domain.ClaimVersion) error {
	if v.ClaimID == "" {
		return fmt.Errorf("append claim_version: claim_id required")
	}
	writtenAt := v.WrittenAt
	if writtenAt.IsZero() {
		writtenAt = time.Now().UTC()
	}
	writtenBy := v.WrittenBy
	if writtenBy == "" {
		writtenBy = "<system>"
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO claim_versions (claim_id, version, text, confidence, status, written_at, written_by)
VALUES (
	?,
	COALESCE((SELECT MAX(version) FROM claim_versions WHERE claim_id = ?), 0) + 1,
	?, ?, ?, ?, ?
)`, v.ClaimID, v.ClaimID, v.Text, v.Confidence, string(v.Status),
		writtenAt.UTC().Format(time.RFC3339Nano), writtenBy)
	if err != nil {
		return fmt.Errorf("append claim_version %s: %w", v.ClaimID, err)
	}
	return nil
}

// ListByClaim returns every version newest-first so a consumer can
// build a diff timeline without re-sorting client-side.
func (r ClaimVersionRepository) ListByClaim(ctx context.Context, claimID string) ([]domain.ClaimVersion, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT claim_id, version, text, confidence, status, written_at, written_by
FROM claim_versions
WHERE claim_id = ?
ORDER BY version DESC`, claimID)
	if err != nil {
		return nil, fmt.Errorf("list claim_versions %s: %w", claimID, err)
	}
	defer closeRows(rows)

	var out []domain.ClaimVersion
	for rows.Next() {
		var (
			v         domain.ClaimVersion
			status    string
			writtenAt string
		)
		if err := rows.Scan(&v.ClaimID, &v.Version, &v.Text, &v.Confidence, &status, &writtenAt, &v.WrittenBy); err != nil {
			return nil, fmt.Errorf("scan claim_version: %w", err)
		}
		v.Status = domain.ClaimStatus(status)
		t, perr := time.Parse(time.RFC3339Nano, writtenAt)
		if perr != nil {
			return nil, fmt.Errorf("parse written_at: %w", perr)
		}
		v.WrittenAt = t
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate claim_versions: %w", err)
	}
	return out, nil
}
