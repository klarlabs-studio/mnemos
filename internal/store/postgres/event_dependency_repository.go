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

// Declare implements [ports.EventDependencyRepository.Declare]. One upsert:
// a new pair is inserted, a removed one revived, and a live one left alone
// (no row affected), which is ErrDependencyExists.
func (r EventDependencyRepository) Declare(ctx context.Context, d domain.EventDependency) (domain.EventDependency, error) {
	if err := d.Validate(); err != nil {
		return domain.EventDependency{}, fmt.Errorf("invalid event dependency: %w", err)
	}
	res, err := r.db.ExecContext(ctx, fmt.Sprintf(`
INSERT INTO %s AS d (event_id, depends_on, kind, signal_type, reference_text, created_at, created_by, removed_at, removed_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, NULL, '')
ON CONFLICT (event_id, depends_on) DO UPDATE SET
  kind = EXCLUDED.kind, signal_type = EXCLUDED.signal_type, reference_text = EXCLUDED.reference_text,
  created_at = EXCLUDED.created_at, created_by = EXCLUDED.created_by, removed_at = NULL, removed_by = ''
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
	return r.get(ctx, d.EventID, d.DependsOn)
}

// Remove implements [ports.EventDependencyRepository.Remove].
func (r EventDependencyRepository) Remove(ctx context.Context, eventID, dependsOn, removedBy string, at time.Time) (domain.EventDependency, error) {
	res, err := r.db.ExecContext(ctx, fmt.Sprintf(`
UPDATE %s SET removed_at = $1, removed_by = $2 WHERE event_id = $3 AND depends_on = $4 AND removed_at IS NULL`,
		qualify(r.ns, "event_dependencies")),
		at.UTC(), actorOr(removedBy), eventID, dependsOn,
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
	out := make([]domain.EventDependency, 0)
	if len(ids) == 0 {
		return out, nil
	}
	live := " AND removed_at IS NULL"
	if withRemoved {
		live = ""
	}
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`
SELECT event_id, depends_on, kind, signal_type, reference_text, created_at, created_by, removed_at, removed_by FROM %s
WHERE %s = ANY($1)%s ORDER BY created_at, event_id, depends_on`, qualify(r.ns, "event_dependencies"), column, live), pgArray(ids))
	if err != nil {
		return nil, fmt.Errorf("list event dependencies: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			d            domain.EventDependency
			kind, signal string
			removed      sql.NullTime
		)
		if err := rows.Scan(&d.EventID, &d.DependsOn, &kind, &signal, &d.Reference, &d.CreatedAt, &d.CreatedBy, &removed, &d.RemovedBy); err != nil {
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
