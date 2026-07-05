package memory

import (
	"context"
	"sort"

	"go.klarlabs.de/mnemos/internal/domain"
)

// BlockRepository is the in-memory implementation of [ports.BlockRepository].
// Backed by the shared state map so a memory Conn shares block state across
// every repo handle holding the same state pointer.
type BlockRepository struct {
	state *state
}

func blockKey(owner, label string) string { return owner + "\x00" + label }

// Get returns the block for (owner, label), or ok=false when none exists.
func (r BlockRepository) Get(_ context.Context, owner, label string) (domain.WorkingMemoryBlock, bool, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	b, ok := r.state.blocks[blockKey(owner, label)]
	return b, ok, nil
}

// List returns every block for an owner, label-ordered.
func (r BlockRepository) List(_ context.Context, owner string) ([]domain.WorkingMemoryBlock, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	out := make([]domain.WorkingMemoryBlock, 0)
	for _, b := range r.state.blocks {
		if b.Owner == owner {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out, nil
}

// Upsert writes the block under (owner, label).
func (r BlockRepository) Upsert(_ context.Context, block domain.WorkingMemoryBlock) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	r.state.blocks[blockKey(block.Owner, block.Label)] = block
	return nil
}

// Delete removes the block for (owner, label). A missing block is a no-op.
func (r BlockRepository) Delete(_ context.Context, owner, label string) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	delete(r.state.blocks, blockKey(owner, label))
	return nil
}
