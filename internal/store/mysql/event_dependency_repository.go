package mysql

import (
	"context"
	"database/sql"
	"fmt"

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
	placeholders, args := inPlaceholders(ids)
	//nolint:gosec // G202: column is one of two literals, placeholders are literal "?" tokens
	rows, err := r.db.QueryContext(ctx, `SELECT event_id, depends_on, kind, signal_type, reference_text, created_at, created_by
FROM event_dependencies WHERE `+column+` IN (`+placeholders+`) ORDER BY created_at, event_id, depends_on`, args...)
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
