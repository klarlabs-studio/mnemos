package postgres

import (
	"context"
	"fmt"

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
	return r.list(ctx, "event_id", ids)
}

// ListDependents implements [ports.EventDependencyRepository.ListDependents].
func (r EventDependencyRepository) ListDependents(ctx context.Context, ids []string) ([]domain.EventDependency, error) {
	return r.list(ctx, "depends_on", ids)
}

func (r EventDependencyRepository) list(ctx context.Context, column string, ids []string) ([]domain.EventDependency, error) {
	out := make([]domain.EventDependency, 0)
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`
SELECT event_id, depends_on, kind, signal_type, reference_text, created_at, created_by FROM %s
WHERE %s = ANY($1) ORDER BY created_at, event_id, depends_on`, qualify(r.ns, "event_dependencies"), column), pgArray(ids))
	if err != nil {
		return nil, fmt.Errorf("list event dependencies: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			d            domain.EventDependency
			kind, signal string
		)
		if err := rows.Scan(&d.EventID, &d.DependsOn, &kind, &signal, &d.Reference, &d.CreatedAt, &d.CreatedBy); err != nil {
			return nil, fmt.Errorf("scan event dependency: %w", err)
		}
		d.Kind, d.Signal = domain.DependencyKind(kind), domain.DependencySignal(signal)
		d.CreatedAt = d.CreatedAt.UTC()
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate event dependencies: %w", err)
	}
	return out, nil
}
