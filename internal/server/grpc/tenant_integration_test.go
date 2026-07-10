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
		})
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
