package sqlite_test

import (
	"context"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
	_ "go.klarlabs.de/mnemos/internal/store/sqlite"
)

// seedTenant writes one lesson into a tenant partition of the base sqlite store,
// scoped by the tenant's derived namespace (file-per-namespace isolation).
func seedTenant(t *testing.T, baseDSN, tenantID, lessonID, statement string) {
	t.Helper()
	ctx := context.Background()
	ns := store.TenantNamespace(tenantID)
	dsn := store.SetDSNParam(baseDSN, "namespace", ns)
	conn, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open tenant %s: %v", tenantID, err)
	}
	defer func() { _ = conn.Close() }()
	now := time.Now().UTC()
	actionID := "a_" + lessonID
	if err := conn.Actions.Append(ctx, domain.Action{
		ID: actionID, Kind: domain.ActionKindDeploy, Subject: "svc", At: now, CreatedAt: now,
	}); err != nil {
		t.Fatalf("append action: %v", err)
	}
	if err := conn.Lessons.Append(ctx, domain.Lesson{
		ID: lessonID, Statement: statement, Confidence: 0.8,
		Evidence: []string{actionID}, DerivedAt: now,
	}); err != nil {
		t.Fatalf("append lesson: %v", err)
	}
}

// TestEnumerateTenants_Sqlite proves the namespace-per-tenant enumerator finds
// every physical tenant partition of ONE base store, reads each one's lessons in
// isolation (evidence hydrated), and never surfaces the reserved default file.
func TestEnumerateTenants_Sqlite(t *testing.T) {
	ctx := context.Background()
	baseDSN := "sqlite://" + filepath.Join(t.TempDir(), "brain.db")

	// Three tenants, each with its own lesson, in the ONE base store.
	want := map[string]string{
		"alpha":   "rollback restores availability",
		"bravo":   "scale workers before the batch",
		"charlie": "cache warmup cuts p99 latency",
	}
	for id, stmt := range want {
		seedTenant(t, baseDSN, id, id+"_l", stmt)
	}
	// Un-scoped/default data in the base file itself must NOT be enumerated as a
	// tenant.
	seedDefault := func() {
		conn, err := store.Open(ctx, baseDSN)
		if err != nil {
			t.Fatalf("open default: %v", err)
		}
		defer func() { _ = conn.Close() }()
		now := time.Now().UTC()
		_ = conn.Actions.Append(ctx, domain.Action{ID: "a_def", Kind: domain.ActionKindDeploy, Subject: "svc", At: now, CreatedAt: now})
		if err := conn.Lessons.Append(ctx, domain.Lesson{
			ID: "def_l", Statement: "default partition secret", Confidence: 0.8,
			Evidence: []string{"a_def"}, DerivedAt: now,
		}); err != nil {
			t.Fatalf("append default lesson: %v", err)
		}
	}
	seedDefault()

	scopes, err := store.EnumerateTenants(ctx, baseDSN)
	if err != nil {
		t.Fatalf("EnumerateTenants: %v", err)
	}
	if len(scopes) != len(want) {
		t.Fatalf("want %d tenant scopes, got %d: %+v", len(want), len(scopes), scopes)
	}

	// Every derived namespace is "t_"-prefixed and distinct; each scope carries
	// exactly its own single lesson with evidence hydrated.
	seenStatements := make([]string, 0, len(scopes))
	for _, s := range scopes {
		if got := store.TenancyModeForDSN(s.DSN); got != store.TenancyNamespace {
			t.Fatalf("scoped DSN %q should stay namespace-mode, got %v", s.DSN, got)
		}
		if len(s.Lessons) != 1 {
			t.Fatalf("tenant %q: want 1 lesson, got %d", s.Tenant, len(s.Lessons))
		}
		l := s.Lessons[0]
		if len(l.Evidence) != 1 {
			t.Fatalf("tenant %q lesson %q: evidence not hydrated: %+v", s.Tenant, l.ID, l.Evidence)
		}
		if l.Statement == "default partition secret" {
			t.Fatalf("LEAK: default-partition lesson surfaced as tenant %q", s.Tenant)
		}
		seenStatements = append(seenStatements, l.Statement)
	}

	wantStatements := make([]string, 0, len(want))
	for _, stmt := range want {
		wantStatements = append(wantStatements, stmt)
	}
	sort.Strings(seenStatements)
	sort.Strings(wantStatements)
	for i := range wantStatements {
		if seenStatements[i] != wantStatements[i] {
			t.Fatalf("statements mismatch:\n got %v\nwant %v", seenStatements, wantStatements)
		}
	}
}

// TestEnumerateTenants_UnsupportedBackend confirms a backend with no per-tenant
// isolation primitive fails closed rather than silently returning nothing.
func TestEnumerateTenants_UnsupportedBackend(t *testing.T) {
	if _, err := store.EnumerateTenants(context.Background(), "memory://"); err == nil {
		t.Fatal("expected memory:// to be rejected for tenant enumeration")
	}
}
