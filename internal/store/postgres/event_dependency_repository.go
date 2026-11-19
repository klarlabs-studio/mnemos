package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// EventDependencyRepository stores source dependence between episodes in
// event_dependencies (ADR 0038). The table is in the RLS scoped list, so a
// tenant reads and writes only its own rows.
type EventDependencyRepository struct {
	db pgQuerier
	ns string
}

// Append implements [ports.EventDependencyRepository.Append].
func (r EventDependencyRepository) Append(ctx context.Context, deps []domain.EventDependency) error {
	for _, d := range deps {
		if err := d.Validate(); err != nil {
			return fmt.Errorf("invalid event dependency: %w", err)
		}
	}
	for _, d := range deps {
		if _, err := r.db.ExecContext(ctx, fmt.Sprintf(`
INSERT INTO %s (event_id, depends_on, kind, signal_type, reference_text, created_at, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (event_id, depends_on) DO NOTHING`, qualify(r.ns, "event_dependencies")),
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

// Declare implements [ports.EventDependencyRepository.Declare]. In one
// transaction: a tombstone for the pair is copied into the edit history, then
// one upsert inserts a new pair, revives a removed one, or leaves a live one
// alone (no row affected), which is ErrDependencyExists and rolls the copy
// back. A concurrent revival waits on the row lock and then sees a live row,
// so it, too, rolls its copy back.
func (r EventDependencyRepository) Declare(ctx context.Context, d domain.EventDependency) (domain.EventDependency, error) {
	if err := d.Validate(); err != nil {
		return domain.EventDependency{}, fmt.Errorf("invalid event dependency: %w", err)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.EventDependency{}, fmt.Errorf("begin declare tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
INSERT INTO %s (event_id, depends_on, kind, signal_type, reference_text, valid_from, recorded_by, valid_to, removed_by, removal_reason)
SELECT event_id, depends_on, kind, signal_type, reference_text, created_at, created_by, removed_at, removed_by, removal_reason
FROM %s WHERE event_id = $1 AND depends_on = $2 AND removed_at IS NOT NULL`,
		qualify(r.ns, "event_dependency_history"), qualify(r.ns, "event_dependencies")),
		d.EventID, d.DependsOn,
	); err != nil {
		return domain.EventDependency{}, fmt.Errorf("keep the removed interval of %s -> %s: %w", d.EventID, d.DependsOn, err)
	}
	res, err := tx.ExecContext(ctx, fmt.Sprintf(`
INSERT INTO %s AS d (event_id, depends_on, kind, signal_type, reference_text, created_at, created_by, removed_at, removed_by, removal_reason)
VALUES ($1, $2, $3, $4, $5, $6, $7, NULL, '', '')
ON CONFLICT (event_id, depends_on) DO UPDATE SET
  kind = EXCLUDED.kind, signal_type = EXCLUDED.signal_type, reference_text = EXCLUDED.reference_text,
  created_at = EXCLUDED.created_at, created_by = EXCLUDED.created_by, removed_at = NULL, removed_by = '', removal_reason = ''
WHERE d.removed_at IS NOT NULL`, qualify(r.ns, "event_dependencies")),
		d.EventID, d.DependsOn, string(d.Kind), string(d.Signal), d.Reference, d.CreatedAt.UTC(), actorOr(d.CreatedBy),
	)
	if err != nil {
		return domain.EventDependency{}, fmt.Errorf("declare event dependency %s -> %s: %w", d.EventID, d.DependsOn, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return domain.EventDependency{}, fmt.Errorf("declare event dependency %s -> %s: %w", d.EventID, d.DependsOn, err)
	}
	if n == 0 {
		return domain.EventDependency{}, fmt.Errorf("%w: %s -> %s", domain.ErrDependencyExists, d.EventID, d.DependsOn)
	}
	if err := tx.Commit(); err != nil {
		return domain.EventDependency{}, fmt.Errorf("commit declare tx: %w", err)
	}
	return r.get(ctx, d.EventID, d.DependsOn)
}

// Remove implements [ports.EventDependencyRepository.Remove].
func (r EventDependencyRepository) Remove(ctx context.Context, eventID, dependsOn, removedBy, reason string, at time.Time) (domain.EventDependency, error) {
	res, err := r.db.ExecContext(ctx, fmt.Sprintf(`
UPDATE %s SET removed_at = $1, removed_by = $2, removal_reason = $3 WHERE event_id = $4 AND depends_on = $5 AND removed_at IS NULL`,
		qualify(r.ns, "event_dependencies")),
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
	rows, err := r.query(ctx, fmt.Sprintf(`SELECT %s FROM %s WHERE event_id = $1 AND depends_on = $2`,
		dependencyColumns, qualify(r.ns, "event_dependencies")), eventID, dependsOn)
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
	if !withRemoved {
		return r.query(ctx, fmt.Sprintf(`SELECT %s FROM %s WHERE %s = ANY($1) AND removed_at IS NULL ORDER BY created_at, event_id, depends_on`,
			dependencyColumns, qualify(r.ns, "event_dependencies"), column), pgArray(ids))
	}
	// Every interval: the current rows and the ones a revival superseded.
	return r.query(ctx, fmt.Sprintf(`SELECT %s FROM %s WHERE %s = ANY($1)
UNION ALL SELECT %s FROM %s WHERE %s = ANY($1) ORDER BY 6, 1, 2`,
		dependencyColumns, qualify(r.ns, "event_dependencies"), column,
		historyColumns, qualify(r.ns, "event_dependency_history"), column), pgArray(ids))
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
