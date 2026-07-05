package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// BlockRepository is the SQLite-backed implementation of [ports.BlockRepository].
// Persists an agent's working-memory blocks in the working_memory_blocks side
// table, keyed by (owner, label).
type BlockRepository struct {
	db *sql.DB
}

// NewBlockRepository returns a repository bound to the given *sql.DB.
func NewBlockRepository(db *sql.DB) BlockRepository {
	return BlockRepository{db: db}
}

// Get returns the block for (owner, label), or ok=false when no row exists.
func (r BlockRepository) Get(ctx context.Context, owner, label string) (domain.WorkingMemoryBlock, bool, error) {
	var (
		content   string
		updatedAt string
	)
	err := r.db.QueryRowContext(ctx, `
SELECT content, updated_at FROM working_memory_blocks
WHERE owner = ? AND label = ?`, owner, label).Scan(&content, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.WorkingMemoryBlock{}, false, nil
	}
	if err != nil {
		return domain.WorkingMemoryBlock{}, false, fmt.Errorf("get block %s/%s: %w", owner, label, err)
	}
	return blockFromRow(owner, label, content, updatedAt)
}

// List returns every block for an owner, label-ordered.
func (r BlockRepository) List(ctx context.Context, owner string) ([]domain.WorkingMemoryBlock, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT label, content, updated_at FROM working_memory_blocks
WHERE owner = ? ORDER BY label ASC`, owner)
	if err != nil {
		return nil, fmt.Errorf("list blocks for %s: %w", owner, err)
	}
	defer rows.Close()
	out := make([]domain.WorkingMemoryBlock, 0)
	for rows.Next() {
		var label, content, updatedAt string
		if err := rows.Scan(&label, &content, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan block: %w", err)
		}
		b, _, err := blockFromRow(owner, label, content, updatedAt)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Upsert writes the block atomically on the (owner, label) primary key.
func (r BlockRepository) Upsert(ctx context.Context, block domain.WorkingMemoryBlock) error {
	if block.Owner == "" || block.Label == "" {
		return errors.New("block upsert: owner and label required")
	}
	var updatedAt string
	if !block.UpdatedAt.IsZero() {
		updatedAt = block.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO working_memory_blocks (owner, label, content, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(owner, label) DO UPDATE SET
  content = excluded.content,
  updated_at = excluded.updated_at
`, block.Owner, block.Label, block.Content, updatedAt)
	if err != nil {
		return fmt.Errorf("upsert block %s/%s: %w", block.Owner, block.Label, err)
	}
	return nil
}

// Delete removes the block for (owner, label). A missing block is a no-op.
func (r BlockRepository) Delete(ctx context.Context, owner, label string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM working_memory_blocks WHERE owner = ? AND label = ?`, owner, label)
	if err != nil {
		return fmt.Errorf("delete block %s/%s: %w", owner, label, err)
	}
	return nil
}

func blockFromRow(owner, label, content, updatedAt string) (domain.WorkingMemoryBlock, bool, error) {
	b := domain.WorkingMemoryBlock{Owner: owner, Label: label, Content: content}
	if updatedAt != "" {
		t, err := time.Parse(time.RFC3339Nano, updatedAt)
		if err != nil {
			return domain.WorkingMemoryBlock{}, false, fmt.Errorf("parse block updated_at: %w", err)
		}
		b.UpdatedAt = t
	}
	return b, true, nil
}
