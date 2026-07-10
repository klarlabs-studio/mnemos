package grpc_test

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/auth"
	"go.klarlabs.de/mnemos/internal/domain"
	mnemosgrpc "go.klarlabs.de/mnemos/internal/server/grpc"
	"go.klarlabs.de/mnemos/internal/store"
	_ "go.klarlabs.de/mnemos/internal/store/postgres"
	mnemosv1 "go.klarlabs.de/mnemos/proto/gen/mnemos/v1"

	grpclib "google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestGRPC_CrossTenantIsolation is the ADR 0007 guardrail for the gRPC surface:
// with serve --require-tenant scoping, tenant B must read NONE of tenant A's
// rows. Requires a live Postgres (TEST_POSTGRES_DSN) and a non-superuser role
// (RLS is unenforceable otherwise — it skips loudly there).
func TestGRPC_CrossTenantIsolation(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set; skipping gRPC tenant isolation test")
	}
	ctx := context.Background()
	ns := fmt.Sprintf("grpc_iso_%d", time.Now().UnixNano())
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	baseDSN := dsn + sep + "namespace=" + ns

	base, err := store.Open(ctx, baseDSN)
	if err != nil {
		t.Fatalf("open base: %v", err)
	}
	t.Cleanup(func() {
		if raw, ok := base.Raw.(interface {
			ExecContext(context.Context, string, ...any) (any, error)
		}); ok {
			_, _ = raw.ExecContext(context.Background(), fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", ns))
		}
		_ = base.Close()
	})
	// Skip if the connecting role bypasses RLS.
	if r, ok := base.Raw.(interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	}); ok {
		var bypass bool
		if err := r.QueryRowContext(ctx, "SELECT rolbypassrls OR rolsuper FROM pg_roles WHERE rolname = current_user").Scan(&bypass); err == nil && bypass {
			t.Skip("connecting role bypasses RLS; use a non-superuser role")
		}
	}

	secret, err := auth.GenerateSecret()
	if err != nil {
		t.Fatalf("secret: %v", err)
	}
	issuer := auth.NewIssuer(secret)
	verifier := auth.NewVerifier(secret, base.RevokedTokens)
	user := domain.User{ID: "usr_iso", Status: domain.UserStatusActive, CreatedAt: time.Now()}
	tokA, _, err := issuer.IssueUserTokenWithTenant(user, "acme", time.Hour)
	if err != nil {
		t.Fatalf("issue A: %v", err)
	}
	tokB, _, err := issuer.IssueUserTokenWithTenant(user, "globex", time.Hour)
	if err != nil {
		t.Fatalf("issue B: %v", err)
	}

	srvImpl := mnemosgrpc.NewServer(base, verifier, testLogger(), "test").
		WithTenantScoping(func(ctx context.Context, tenant string) (*store.Conn, error) {
			return store.Open(ctx, baseDSN+"&tenant="+tenant)
		}, func(c *store.Conn) { _ = c.Close() })
	gs := grpclib.NewServer(grpclib.UnaryInterceptor(srvImpl.UnaryInterceptor()))
	srvImpl.Register(gs)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = gs.Serve(lis) }()
	defer gs.GracefulStop()
	cc, err := grpclib.NewClient(lis.Addr().String(), grpclib.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = cc.Close() }()
	client := mnemosv1.NewMnemosServiceClient(cc)

	authCtx := func(tok string) context.Context {
		return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+tok)
	}

	now := timestamppb.New(time.Now().UTC())
	if _, err := client.AppendEvents(authCtx(tokA), &mnemosv1.AppendEventsRequest{
		Events: []*mnemosv1.Event{
			{Id: "ev-acme", RunId: "r", SchemaVersion: "v1", Content: "acme secret", SourceInputId: "in", Timestamp: now, IngestedAt: now},
		},
	}); err != nil {
		t.Fatalf("tenant A AppendEvents: %v", err)
	}

	// Tenant B must see none of A's events.
	bList, err := client.ListEvents(authCtx(tokB), &mnemosv1.ListEventsRequest{})
	if err != nil {
		t.Fatalf("tenant B ListEvents: %v", err)
	}
	for _, e := range bList.Events {
		if e.Id == "ev-acme" {
			t.Fatalf("CROSS-TENANT LEAK: tenant B read tenant A's event %q", e.Id)
		}
	}

	// Tenant A sees its own.
	aList, err := client.ListEvents(authCtx(tokA), &mnemosv1.ListEventsRequest{})
	if err != nil {
		t.Fatalf("tenant A ListEvents: %v", err)
	}
	found := false
	for _, e := range aList.Events {
		if e.Id == "ev-acme" {
			found = true
		}
	}
	if !found {
		t.Error("tenant A cannot see its own event")
	}

	// A token with no tenant claim is rejected.
	plain, _, _ := issuer.IssueUserToken(user, time.Hour)
	if _, err := client.ListEvents(authCtx(plain), &mnemosv1.ListEventsRequest{}); err == nil {
		t.Error("expected a token without a tenant to be rejected")
	}
}

