package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// journalDefaultLimit caps an unbounded journal read.
const journalDefaultLimit = 100

// JournalRepository is the Postgres-backed append-only cognitive journal (ADR 0018).
type JournalRepository struct {
	db pgQuerier
	ns string
}

// Append persists journal entries. Idempotent on id (ON CONFLICT DO NOTHING).
func (r JournalRepository) Append(ctx context.Context, entries []domain.JournalEntry) error {
	stmt := fmt.Sprintf(`INSERT INTO %s (id, at, run_id, kind, subject_id, data)
VALUES ($1, $2, $3, $4, $5, $6::jsonb)
ON CONFLICT (id) DO NOTHING`, qualify(r.ns, "cognitive_journal"))
	for _, e := range entries {
		if err := e.Validate(); err != nil {
			return fmt.Errorf("invalid journal entry: %w", err)
		}
		data := e.Data
		if data == "" {
			data = "{}"
		}
		if _, err := r.db.ExecContext(ctx, stmt, e.ID, e.At.UTC(), e.RunID, e.Kind, e.SubjectID, data); err != nil {
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
		rows, err = r.db.QueryContext(ctx, fmt.Sprintf(
			`SELECT id, at, run_id, kind, subject_id, data FROM %s ORDER BY at DESC, id DESC LIMIT $1`, qualify(r.ns, "cognitive_journal")), limit)
	} else {
		rows, err = r.db.QueryContext(ctx, fmt.Sprintf(
			`SELECT id, at, run_id, kind, subject_id, data FROM %s WHERE kind = $1 ORDER BY at DESC, id DESC LIMIT $2`, qualify(r.ns, "cognitive_journal")), kind, limit)
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
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(
		`SELECT id, at, run_id, kind, subject_id, data FROM %s WHERE subject_id = $1 ORDER BY at DESC, id DESC LIMIT $2`, qualify(r.ns, "cognitive_journal")), subjectID, limit)
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
		if err := rows.Scan(&e.ID, &at, &e.RunID, &e.Kind, &e.SubjectID, &e.Data); err != nil {
			return nil, fmt.Errorf("scan journal row: %w", err)
		}
		e.At = at.UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}
