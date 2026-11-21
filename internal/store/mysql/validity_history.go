package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/clock"
	"go.klarlabs.de/mnemos/internal/domain"
)

// SetValidity sets a claim's valid_to, or clears it when validTo is zero, and
// records the change in claim_validity_history (ADR 0034).
func (r ClaimRepository) SetValidity(ctx context.Context, claimID string, validTo time.Time, note domain.ChangeNote) error {
	found, err := r.changeValidity(ctx, claimID, domain.ValidityFieldValidTo, validTo, note)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("claim %s: %w", claimID, sql.ErrNoRows)
	}
	return nil
}

// Archive stops surfacing a claim from at (ADR 0034); valid time and status
// are untouched. An unknown claim is a no-op.
func (r ClaimRepository) Archive(ctx context.Context, claimID string, at time.Time, note domain.ChangeNote) error {
	if at.IsZero() {
		return fmt.Errorf("archive %s: zero time", claimID)
	}
	_, err := r.changeValidity(ctx, claimID, domain.ValidityFieldArchivedAt, at, note)
	return err
}

// Unarchive clears archived_at on whichever of ids are archived and returns
// how many were.
func (r ClaimRepository) Unarchive(ctx context.Context, ids []string, note domain.ChangeNote) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin unarchive tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	placeholders, args := inPlaceholders(ids)
	//nolint:gosec // G202: placeholders are literal "?" tokens, not user input
	rows, err := tx.QueryContext(ctx, `SELECT id, archived_at FROM claims
		WHERE archived_at IS NOT NULL AND id IN (`+placeholders+`) FOR UPDATE`, args...)
	if err != nil {
		return 0, fmt.Errorf("list archived claims: %w", err)
	}
	type was struct {
		id string
		at time.Time
	}
	var cleared []was
	for rows.Next() {
		var w was
		if err := rows.Scan(&w.id, &w.at); err != nil {
			_ = rows.Close()
			return 0, err
		}
		w.at = w.at.UTC()
		cleared = append(cleared, w)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	now := clock.Now().UTC()
	for _, w := range cleared {
		if _, err := tx.ExecContext(ctx, `UPDATE claims SET archived_at = NULL WHERE id = ?`, w.id); err != nil {
			return 0, fmt.Errorf("unarchive %s: %w", w.id, err)
		}
		if err := r.insertValidityChange(ctx, tx, w.id, domain.ValidityFieldArchivedAt, w.at, time.Time{}, now, note); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit unarchive: %w", err)
	}
	return len(cleared), nil
}

// changeValidity writes one validity field and its history row in one
// transaction, and reports whether the claim exists. A write that leaves the
// value as it was records nothing.
func (r ClaimRepository) changeValidity(ctx context.Context, claimID string, field domain.ValidityField, to time.Time, note domain.ChangeNote) (bool, error) {
	to = storedTime(to)
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin %s tx: %w", field, err)
	}
	defer func() { _ = tx.Rollback() }()
	var from sql.NullTime
	//nolint:gosec // G201: field is one of two domain constants, not input
	err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT %s FROM claims WHERE id = ? FOR UPDATE`, field), claimID).Scan(&from)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s of %s: %w", field, claimID, err)
	}
	var fromTime time.Time
	if from.Valid {
		fromTime = from.Time.UTC()
	}
	if fromTime.Equal(to) {
		return true, nil
	}
	var stored any
	if !to.IsZero() {
		stored = to
	}
	//nolint:gosec // G201: field is one of two domain constants, not input
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE claims SET %s = ? WHERE id = ?`, field), stored, claimID); err != nil {
		return true, fmt.Errorf("set %s of %s: %w", field, claimID, err)
	}
	if err := r.insertValidityChange(ctx, tx, claimID, field, fromTime, to, clock.Now().UTC(), note); err != nil {
		return true, err
	}
	if err := tx.Commit(); err != nil {
		return true, fmt.Errorf("commit %s change: %w", field, err)
	}
	return true, nil
}

func (r ClaimRepository) insertValidityChange(ctx context.Context, tx *sql.Tx, claimID string, field domain.ValidityField, from, to, at time.Time, note domain.ChangeNote) error {
	by := note.ChangedBy
	if by == "" {
		by = domain.SystemUser
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO claim_validity_history (claim_id, field, from_value, to_value, changed_at, reason, changed_by)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		claimID, string(field), nullTime(from), nullTime(to), at, note.Reason, by); err != nil {
		return fmt.Errorf("record %s change of %s: %w", field, claimID, err)
	}
	return nil
}

// ListValidityHistory returns claimID's validity changes oldest first, or
// every claim's when claimID is empty.
func (r ClaimRepository) ListValidityHistory(ctx context.Context, claimID string) ([]domain.ValidityChange, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT claim_id, field, from_value, to_value, changed_at, reason, changed_by
		FROM claim_validity_history WHERE (? = '' OR claim_id = ?) ORDER BY id ASC`, claimID, claimID)
	if err != nil {
		return nil, fmt.Errorf("list validity history: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.ValidityChange
	for rows.Next() {
		var (
			c        domain.ValidityChange
			field    string
			from, to sql.NullTime
		)
		if err := rows.Scan(&c.ClaimID, &field, &from, &to, &c.ChangedAt, &c.Reason, &c.ChangedBy); err != nil {
			return nil, err
		}
		c.Field = domain.ValidityField(field)
		if from.Valid {
			c.From = from.Time.UTC()
		}
		if to.Valid {
			c.To = to.Time.UTC()
		}
		c.ChangedAt = c.ChangedAt.UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// storedTime is t as a DATETIME(6) column returns it: UTC, microseconds.
func storedTime(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	return t.UTC().Truncate(time.Microsecond)
}
