package mysql

import (
	"context"
	"fmt"

	"go.klarlabs.de/mnemos/internal/domain"
)

// AppendPending implements [ports.EventDependencyRepository.AppendPending].
func (r EventDependencyRepository) AppendPending(ctx context.Context, refs []domain.PendingReference) error {
	for _, p := range refs {
		if err := p.Validate(); err != nil {
			return fmt.Errorf("invalid pending reference: %w", err)
		}
	}
	for _, p := range refs {
		if _, err := r.db.ExecContext(ctx,
			`INSERT IGNORE INTO pending_references (event_id, lookup_key, created_at) VALUES (?, ?, ?)`,
			p.EventID, p.LookupKey, p.CreatedAt.UTC(),
		); err != nil {
			return fmt.Errorf("insert pending reference %s %q: %w", p.EventID, p.LookupKey, err)
		}
	}
	return nil
}

// ListPendingByKeys implements [ports.EventDependencyRepository.ListPendingByKeys].
func (r EventDependencyRepository) ListPendingByKeys(ctx context.Context, keys []string) ([]domain.PendingReference, error) {
	return r.listPending(ctx, "lookup_key", keys)
}

// ListPendingByEventIDs implements [ports.EventDependencyRepository.ListPendingByEventIDs].
func (r EventDependencyRepository) ListPendingByEventIDs(ctx context.Context, ids []string) ([]domain.PendingReference, error) {
	return r.listPending(ctx, "event_id", ids)
}

func (r EventDependencyRepository) listPending(ctx context.Context, column string, values []string) ([]domain.PendingReference, error) {
	out := make([]domain.PendingReference, 0)
	if len(values) == 0 {
		return out, nil
	}
	placeholders, args := inPlaceholders(values)
	//nolint:gosec // G202: column is one of two literals, placeholders are literal "?" tokens
	rows, err := r.db.QueryContext(ctx, `SELECT event_id, lookup_key, created_at FROM pending_references WHERE `+column+` IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("list pending references: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var p domain.PendingReference
		if err := rows.Scan(&p.EventID, &p.LookupKey, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan pending reference: %w", err)
		}
		p.CreatedAt = p.CreatedAt.UTC()
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending references: %w", err)
	}
	domain.SortPendingReferences(out)
	return out, nil
}

// DeletePending implements [ports.EventDependencyRepository.DeletePending].
func (r EventDependencyRepository) DeletePending(ctx context.Context, refs []domain.PendingReference) error {
	for _, p := range refs {
		if _, err := r.db.ExecContext(ctx, `DELETE FROM pending_references WHERE event_id = ? AND lookup_key = ?`, p.EventID, p.LookupKey); err != nil {
			return fmt.Errorf("delete pending reference %s %q: %w", p.EventID, p.LookupKey, err)
		}
	}
	return nil
}
