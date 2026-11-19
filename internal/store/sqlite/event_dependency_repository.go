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

// Declare implements [ports.EventDependencyRepository.Declare]. In one
// transaction: a tombstone for the pair is copied into the edit history, then
// one upsert inserts a new pair, revives a removed one, or leaves a live one
// alone (no row affected), which is ErrDependencyExists and rolls the copy
// back. Both statements are conditional on the stored row being removed, so
// they agree on what they saw.
func (r EventDependencyRepository) Declare(ctx context.Context, d domain.EventDependency) (domain.EventDependency, error) {
	if err := d.Validate(); err != nil {
		return domain.EventDependency{}, fmt.Errorf("invalid event dependency: %w", err)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.EventDependency{}, fmt.Errorf("begin declare tx: %w", err)
	}
	defer rollbackTx(tx)
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO event_dependency_history (event_id, depends_on, kind, signal_type, reference_text, valid_from, recorded_by, valid_to, removed_by, removal_reason)
		 SELECT event_id, depends_on, kind, signal_type, reference_text, created_at, created_by, removed_at, removed_by, removal_reason
		 FROM event_dependencies WHERE event_id = ? AND depends_on = ? AND removed_at IS NOT NULL`,
		d.EventID, d.DependsOn,
	); err != nil {
		return domain.EventDependency{}, fmt.Errorf("keep the removed interval of %s -> %s: %w", d.EventID, d.DependsOn, err)
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO event_dependencies (event_id, depends_on, kind, signal_type, reference_text, created_at, created_by, removed_at, removed_by, removal_reason)
		 VALUES (?, ?, ?, ?, ?, ?, ?, NULL, '', '')
		 ON CONFLICT (event_id, depends_on) DO UPDATE SET
		   kind = excluded.kind, signal_type = excluded.signal_type, reference_text = excluded.reference_text,
		   created_at = excluded.created_at, created_by = excluded.created_by, removed_at = NULL, removed_by = '', removal_reason = ''
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
	if err := tx.Commit(); err != nil {
		return domain.EventDependency{}, fmt.Errorf("commit declare tx: %w", err)
	}
	return r.get(ctx, d.EventID, d.DependsOn)
}

// Remove implements [ports.EventDependencyRepository.Remove].
func (r EventDependencyRepository) Remove(ctx context.Context, eventID, dependsOn, removedBy, reason string, at time.Time) (domain.EventDependency, error) {
	res, err := r.db.ExecContext(ctx,
		`UPDATE event_dependencies SET removed_at = ?, removed_by = ?, removal_reason = ? WHERE event_id = ? AND depends_on = ? AND removed_at IS NULL`,
		at.UTC().Format(time.RFC3339Nano), actorOr(removedBy), reason, eventID, dependsOn,
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

// get reads back the current row of one pair, live or removed.
func (r EventDependencyRepository) get(ctx context.Context, eventID, dependsOn string) (domain.EventDependency, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+dependencyColumns+` FROM event_dependencies WHERE event_id = ? AND depends_on = ?`, eventID, dependsOn)
	if err != nil {
		return domain.EventDependency{}, fmt.Errorf("read event dependency %s -> %s: %w", eventID, dependsOn, err)
	}
	defer closeRows(rows)
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return domain.EventDependency{}, err
		}
		return domain.EventDependency{}, fmt.Errorf("%w: %s -> %s", domain.ErrDependencyNotFound, eventID, dependsOn)
	}
	return scanDependency(rows)
}

// dependencyColumns is the projection of event_dependencies; the history's
// historyColumns names the same values in the same order.
const (
	dependencyColumns = `event_id, depends_on, kind, signal_type, reference_text, created_at, created_by, removed_at, removed_by, removal_reason`
	historyColumns    = `event_id, depends_on, kind, signal_type, reference_text, valid_from, recorded_by, valid_to, removed_by, removal_reason`
)

func (r EventDependencyRepository) list(ctx context.Context, column string, ids []string, withRemoved bool) ([]domain.EventDependency, error) {
	query := func(n int) string {
		in := column + ` IN (` + placeholders(n) + `)`
		if !withRemoved {
			return `SELECT ` + dependencyColumns + ` FROM event_dependencies WHERE ` + in + ` AND removed_at IS NULL ORDER BY created_at, event_id, depends_on`
		}
		// Every interval: the current rows and the ones a revival superseded.
		return `SELECT ` + dependencyColumns + ` FROM event_dependencies WHERE ` + in +
			` UNION ALL SELECT ` + historyColumns + ` FROM event_dependency_history WHERE ` + in +
			` ORDER BY 6, 1, 2`
	}
	out := make([]domain.EventDependency, 0)
	err := inChunks(ids, func(chunk []string) error {
		args := anyArgs(chunk)
		if withRemoved {
			args = append(args, anyArgs(chunk)...)
		}
		rows, err := r.db.QueryContext(ctx, query(len(chunk)), args...) //nolint:gosec // G202: column is a literal, placeholders are literal "?"
		if err != nil {
			return fmt.Errorf("list event dependencies: %w", err)
		}
		defer closeRows(rows)
		for rows.Next() {
			d, err := scanDependency(rows)
			if err != nil {
				return err
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

func scanDependency(rows *sql.Rows) (domain.EventDependency, error) {
	var (
		d            domain.EventDependency
		kind, signal string
		created      string
		removed      sql.NullString
	)
	if err := rows.Scan(&d.EventID, &d.DependsOn, &kind, &signal, &d.Reference, &created, &d.CreatedBy, &removed, &d.RemovedBy, &d.RemovalReason); err != nil {
		return domain.EventDependency{}, fmt.Errorf("scan event dependency: %w", err)
	}
	d.Kind, d.Signal = domain.DependencyKind(kind), domain.DependencySignal(signal)
	var err error
	if d.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return domain.EventDependency{}, fmt.Errorf("parse event dependency created_at: %w", err)
	}
	if removed.Valid {
		at, err := time.Parse(time.RFC3339Nano, removed.String)
		if err != nil {
			return domain.EventDependency{}, fmt.Errorf("parse event dependency removed_at: %w", err)
		}
		d.RemovedAt = &at
	}
	return d, nil
}
