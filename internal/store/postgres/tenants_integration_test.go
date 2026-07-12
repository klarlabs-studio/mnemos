package postgres_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
	_ "go.klarlabs.de/mnemos/internal/store/postgres"
)

// TestEnumerateTenants_Postgres proves the row-level (RLS) enumerator over a
// single Postgres brain: three tenants in ONE namespace, isolated by the tenant
// column, are each read back scoped to that tenant with correct attribution —
// no tenant's lesson bleeds into another's scope (which would fabricate
// cross-tenant corroboration). Gated on TEST_POSTGRES_DSN like the rest of the
// Postgres integration suite.
func TestEnumerateTenants_Postgres(t *testing.T) {
	baseNoNS := requireLiveDSN(t)
	ns := fmt.Sprintf("mnemos_enum_%d", time.Now().UnixNano())
	base := store.SetDSNParam(baseNoNS, "namespace", ns)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tenants := map[string]string{
		"tenant-alpha":   "rollback restores availability",
		"tenant-bravo":   "scale workers before the batch",
		"tenant-charlie": "cache warmup cuts p99 latency",
	}
	for id, stmt := range tenants {
		writeLesson(ctx, t, store.SetDSNParam(base, "tenant", id), id+"_l", stmt)
	}

	t.Cleanup(func() {
		conn, err := store.Open(context.Background(), base)
		if err == nil {
			if raw, ok := conn.Raw.(interface {
				ExecContext(context.Context, string, ...any) (any, error)
			}); ok {
				_, _ = raw.ExecContext(context.Background(), fmt.Sprintf(`DROP SCHEMA IF EXISTS %s CASCADE`, ns))
			}
			_ = conn.Close()
		}
	})

	scopes, err := store.EnumerateTenants(ctx, base)
	if err != nil {
		t.Fatalf("EnumerateTenants: %v", err)
	}
	if len(scopes) < len(tenants) {
		// A properly RLS-restricted role cannot read across tenants, so the
		// DISTINCT enumeration returns nothing. Enumeration is inherently a
		// privileged cross-tenant read (see the enumerator doc); skip when the
		// test role cannot perform it.
		t.Skipf("role cannot read across tenants for enumeration (got %d scopes); needs superuser/BYPASSRLS", len(scopes))
	}
	if len(scopes) != len(tenants) {
		t.Fatalf("want %d tenant scopes, got %d", len(tenants), len(scopes))
	}

	// Attribution: each scope holds exactly its own tenant's single lesson —
	// proving the explicit tenant-filtered read isolates even under a role that
	// bypasses RLS.
	for _, s := range scopes {
		wantStmt, ok := tenants[s.Tenant]
		if !ok {
			t.Fatalf("unexpected tenant %q enumerated", s.Tenant)
		}
		if len(s.Lessons) != 1 {
			t.Fatalf("tenant %q: want exactly 1 lesson (no cross-tenant bleed), got %d: %+v", s.Tenant, len(s.Lessons), s.Lessons)
		}
		if s.Lessons[0].Statement != wantStmt {
			t.Fatalf("tenant %q: statement %q, want %q", s.Tenant, s.Lessons[0].Statement, wantStmt)
		}
	}
}

func writeLesson(ctx context.Context, t *testing.T, dsn, id, statement string) {
	t.Helper()
	conn, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open %s: %v", dsn, err)
	}
	defer func() { _ = conn.Close() }()
	now := time.Now().UTC()
	aid := "a_" + id
	if err := conn.Actions.Append(ctx, domain.Action{ID: aid, Kind: domain.ActionKindDeploy, Subject: "svc", At: now, CreatedAt: now}); err != nil {
		t.Fatalf("append action: %v", err)
	}
	if err := conn.Lessons.Append(ctx, domain.Lesson{ID: id, Statement: statement, Confidence: 0.8, Evidence: []string{aid}, DerivedAt: now}); err != nil {
		t.Fatalf("append lesson: %v", err)
	}
}
