package mnemos

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
)

// concurrentDependencyEdit makes another writer's edit commit right after the
// call's own live read of the pair, the check in front of the governed write.
// It runs once.
type concurrentDependencyEdit struct {
	ports.EventDependencyRepository
	once *sync.Once
	edit func(ctx context.Context, deps ports.EventDependencyRepository) error
	err  *error
}

func (c concurrentDependencyEdit) ListByEventIDs(ctx context.Context, ids []string) ([]domain.EventDependency, error) {
	rows, err := c.EventDependencyRepository.ListByEventIDs(ctx, ids)
	c.once.Do(func() { *c.err = c.edit(ctx, c.EventDependencyRepository) })
	return rows, err
}

// The library's own governed kernel keeps the store's sentinel too: a
// concurrent edit that lands between the check and the write is
// ErrDependencyExists / ErrDependencyNotFound, as the check would have said.
func TestEventDependencies_ConcurrentEditsKeepTheirSentinel(t *testing.T) {
	mem, err := New(WithSQLite(filepath.Join(t.TempDir(), "race.db")), WithPassiveMode(), WithActor("lib-operator"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	m, ok := mem.(*memory)
	if !ok {
		t.Fatalf("Memory is %T", mem)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	for _, id := range []string{"ev_src", "ev_copy"} {
		if err := mem.RememberEvent(ctx, Event{ID: id, At: now, Type: "observation", Content: "source " + id}); err != nil {
			t.Fatalf("RememberEvent %s: %v", id, err)
		}
	}
	inner := m.conn.Dependencies
	t.Cleanup(func() { m.conn.Dependencies = inner })
	race := func(edit func(context.Context, ports.EventDependencyRepository) error) *error {
		var editErr error
		m.conn.Dependencies = concurrentDependencyEdit{EventDependencyRepository: inner, once: &sync.Once{}, edit: edit, err: &editErr}
		return &editErr
	}

	editErr := race(func(ctx context.Context, deps ports.EventDependencyRepository) error {
		_, err := deps.Declare(ctx, domain.EventDependency{EventID: "ev_copy", DependsOn: "ev_src", Kind: domain.DependencyCites,
			Signal: domain.SignalDeclared, CreatedAt: time.Now().UTC(), CreatedBy: "other-writer"})
		return err
	})
	if _, err := mem.DeclareEventDependency(ctx, "ev_copy", "ev_src", "copies", ""); !errors.Is(err, ErrDependencyExists) {
		t.Errorf("raced declaration: err = %v, want ErrDependencyExists", err)
	}
	if *editErr != nil {
		t.Fatalf("concurrent declaration: %v", *editErr)
	}

	editErr = race(func(ctx context.Context, deps ports.EventDependencyRepository) error {
		_, err := deps.Remove(ctx, "ev_copy", "ev_src", "other-writer", "", time.Now().UTC())
		return err
	})
	if _, err := mem.RemoveEventDependency(ctx, "ev_copy", "ev_src", ""); !errors.Is(err, ErrDependencyNotFound) {
		t.Errorf("raced removal: err = %v, want ErrDependencyNotFound", err)
	}
	if *editErr != nil {
		t.Fatalf("concurrent removal: %v", *editErr)
	}
}
