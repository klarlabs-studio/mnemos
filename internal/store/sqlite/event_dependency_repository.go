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
	return r.list(ctx, "event_id", ids, false)
}

// ListDependents implements [ports.EventDependencyRepository.ListDependents].
func (r EventDependencyRepository) ListDependents(ctx context.Context, ids []string) ([]domain.EventDependency, error) {
	return r.list(ctx, "depends_on", ids, false)
}

// ListByEventIDsWithRemoved implements [ports.EventDependencyRepository.ListByEventIDsWithRemoved].
func (r EventDependencyRepository) ListByEventIDsWithRemoved(ctx context.Context, ids []string) ([]domain.EventDependency, error) {
	return r.list(ctx, "event_id", ids, true)
}

// ListDependentsWithRemoved implements [ports.EventDependencyRepository.ListDependentsWithRemoved].
func (r EventDependencyRepository) ListDependentsWithRemoved(ctx context.Context, ids []string) ([]domain.EventDependency, error) {
	return r.list(ctx, "depends_on", ids, true)
}

// Declare implements [ports.EventDependencyRepository.Declare]. One upsert:
// a new pair is inserted, a removed one revived, and a live one left alone
// (no row affected), which is ErrDependencyExists.
func (r EventDependencyRepository) Declare(ctx context.Context, d domain.EventDependency) (domain.EventDependency, error) {
	if err := d.Validate(); err != nil {
		return domain.EventDependency{}, fmt.Errorf("invalid event dependency: %w", err)
	}
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO event_dependencies (event_id, depends_on, kind, signal_type, reference_text, created_at, created_by, removed_at, removed_by)
		 VALUES (?, ?, ?, ?, ?, ?, ?, NULL, '')
		 ON CONFLICT (event_id, depends_on) DO UPDATE SET
		   kind = excluded.kind, signal_type = excluded.signal_type, reference_text = excluded.reference_text,
		   created_at = excluded.created_at, created_by = excluded.created_by, removed_at = NULL, removed_by = ''
		 WHERE event_dependencies.removed_at IS NOT NULL`,
		d.EventID, d.DependsOn, string(d.Kind), string(d.Signal), d.Reference,
		d.CreatedAt.UTC().Format(time.RFC3339Nano), actorOr(d.CreatedBy),
	)
	if err != nil {
		return domain.EventDependency{}, fmt.Errorf("declare event dependency %s -> %s: %w", d.EventID, d.DependsOn, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return domain.EventDependency{}, fmt.Errorf("declare event dependency %s -> %s: %w", d.EventID, d.DependsOn, err)
	} else if n == 0 {
		return domain.EventDependency{}, fmt.Errorf("%w: %s -> %s", domain.ErrDependencyExists, d.EventID, d.DependsOn)
	}
	return r.get(ctx, d.EventID, d.DependsOn)
}

// Remove implements [ports.EventDependencyRepository.Remove].
func (r EventDependencyRepository) Remove(ctx context.Context, eventID, dependsOn, removedBy string, at time.Time) (domain.EventDependency, error) {
	res, err := r.db.ExecContext(ctx,
		`UPDATE event_dependencies SET removed_at = ?, removed_by = ? WHERE event_id = ? AND depends_on = ? AND removed_at IS NULL`,
		at.UTC().Format(time.RFC3339Nano), actorOr(removedBy), eventID, dependsOn,
	)
	if err != nil {
		return domain.EventDependency{}, fmt.Errorf("remove event dependency %s -> %s: %w", eventID, dependsOn, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return domain.EventDependency{}, fmt.Errorf("remove event dependency %s -> %s: %w", eventID, dependsOn, err)
	} else if n == 0 {
		return domain.EventDependency{}, fmt.Errorf("%w: %s -> %s", domain.ErrDependencyNotFound, eventID, dependsOn)
	}
	return r.get(ctx, eventID, dependsOn)
}

// get reads back one stored row, live or removed.
func (r EventDependencyRepository) get(ctx context.Context, eventID, dependsOn string) (domain.EventDependency, error) {
	rows, err := r.ListByEventIDsWithRemoved(ctx, []string{eventID})
	if err != nil {
		return domain.EventDependency{}, err
	}
	for _, d := range rows {
		if d.DependsOn == dependsOn {
			return d, nil
		}
	}
	return domain.EventDependency{}, fmt.Errorf("%w: %s -> %s", domain.ErrDependencyNotFound, eventID, dependsOn)
}

func (r EventDependencyRepository) list(ctx context.Context, column string, ids []string, withRemoved bool) ([]domain.EventDependency, error) {
	live := " AND removed_at IS NULL"
	if withRemoved {
		live = ""
	}
	out := make([]domain.EventDependency, 0)
	err := inChunks(ids, func(chunk []string) error {
		rows, err := r.db.QueryContext(ctx,
			`SELECT event_id, depends_on, kind, signal_type, reference_text, created_at, created_by, removed_at, removed_by FROM event_dependencies WHERE `+column+` IN (`+placeholders(len(chunk))+`)`+live+` ORDER BY created_at, event_id, depends_on`, //nolint:gosec // G202: column and live are literals, placeholders are literal "?"
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
				removed      sql.NullString
			)
			if err := rows.Scan(&d.EventID, &d.DependsOn, &kind, &signal, &d.Reference, &created, &d.CreatedBy, &removed, &d.RemovedBy); err != nil {
				return fmt.Errorf("scan event dependency: %w", err)
			}
			d.Kind, d.Signal = domain.DependencyKind(kind), domain.DependencySignal(signal)
			if d.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
				return fmt.Errorf("parse event dependency created_at: %w", err)
			}
			if removed.Valid {
				at, err := time.Parse(time.RFC3339Nano, removed.String)
				if err != nil {
					return fmt.Errorf("parse event dependency removed_at: %w", err)
				}
				d.RemovedAt = &at
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
