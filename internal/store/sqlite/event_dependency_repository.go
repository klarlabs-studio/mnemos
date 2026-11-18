package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// EventDependencyRepository stores source dependence between episodes in
// event_dependencies (ADR 0038).
type EventDependencyRepository struct {
	db *sql.DB
}

// NewEventDependencyRepository returns the repository over db.
func NewEventDependencyRepository(db *sql.DB) EventDependencyRepository {
	return EventDependencyRepository{db: db}
}

// Append implements [ports.EventDependencyRepository.Append].
func (r EventDependencyRepository) Append(ctx context.Context, deps []domain.EventDependency) error {
	for _, d := range deps {
		if err := d.Validate(); err != nil {
			return fmt.Errorf("invalid event dependency: %w", err)
		}
	}
	if len(deps) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin event dependency tx: %w", err)
	}
	defer rollbackTx(tx)
	for _, d := range deps {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO event_dependencies (event_id, depends_on, kind, signal_type, reference_text, created_at, created_by)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			d.EventID, d.DependsOn, string(d.Kind), string(d.Signal), d.Reference,
			d.CreatedAt.UTC().Format(time.RFC3339Nano), actorOr(d.CreatedBy),
		); err != nil {
			return fmt.Errorf("insert event dependency %s -> %s: %w", d.EventID, d.DependsOn, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit event dependency tx: %w", err)
	}
	return nil
}

// ListByEventIDs implements [ports.EventDependencyRepository.ListByEventIDs].
func (r EventDependencyRepository) ListByEventIDs(ctx context.Context, ids []string) ([]domain.EventDependency, error) {
	return r.list(ctx, "event_id", ids)
}

// ListDependents implements [ports.EventDependencyRepository.ListDependents].
func (r EventDependencyRepository) ListDependents(ctx context.Context, ids []string) ([]domain.EventDependency, error) {
	return r.list(ctx, "depends_on", ids)
}

func (r EventDependencyRepository) list(ctx context.Context, column string, ids []string) ([]domain.EventDependency, error) {
	out := make([]domain.EventDependency, 0)
	err := inChunks(ids, func(chunk []string) error {
		rows, err := r.db.QueryContext(ctx,
			`SELECT event_id, depends_on, kind, signal_type, reference_text, created_at, created_by FROM event_dependencies WHERE `+column+` IN (`+placeholders(len(chunk))+`) ORDER BY created_at, event_id, depends_on`, //nolint:gosec // G202: column is one of two literals, placeholders are literal "?"
			anyArgs(chunk)...)
		if err != nil {
			return fmt.Errorf("list event dependencies: %w", err)
		}
		defer closeRows(rows)
		for rows.Next() {
			var (
				d            domain.EventDependency
				kind, signal string
				created      string
			)
			if err := rows.Scan(&d.EventID, &d.DependsOn, &kind, &signal, &d.Reference, &created, &d.CreatedBy); err != nil {
				return fmt.Errorf("scan event dependency: %w", err)
			}
			d.Kind, d.Signal = domain.DependencyKind(kind), domain.DependencySignal(signal)
			if d.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
				return fmt.Errorf("parse event dependency created_at: %w", err)
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
