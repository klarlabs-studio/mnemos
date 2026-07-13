package memory

import (
	"context"
	"fmt"

	"go.klarlabs.de/mnemos/internal/domain"
)

// journalDefaultLimit caps an unbounded journal read.
const journalDefaultLimit = 100

// JournalRepository is the in-memory append-only cognitive journal (ADR 0018).
type JournalRepository struct {
	state *state
}

// Append persists journal entries. Idempotent on id.
func (r JournalRepository) Append(_ context.Context, entries []domain.JournalEntry) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	seen := make(map[string]struct{}, len(r.state.journalEntries))
	for _, e := range r.state.journalEntries {
		seen[e.ID] = struct{}{}
	}
	for _, e := range entries {
		if err := e.Validate(); err != nil {
			return fmt.Errorf("invalid journal entry: %w", err)
		}
		if _, dup := seen[e.ID]; dup {
			continue // idempotent on id
		}
		cp := e
		cp.At = e.At.UTC()
		if cp.Data == "" {
			cp.Data = "{}"
		}
		r.state.journalEntries = append(r.state.journalEntries, cp)
		seen[e.ID] = struct{}{}
	}
	return nil
}

// List returns the most recent entries of the given kind (empty = any), newest first.
func (r JournalRepository) List(_ context.Context, kind string, limit int) ([]domain.JournalEntry, error) {
	if limit <= 0 {
		limit = journalDefaultLimit
	}
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	return r.recentMatching(limit, func(e domain.JournalEntry) bool {
		return kind == "" || e.Kind == kind
	}), nil
}

// ListBySubject returns the most recent entries for one subject id, newest first.
func (r JournalRepository) ListBySubject(_ context.Context, subjectID string, limit int) ([]domain.JournalEntry, error) {
	if limit <= 0 {
		limit = journalDefaultLimit
	}
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	return r.recentMatching(limit, func(e domain.JournalEntry) bool {
		return e.SubjectID == subjectID
	}), nil
}

// recentMatching walks the append-ordered log newest-first, returning up to limit
// entries that match. Caller holds the lock.
func (r JournalRepository) recentMatching(limit int, match func(domain.JournalEntry) bool) []domain.JournalEntry {
	out := make([]domain.JournalEntry, 0, limit)
	for i := len(r.state.journalEntries) - 1; i >= 0 && len(out) < limit; i-- {
		e := r.state.journalEntries[i]
		if match(e) {
			out = append(out, e)
		}
	}
	return out
}
