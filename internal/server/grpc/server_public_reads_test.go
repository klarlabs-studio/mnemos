package grpc

import (
	"context"
	"testing"

	"go.klarlabs.de/bolt"
	"go.klarlabs.de/mnemos/internal/auth"
	"go.klarlabs.de/mnemos/internal/store"
)

// TestAuthenticate_ReadsRequireTokenByDefault is the gRPC secure-by-default
// guardrail: in single-tenant mode a read RPC with no token is rejected unless
// the operator opted into public reads; writes always require a token.
func TestAuthenticate_ReadsRequireTokenByDefault(t *testing.T) {
	base, err := store.Open(context.Background(), "sqlite://"+t.TempDir()+"/t.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = base.Close() }()
	secret, err := auth.GenerateSecret()
	if err != nil {
		t.Fatalf("secret: %v", err)
	}
	verifier := auth.NewVerifier(secret, base.RevokedTokens)
	logger := bolt.New(bolt.NewJSONHandler(nil))

	const readMethod = "/mnemos.v1.MnemosService/ListBeliefs"
	const writeMethod = "/mnemos.v1.MnemosService/AppendBeliefs"
	ctx := context.Background() // no metadata → anonymous

	// Secure default: anonymous read is rejected.
	secure := NewServer(base, verifier, logger, "test")
	if _, err := secure.authenticate(ctx, readMethod); err == nil {
		t.Error("secure default: anonymous read RPC must be rejected, got nil error")
	}
	if _, err := secure.authenticate(ctx, writeMethod); err == nil {
		t.Error("secure default: anonymous write RPC must be rejected, got nil error")
	}

	// Opt-in public reads: anonymous read passes; writes still require a token.
	public := NewServer(base, verifier, logger, "test").WithPublicReads()
	if _, err := public.authenticate(ctx, readMethod); err != nil {
		t.Errorf("public-reads: anonymous read RPC must pass, got %v", err)
	}
	if _, err := public.authenticate(ctx, writeMethod); err == nil {
		t.Error("public-reads: anonymous write RPC must still be rejected, got nil error")
	}
}