// TestGRPC_CachedTenantConnSurvivesMultipleRPCs is the regression guard for the
// availability bug where the interceptor force-closed a process-cached tenant
// pool after every RPC (raw tconn.Close instead of the cache-aware closer). It
// wires WithTenantScoping with an opener that always returns ONE shared conn
// (as the serve conn cache does) and a no-op closer; two sequential RPCs for the
// same tenant must both succeed. Under the old code the first RPC closed the
// shared pool and the second failed with "sql: database is closed".
func TestGRPC_CachedTenantConnSurvivesMultipleRPCs(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set; skipping gRPC cached-conn regression test")
	}
	ctx := context.Background()
	ns := fmt.Sprintf("grpc_cache_%d", time.Now().UnixNano())
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	baseDSN := dsn + sep + "namespace=" + ns

	base, err := store.Open(ctx, baseDSN)
	if err != nil {
		t.Fatalf("open base: %v", err)
	}
	t.Cleanup(func() {
		if raw, ok := base.Raw.(interface {
			ExecContext(context.Context, string, ...any) (any, error)
		}); ok {
			_, _ = raw.ExecContext(context.Background(), fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", ns))
		}
		_ = base.Close()
	})

	// One shared, tenant-scoped conn returned for every request — the exact
	// ownership the serve conn cache has (closed only at shutdown, never per RPC).
	const tenant = "acme"
	shared, err := store.Open(ctx, baseDSN+"&tenant="+tenant)
	if err != nil {
		t.Fatalf("open shared tenant conn: %v", err)
	}
	closes := 0
	t.Cleanup(func() { _ = shared.Close() })

	secret, err := auth.GenerateSecret()
	if err != nil {
		t.Fatalf("secret: %v", err)
	}
	issuer := auth.NewIssuer(secret)
	verifier := auth.NewVerifier(secret, base.RevokedTokens)
	user := domain.User{ID: "usr_cache", Status: domain.UserStatusActive, CreatedAt: time.Now()}
	tok, _, err := issuer.IssueUserTokenWithTenant(user, tenant, time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	srvImpl := mnemosgrpc.NewServer(base, verifier, testLogger(), "test").
		WithTenantScoping(
			func(ctx context.Context, tn string) (*store.Conn, error) { return shared, nil },
			func(c *store.Conn) { closes++ }, // cache-aware: never actually close the shared pool
		)
	gs := grpclib.NewServer(grpclib.UnaryInterceptor(srvImpl.UnaryInterceptor()))
	srvImpl.Register(gs)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = gs.Serve(lis) }()
	defer gs.GracefulStop()
	cc, err := grpclib.NewClient(lis.Addr().String(), grpclib.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = cc.Close() }()
	client := mnemosv1.NewMnemosServiceClient(cc)
	authCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+tok)

	now := timestamppb.New(time.Now().UTC())
	if _, err := client.AppendEvents(authCtx, &mnemosv1.AppendEventsRequest{
		Events: []*mnemosv1.Event{
			{Id: "ev-1", RunId: "r", SchemaVersion: "v1", Content: "first", SourceInputId: "in", Timestamp: now, IngestedAt: now},
		},
	}); err != nil {
		t.Fatalf("RPC 1 AppendEvents: %v", err)
	}
	// RPC 2 over the SAME shared conn — must not hit a closed pool.
	if _, err := client.ListEvents(authCtx, &mnemosv1.ListEventsRequest{}); err != nil {
		t.Fatalf("RPC 2 ListEvents (shared conn closed by a prior RPC?): %v", err)
	}
	// The interceptor must have routed both releases through the supplied closer,
	// never a raw Close on the shared conn.
	if closes < 2 {
		t.Errorf("expected the cache-aware closer to be called per RPC; got %d calls", closes)
	}
}
