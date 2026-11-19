package grpc_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
	mnemosv1 "go.klarlabs.de/mnemos/proto/gen/mnemos/v1"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// concurrentDependencyEdit makes another writer's edit commit right after the
// request's own live read of the pair, the check in front of the governed
// write: the interleaving two concurrent requests produce. It runs once.
type concurrentDependencyEdit struct {
	ports.EventDependencyRepository
	once *sync.Once
	edit func(ctx context.Context, deps ports.EventDependencyRepository)
}

func (c concurrentDependencyEdit) ListByEventIDs(ctx context.Context, ids []string) ([]domain.EventDependency, error) {
	rows, err := c.EventDependencyRepository.ListByEventIDs(ctx, ids)
	c.once.Do(func() { c.edit(ctx, c.EventDependencyRepository) })
	return rows, err
}

// A concurrent edit landing between the check and the governed write is
// ALREADY_EXISTS / NOT_FOUND, the code the check itself would give, not
// INTERNAL.
func TestGRPC_EpisodeDependencies_ConcurrentEditsAreRefusals(t *testing.T) {
	client, conn, issuer := startSQLiteAuthServer(t)
	seedRetractable(t, conn)
	ctx := bearerCtx(t, mintToken(t, issuer, []string{domain.ScopeClaimsWrite}, nil))
	inner := conn.Dependencies
	t.Cleanup(func() { conn.Dependencies = inner })

	conn.Dependencies = concurrentDependencyEdit{EventDependencyRepository: inner, once: &sync.Once{}, edit: func(ctx context.Context, deps ports.EventDependencyRepository) {
		if _, err := deps.Declare(ctx, domain.EventDependency{EventID: "ev-other", DependsOn: "ev-mine", Kind: domain.DependencyCites,
			Signal: domain.SignalDeclared, CreatedAt: time.Now().UTC(), CreatedBy: "other-writer"}); err != nil {
			t.Errorf("concurrent declaration: %v", err)
		}
	}}
	_, err := client.DeclareEpisodeDependency(ctx, &mnemosv1.DeclareEpisodeDependencyRequest{EpisodeId: "ev-other", DependsOn: "ev-mine", Kind: "copies"})
	if got := status.Code(err); got != codes.AlreadyExists {
		t.Errorf("declaration raced by another declaration: code = %s (%v), want AlreadyExists", got, err)
	}

	conn.Dependencies = concurrentDependencyEdit{EventDependencyRepository: inner, once: &sync.Once{}, edit: func(ctx context.Context, deps ports.EventDependencyRepository) {
		if _, err := deps.Remove(ctx, "ev-other", "ev-mine", "other-writer", time.Now().UTC()); err != nil {
			t.Errorf("concurrent removal: %v", err)
		}
	}}
	_, err = client.RemoveEpisodeDependency(ctx, &mnemosv1.RemoveEpisodeDependencyRequest{EpisodeId: "ev-other", DependsOn: "ev-mine"})
	if got := status.Code(err); got != codes.NotFound {
		t.Errorf("removal raced by another removal: code = %s (%v), want NotFound", got, err)
	}
}
