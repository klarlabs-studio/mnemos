package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// SetValidity sets a claim's valid_to, or clears it when validTo is zero
// (un-supersedes the claim, as when a resolution is reverted), and records the
// change in claim_validity_history (ADR 0034). An unknown claim is a no-op.
func (r ClaimRepository) SetValidity(ctx context.Context, claimID string, validTo time.Time, note domain.ChangeNote) error {
	return r.changeValidity(ctx, claimID, domain.ValidityFieldValidTo, validTo, note)
}

// Archive stops surfacing a claim from at (ADR 0034); valid time and status
// are untouched.
func (r ClaimRepository) Archive(ctx context.Context, claimID string, at time.Time, note domain.ChangeNote) error {
	if at.IsZero() {
		return fmt.Errorf("archive %s: zero time", claimID)
	}
	return r.changeValidity(ctx, claimID, domain.ValidityFieldArchivedAt, at, note)
}

// Unarchive clears archived_at on whichever of ids are archived and returns
// how many were.
func (r ClaimRepository) Unarchive(ctx context.Context, ids []string, note domain.ChangeNote) (int, error) {
	total := 0
	for start := 0; start < len(ids); start += unarchiveBatch {
		chunk := ids[start:min(start+unarchiveBatch, len(ids))]
		n, err := r.unarchiveChunk(ctx, chunk, note)
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// unarchiveBatch keeps each Unarchive statement under SQLite's bound-variable
// limit.
const unarchiveBatch = 500

func (r ClaimRepository) unarchiveChunk(ctx context.Context, ids []string, note domain.ChangeNote) (int, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin unarchive tx: %w", err)
	}
	defer rollbackTx(tx)
	rows, err := tx.QueryContext(ctx, `SELECT id, archived_at FROM claims WHERE archived_at <> '' AND id IN (`+placeholders(len(ids))+`)`, anyArgs(ids)...)
	if err != nil {
		return 0, fmt.Errorf("list archived claims: %w", err)
	}
	type archived struct{ id, at string }
	var found []archived
	for rows.Next() {
		var a archived
		if err := rows.Scan(&a.id, &a.at); err != nil {
			closeRows(rows)
			return 0, err
		}
		found = append(found, a)
	}
	closeRows(rows)
	if err := rows.Err(); err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	for _, a := range found {
		if _, err := tx.ExecContext(ctx, `UPDATE claims SET archived_at = '' WHERE id = ?`, a.id); err != nil {
			return 0, fmt.Errorf("unarchive %s: %w", a.id, err)
		}
		if err := insertValidityChange(ctx, tx, a.id, domain.ValidityFieldArchivedAt, a.at, "", now, note); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit unarchive: %w", err)
	}
	return len(found), nil
}

// changeValidity writes one validity field and its history row in one
// transaction. A write that leaves the value as it was records nothing.
func (r ClaimRepository) changeValidity(ctx context.Context, claimID string, field domain.ValidityField, to time.Time, note domain.ChangeNote) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin %s tx: %w", field, err)
	}
	defer rollbackTx(tx)
	var from sql.NullString
	//nolint:gosec // G201: field is one of two domain constants, not input
	err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT %s FROM claims WHERE id = ?`, field), claimID).Scan(&from)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s of %s: %w", field, claimID, err)
	}
	fromTime, err := parseOptionalTime(from.String)
	if err != nil {
		return fmt.Errorf("parse %s of %s: %w", field, claimID, err)
	}
	if fromTime.Equal(to) {
		return nil
	}
	toText := formatOptionalTime(to)
	var stored any = toText
	if field == domain.ValidityFieldValidTo && toText == "" {
		stored = nil // valid_to is nullable; NULL is its "open" value
	}
	//nolint:gosec // G201: field is one of two domain constants, not input
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE claims SET %s = ? WHERE id = ?`, field), stored, claimID); err != nil {
		return fmt.Errorf("set %s of %s: %w", field, claimID, err)
	}
	if err := insertValidityChange(ctx, tx, claimID, field, from.String, toText, time.Now().UTC(), note); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit %s change: %w", field, err)
	}
	return nil
}

func insertValidityChange(ctx context.Context, tx *sql.Tx, claimID string, field domain.ValidityField, from, to string, at time.Time, note domain.ChangeNote) error {
	by := note.ChangedBy
	if by == "" {
		by = domain.SystemUser
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO claim_validity_history (claim_id, field, from_value, to_value, changed_at, reason, changed_by) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		claimID, string(field), from, to, at.Format(time.RFC3339Nano), note.Reason, by); err != nil {
		return fmt.Errorf("record %s change of %s: %w", field, claimID, err)
	}
	return nil
}

// ListValidityHistory returns claimID's validity changes oldest first, or
// every claim's when claimID is empty.
func (r ClaimRepository) ListValidityHistory(ctx context.Context, claimID string) ([]domain.ValidityChange, error) {
	q := `SELECT claim_id, field, from_value, to_value, changed_at, reason, changed_by FROM claim_validity_history`
	var args []any
	if claimID != "" {
		q += ` WHERE claim_id = ?`
		args = append(args, claimID)
	}
	rows, err := r.db.QueryContext(ctx, q+` ORDER BY id ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("list validity history: %w", err)
	}
	defer closeRows(rows)
	var out []domain.ValidityChange
	for rows.Next() {
		var (
			c                     domain.ValidityChange
			field, from, to, chAt string
		)
		if err := rows.Scan(&c.ClaimID, &field, &from, &to, &chAt, &c.Reason, &c.ChangedBy); err != nil {
			return nil, err
		}
		c.Field = domain.ValidityField(field)
		if c.From, err = parseOptionalTime(from); err != nil {
			return nil, fmt.Errorf("parse validity history from_value: %w", err)
		}
		if c.To, err = parseOptionalTime(to); err != nil {
			return nil, fmt.Errorf("parse validity history to_value: %w", err)
		}
		if c.ChangedAt, err = time.Parse(time.RFC3339Nano, chAt); err != nil {
			return nil, fmt.Errorf("parse validity history changed_at: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// formatOptionalTime is parseOptionalTime's inverse: "" for the zero time.
func formatOptionalTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}
