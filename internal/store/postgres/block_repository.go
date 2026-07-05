package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// BlockRepository persists an agent's working-memory blocks in the configured
// Postgres namespace. Per-tenant isolation is handled transparently by the ADR
// 0007 tenant column + row-level security (the connection pins the mnemos.tenant
// GUC), so this repository issues plain SQL like every other.
type BlockRepository struct {
	db *sql.DB
	ns string
}

// Get returns the block for (owner, label), or ok=false when no row exists.
func (r BlockRepository) Get(ctx context.Context, owner, label string) (domain.WorkingMemoryBlock, bool, error) {
	var (
		content   string
		updatedAt time.Time
	)
	err := r.db.QueryRowContext(ctx, fmt.Sprintf(`
SELECT content, updated_at FROM %s WHERE owner = $1 AND label = $2`, qualify(r.ns, "working_memory_blocks")),
		owner, label).Scan(&content, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.WorkingMemoryBlock{}, false, nil
	}
	if err != nil {
		return domain.WorkingMemoryBlock{}, false, fmt.Errorf("get block %s/%s: %w", owner, label, err)
	}
	return domain.WorkingMemoryBlock{Owner: owner, Label: label, Content: content, UpdatedAt: updatedAt}, true, nil
}

// List returns every block for an owner, label-ordered.
func (r BlockRepository) List(ctx context.Context, owner string) ([]domain.WorkingMemoryBlock, error) {
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`
SELECT label, content, updated_at FROM %s WHERE owner = $1 ORDER BY label ASC`, qualify(r.ns, "working_memory_blocks")), owner)
	if err != nil {
		return nil, fmt.Errorf("list blocks for %s: %w", owner, err)
	}
	defer rows.Close()
	out := make([]domain.WorkingMemoryBlock, 0)
	for rows.Next() {
		var label, content string
		var updatedAt time.Time
		if err := rows.Scan(&label, &content, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan block: %w", err)
		}
		out = append(out, domain.WorkingMemoryBlock{Owner: owner, Label: label, Content: content, UpdatedAt: updatedAt})
	}
	return out, rows.Err()
}

// Upsert writes the block atomically on the (owner, label) primary key.
func (r BlockRepository) Upsert(ctx context.Context, block domain.WorkingMemoryBlock) error {
	if block.Owner == "" || block.Label == "" {
		return errors.New("block upsert: owner and label required")
	}
	updatedAt := block.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	_, err := r.db.ExecContext(ctx, fmt.Sprintf(`
INSERT INTO %s (owner, label, content, updated_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (owner, label) DO UPDATE SET
  content = excluded.content,
  updated_at = excluded.updated_at`, qualify(r.ns, "working_memory_blocks")),
		block.Owner, block.Label, block.Content, updatedAt.UTC())
	if err != nil {
		return fmt.Errorf("upsert block %s/%s: %w", block.Owner, block.Label, err)
	}
	return nil
}

// Delete removes the block for (owner, label). A missing block is a no-op.
func (r BlockRepository) Delete(ctx context.Context, owner, label string) error {
	_, err := r.db.ExecContext(ctx, fmt.Sprintf(`
DELETE FROM %s WHERE owner = $1 AND label = $2`, qualify(r.ns, "working_memory_blocks")), owner, label)
	if err != nil {
		return fmt.Errorf("delete block %s/%s: %w", owner, label, err)
	}
	return nil
}
