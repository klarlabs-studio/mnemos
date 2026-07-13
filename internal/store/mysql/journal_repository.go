package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// journalDefaultLimit caps an unbounded journal read.
const journalDefaultLimit = 100

// JournalRepository is the MySQL-backed append-only cognitive journal (ADR 0018).
type JournalRepository struct {
	db *sql.DB
}

// Append persists journal entries. Idempotent on id (INSERT IGNORE).
func (r JournalRepository) Append(ctx context.Context, entries []domain.JournalEntry) error {
	for _, e := range entries {
		if err := e.Validate(); err != nil {
			return fmt.Errorf("invalid journal entry: %w", err)
		}
		data := e.Data
		if data == "" {
			data = "{}"
		}
		if _, err := r.db.ExecContext(ctx,
			`INSERT IGNORE INTO cognitive_journal (id, at, run_id, kind, subject_id, data)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			e.ID, e.At.UTC(), e.RunID, e.Kind, e.SubjectID, data); err != nil {
			return fmt.Errorf("append journal entry %s: %w", e.ID, err)
		}
	}
	return nil
}

// List returns the most recent entries of the given kind (empty = any), newest first.
func (r JournalRepository) List(ctx context.Context, kind string, limit int) ([]domain.JournalEntry, error) {
	if limit <= 0 {
		limit = journalDefaultLimit
	}
	var (
		rows *sql.Rows
		err  error
	)
	if kind == "" {
		rows, err = r.db.QueryContext(ctx,
			`SELECT id, at, run_id, kind, subject_id, data FROM cognitive_journal ORDER BY at DESC, id DESC LIMIT ?`, limit)
	} else {
		rows, err = r.db.QueryContext(ctx,
			`SELECT id, at, run_id, kind, subject_id, data FROM cognitive_journal WHERE kind = ? ORDER BY at DESC, id DESC LIMIT ?`, kind, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("list journal: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanJournalRows(rows)
}

// ListBySubject returns the most recent entries for one subject id, newest first.
func (r JournalRepository) ListBySubject(ctx context.Context, subjectID string, limit int) ([]domain.JournalEntry, error) {
	if limit <= 0 {
		limit = journalDefaultLimit
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, at, run_id, kind, subject_id, data FROM cognitive_journal WHERE subject_id = ? ORDER BY at DESC, id DESC LIMIT ?`, subjectID, limit)
	if err != nil {
		return nil, fmt.Errorf("list journal by subject: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanJournalRows(rows)
}

func scanJournalRows(rows *sql.Rows) ([]domain.JournalEntry, error) {
	out := make([]domain.JournalEntry, 0)
	for rows.Next() {
		var e domain.JournalEntry
		var at time.Time
		var data []byte
		if err := rows.Scan(&e.ID, &at, &e.RunID, &e.Kind, &e.SubjectID, &data); err != nil {
			return nil, fmt.Errorf("scan journal row: %w", err)
		}
		e.At = at.UTC()
		e.Data = string(data)
		out = append(out, e)
	}
	return out, rows.Err()
}
