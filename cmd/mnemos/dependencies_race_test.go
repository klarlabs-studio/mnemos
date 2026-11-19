package main

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"go.klarlabs.de/mcp/protocol"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
)

// concurrentDependencyEdit makes another writer's edit commit right after the
// caller's own live read of the pair (the check internal/dependency does in
// front of the governed write), the interleaving two concurrent requests
// produce. The edit runs once.
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

func raceDependencyEdits(t *testing.T, deps *ports.EventDependencyRepository, edit func(context.Context, ports.EventDependencyRepository) error) {
	t.Helper()
	inner := *deps
	var editErr error
	*deps = concurrentDependencyEdit{EventDependencyRepository: inner, once: &sync.Once{}, edit: edit, err: &editErr}
	t.Cleanup(func() {
		*deps = inner
		if editErr != nil {
			t.Errorf("concurrent edit: %v", editErr)
		}
	})
}

// A concurrent edit that lands between the request's check and its governed
// write is the same refusal over REST as one the check caught: 409 for a
// declaration the other writer made first, 404 for a removal it made first,
// never a 500.
func TestREST_EpisodeDependencies_ConcurrentEditsAreRefusals(t *testing.T) {
	f := newRunScopeFixture(t)
	f.seedRun(t, "run-a", "ev-a", "cl-a")
	f.seedRun(t, "run-a", "ev-a2", "cl-a2")
	write := f.openToken(t, []string{domain.ScopeClaimsWrite})

	raceDependencyEdits(t, &f.conn.Dependencies, func(ctx context.Context, deps ports.EventDependencyRepository) error {
		_, err := deps.Declare(ctx, domain.EventDependency{EventID: "ev-a2", DependsOn: "ev-a", Kind: domain.DependencyCites,
			Signal: domain.SignalDeclared, CreatedAt: time.Now().UTC(), CreatedBy: "other-writer"})
		return err
	})
	resp := f.declareDependency(t, "ev-a2", write, map[string]string{"depends_on": "ev-a", "kind": "copies"})
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("declaration raced by another declaration: status = %d, want 409", resp.StatusCode)
	}

	raceDependencyEdits(t, &f.conn.Dependencies, func(ctx context.Context, deps ports.EventDependencyRepository) error {
		_, err := deps.Remove(ctx, "ev-a2", "ev-a", "other-writer", time.Now().UTC())
		return err
	})
	if got := f.status(t, http.MethodDelete, "/v1/episodes/ev-a2/dependencies/ev-a", write); got != http.StatusNotFound {
		t.Errorf("removal raced by another removal: status = %d, want 404", got)
	}
}

// The CLI and MCP render the same race as a refusal: a user error, and -32602
// with the message instead of an opaque -32603.
func TestEpisodeDependencies_ConcurrentEditsAreRefusalsOnCLIAndMCP(t *testing.T) {
	setIsolatedDB(t)
	ctx := context.Background()
	w, err := openWriter(ctx)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	t.Cleanup(func() { closeWriter(w) })
	f := runScopeFixture{conn: w.Conn()}
	f.seedRun(t, "run-a", "ev-src", "cl-src")
	f.seedRun(t, "run-a", "ev-copy", "cl-copy")

	raceDependencyEdits(t, &w.Conn().Dependencies, func(ctx context.Context, deps ports.EventDependencyRepository) error {
		_, err := deps.Declare(ctx, domain.EventDependency{EventID: "ev-copy", DependsOn: "ev-src", Kind: domain.DependencyCites,
			Signal: domain.SignalDeclared, CreatedAt: time.Now().UTC(), CreatedBy: "other-writer"})
		return err
	})
	_, err = runDependencyDeclare(ctx, w, "ev-copy", "ev-src", "copies", "", "me", nil)
	if !errors.Is(err, domain.ErrDependencyExists) {
		t.Fatalf("raced declaration: err = %v, want ErrDependencyExists through the governed write", err)
	}
	var ue *MnemosError
	if cliErr := dependencyCLIError(err, dependenciesArgs{id: "ev-copy", declare: "ev-src"}); !errors.As(cliErr, &ue) || ue.Code != ExitUsage {
		t.Errorf("CLI error = %v, want a user error", cliErr)
	}
	var pe *protocol.Error
	if mcpErr := mcpDependencyError(err, "ev-copy", "ev-src"); !errors.As(mcpErr, &pe) || pe.Code != protocol.CodeInvalidParams {
		t.Errorf("MCP error = %v, want -32602", mcpErr)
	}

	raceDependencyEdits(t, &w.Conn().Dependencies, func(ctx context.Context, deps ports.EventDependencyRepository) error {
		_, err := deps.Remove(ctx, "ev-copy", "ev-src", "other-writer", time.Now().UTC())
		return err
	})
	if _, err := runDependencyRemove(ctx, w, "ev-copy", "ev-src", "me", nil); !errors.Is(err, domain.ErrDependencyNotFound) {
		t.Errorf("raced removal: err = %v, want ErrDependencyNotFound through the governed write", err)
	}
}
