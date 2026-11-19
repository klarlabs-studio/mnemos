package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	driver "github.com/go-sql-driver/mysql"

	"go.klarlabs.de/mnemos/internal/domain"
)

// EventDependencyRepository stores source dependence between episodes in
// event_dependencies (ADR 0038).
type EventDependencyRepository struct {
	db *sql.DB
}

// Append implements [ports.EventDependencyRepository.Append].
func (r EventDependencyRepository) Append(ctx context.Context, deps []domain.EventDependency) error {
	for _, d := range deps {
		if err := d.Validate(); err != nil {
			return fmt.Errorf("invalid event dependency: %w", err)
		}
	}
	for _, d := range deps {
		if _, err := r.db.ExecContext(ctx,
			`INSERT IGNORE INTO event_dependencies (event_id, depends_on, kind, signal_type, reference_text, created_at, created_by)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			d.EventID, d.DependsOn, string(d.Kind), string(d.Signal), d.Reference, d.CreatedAt.UTC(), actorOr(d.CreatedBy),
		); err != nil {
			return fmt.Errorf("insert event dependency %s -> %s: %w", d.EventID, d.DependsOn, err)
		}
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

// mysqlDuplicateEntry is MySQL's ER_DUP_ENTRY.
const mysqlDuplicateEntry = 1062

// Declare implements [ports.EventDependencyRepository.Declare]. MySQL's
// upsert has no conditional "leave a live row alone" form (its assignments
// see each other's results, left to right), so the pair is locked and read in
// a transaction: a missing row is inserted, a removed one revived after its
// tombstone is copied into the edit history, and a live one is
// ErrDependencyExists. A concurrent insert of the same pair surfaces as a
// duplicate entry, which is the same conflict.
func (r EventDependencyRepository) Declare(ctx context.Context, d domain.EventDependency) (domain.EventDependency, error) {
	if err := d.Validate(); err != nil {
		return domain.EventDependency{}, fmt.Errorf("invalid event dependency: %w", err)
	}
	exists := fmt.Errorf("%w: %s -> %s", domain.ErrDependencyExists, d.EventID, d.DependsOn)
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.EventDependency{}, fmt.Errorf("begin declare tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var removed sql.NullTime
	err = tx.QueryRowContext(ctx,
		`SELECT removed_at FROM event_dependencies WHERE event_id = ? AND depends_on = ? FOR UPDATE`, d.EventID, d.DependsOn,
	).Scan(&removed)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO event_dependencies (event_id, depends_on, kind, signal_type, reference_text, created_at, created_by, removed_at, removed_by, removal_reason)
			 VALUES (?, ?, ?, ?, ?, ?, ?, NULL, '', '')`,
			d.EventID, d.DependsOn, string(d.Kind), string(d.Signal), d.Reference, d.CreatedAt.UTC(), actorOr(d.CreatedBy),
		); err != nil {
			var myErr *driver.MySQLError
			if errors.As(err, &myErr) && myErr.Number == mysqlDuplicateEntry {
				return domain.EventDependency{}, exists
			}
			return domain.EventDependency{}, fmt.Errorf("declare event dependency %s -> %s: %w", d.EventID, d.DependsOn, err)
		}
	case err != nil:
		return domain.EventDependency{}, fmt.Errorf("read event dependency %s -> %s: %w", d.EventID, d.DependsOn, err)
	case !removed.Valid:
		return domain.EventDependency{}, exists
	default:
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO event_dependency_history (event_id, depends_on, kind, signal_type, reference_text, valid_from, recorded_by, valid_to, removed_by, removal_reason)
			 SELECT event_id, depends_on, kind, signal_type, reference_text, created_at, created_by, removed_at, removed_by, removal_reason
			 FROM event_dependencies WHERE event_id = ? AND depends_on = ?`,
			d.EventID, d.DependsOn,
		); err != nil {
			return domain.EventDependency{}, fmt.Errorf("keep the removed interval of %s -> %s: %w", d.EventID, d.DependsOn, err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE event_dependencies SET kind = ?, signal_type = ?, reference_text = ?, created_at = ?, created_by = ?, removed_at = NULL, removed_by = '', removal_reason = ''
			 WHERE event_id = ? AND depends_on = ?`,
			string(d.Kind), string(d.Signal), d.Reference, d.CreatedAt.UTC(), actorOr(d.CreatedBy), d.EventID, d.DependsOn,
		); err != nil {
			return domain.EventDependency{}, fmt.Errorf("revive event dependency %s -> %s: %w", d.EventID, d.DependsOn, err)
		}
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
		at.UTC(), actorOr(removedBy), reason, eventID, dependsOn,
	)
	if err != nil {
		return domain.EventDependency{}, fmt.Errorf("remove event dependency %s -> %s: %w", eventID, dependsOn, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return domain.EventDependency{}, fmt.Errorf("remove event dependency %s -> %s: %w", eventID, dependsOn, err)
	}
	if n == 0 {
		return domain.EventDependency{}, fmt.Errorf("%w: %s -> %s", domain.ErrDependencyNotFound, eventID, dependsOn)
	}
	return r.get(ctx, eventID, dependsOn)
}

