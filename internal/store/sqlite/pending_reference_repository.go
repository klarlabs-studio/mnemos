package sqlite

import (
	"context"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// AppendPending implements [ports.EventDependencyRepository.AppendPending].
func (r EventDependencyRepository) AppendPending(ctx context.Context, refs []domain.PendingReference) error {
	for _, p := range refs {
		if err := p.Validate(); err != nil {
			return fmt.Errorf("invalid pending reference: %w", err)
		}
	}
	if len(refs) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin pending reference tx: %w", err)
	}
	defer rollbackTx(tx)
	for _, p := range refs {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO pending_references (event_id, lookup_key, created_at) VALUES (?, ?, ?)`,
			p.EventID, p.LookupKey, p.CreatedAt.UTC().Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("insert pending reference %s %q: %w", p.EventID, p.LookupKey, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit pending reference tx: %w", err)
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
	err := inChunks(values, func(chunk []string) error {
		rows, err := r.db.QueryContext(ctx,
			`SELECT event_id, lookup_key, created_at FROM pending_references WHERE `+column+` IN (`+placeholders(len(chunk))+`)`, //nolint:gosec // G202: column is one of two literals, placeholders are literal "?"
			anyArgs(chunk)...)
		if err != nil {
			return fmt.Errorf("list pending references: %w", err)
		}
		defer closeRows(rows)
		for rows.Next() {
			var (
				p       domain.PendingReference
				created string
			)
			if err := rows.Scan(&p.EventID, &p.LookupKey, &created); err != nil {
				return fmt.Errorf("scan pending reference: %w", err)
			}
			if p.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
				return fmt.Errorf("parse pending reference created_at: %w", err)
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	domain.SortPendingReferences(out)
	return out, nil
}

// DeletePending implements [ports.EventDependencyRepository.DeletePending].
func (r EventDependencyRepository) DeletePending(ctx context.Context, refs []domain.PendingReference) error {
	if len(refs) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin pending reference delete tx: %w", err)
	}
	defer rollbackTx(tx)
	for _, p := range refs {
		if _, err := tx.ExecContext(ctx, `DELETE FROM pending_references WHERE event_id = ? AND lookup_key = ?`, p.EventID, p.LookupKey); err != nil {
			return fmt.Errorf("delete pending reference %s %q: %w", p.EventID, p.LookupKey, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit pending reference delete tx: %w", err)
	}
	return nil
}
