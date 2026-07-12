package mysql_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
	mysqlprov "go.klarlabs.de/mnemos/internal/store/mysql"
)

// TestEnumerateTenants_MySQL proves the namespace-per-tenant enumerator over a
// single MySQL server: three tenants each live in their own physical database
// (t_<slug>_<hash>), and enumeration reads each one's lessons in isolation.
// Gated on TEST_MYSQL_DSN.
func TestEnumerateTenants_MySQL(t *testing.T) {
	base := requireLiveDSN(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Unique tenant ids per run so derived namespaces don't collide with a
	// leftover partition from a previous run.
	suffix := time.Now().UnixNano()
	want := map[string]string{ // derived namespace -> statement
		store.TenantNamespace(fmt.Sprintf("alpha-%d", suffix)):   "rollback restores availability",
		store.TenantNamespace(fmt.Sprintf("bravo-%d", suffix)):   "scale workers before the batch",
		store.TenantNamespace(fmt.Sprintf("charlie-%d", suffix)): "cache warmup cuts p99 latency",
	}

	i := 0
	for ns, stmt := range want {
		writeLesson(ctx, t, store.SetDSNParam(base, "namespace", ns), fmt.Sprintf("l_%d_%d", suffix, i), stmt)
		i++
	}
	t.Cleanup(func() { dropDatabases(base, keysOf(want)) })

	scopes, err := store.EnumerateTenants(ctx, base)
	if err != nil {
		t.Fatalf("EnumerateTenants: %v", err)
	}

	// Subset assertion: our three tenants must each appear with their own single
	// lesson (other partitions on a shared server are ignored).
	byTenant := make(map[string]store.TenantScope, len(scopes))
	for _, s := range scopes {
		byTenant[s.Tenant] = s
	}
	for ns, stmt := range want {
		s, ok := byTenant[ns]
		if !ok {
			t.Fatalf("tenant namespace %q not enumerated; got %d scopes", ns, len(scopes))
		}
		if len(s.Lessons) != 1 {
			t.Fatalf("tenant %q: want 1 lesson, got %d", ns, len(s.Lessons))
		}
		if s.Lessons[0].Statement != stmt {
			t.Fatalf("tenant %q: statement %q, want %q", ns, s.Lessons[0].Statement, stmt)
		}
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
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

// dropDatabases removes the tenant databases created by the test.
func dropDatabases(base string, namespaces []string) {
	parsed, err := mysqlprov.ParseDSN(base)
	if err != nil {
		return
	}
	admin, err := sql.Open("mysql", parsed.AdminDSN)
	if err != nil {
		return
	}
	defer func() { _ = admin.Close() }()
	for _, ns := range namespaces {
		_, _ = admin.ExecContext(context.Background(), fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", ns))
	}
}