// dependencyColumns is the projection of event_dependencies; historyColumns
// names the same values of event_dependency_history in the same order.
const (
	dependencyColumns = `event_id, depends_on, kind, signal_type, reference_text, created_at, created_by, removed_at, removed_by, removal_reason`
	historyColumns    = `event_id, depends_on, kind, signal_type, reference_text, valid_from, recorded_by, valid_to, removed_by, removal_reason`
)

// get reads back the current row of one pair, live or removed.
func (r EventDependencyRepository) get(ctx context.Context, eventID, dependsOn string) (domain.EventDependency, error) {
	rows, err := r.query(ctx, `SELECT `+dependencyColumns+` FROM event_dependencies WHERE event_id = ? AND depends_on = ?`, eventID, dependsOn)
	if err != nil {
		return domain.EventDependency{}, err
	}
	if len(rows) == 0 {
		return domain.EventDependency{}, fmt.Errorf("%w: %s -> %s", domain.ErrDependencyNotFound, eventID, dependsOn)
	}
	return rows[0], nil
}

func (r EventDependencyRepository) list(ctx context.Context, column string, ids []string, withRemoved bool) ([]domain.EventDependency, error) {
	if len(ids) == 0 {
		return []domain.EventDependency{}, nil
	}
	placeholders, args := inPlaceholders(ids)
	if !withRemoved {
		//nolint:gosec // G202: column is a literal, placeholders are literal "?" tokens
		return r.query(ctx, `SELECT `+dependencyColumns+` FROM event_dependencies WHERE `+column+` IN (`+placeholders+`) AND removed_at IS NULL ORDER BY created_at, event_id, depends_on`, args...)
	}
	// Every interval: the current rows and the ones a revival superseded.
	//nolint:gosec // G202: column is a literal, placeholders are literal "?" tokens
	return r.query(ctx, `SELECT `+dependencyColumns+` FROM event_dependencies WHERE `+column+` IN (`+placeholders+`)
UNION ALL SELECT `+historyColumns+` FROM event_dependency_history WHERE `+column+` IN (`+placeholders+`) ORDER BY 6, 1, 2`, append(args, args...)...)
}

func (r EventDependencyRepository) query(ctx context.Context, q string, args ...any) ([]domain.EventDependency, error) {
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list event dependencies: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]domain.EventDependency, 0)
	for rows.Next() {
		var (
			d            domain.EventDependency
			kind, signal string
			removed      sql.NullTime
		)
		if err := rows.Scan(&d.EventID, &d.DependsOn, &kind, &signal, &d.Reference, &d.CreatedAt, &d.CreatedBy, &removed, &d.RemovedBy, &d.RemovalReason); err != nil {
			return nil, fmt.Errorf("scan event dependency: %w", err)
		}
		d.Kind, d.Signal = domain.DependencyKind(kind), domain.DependencySignal(signal)
		d.CreatedAt = d.CreatedAt.UTC()
		if removed.Valid {
			at := removed.Time.UTC()
			d.RemovedAt = &at
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate event dependencies: %w", err)
	}
	return out, nil
}
