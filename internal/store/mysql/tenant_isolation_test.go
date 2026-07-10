package mysql_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
	_ "go.klarlabs.de/mnemos/internal/store/mysql"
)

// TestMySQL_CrossTenantIsolation is the namespace-per-tenant guardrail for the
// MySQL backend (the non-Postgres analogue of TestPostgres_CrossTenantIsolation).
// Two tenants derive distinct namespaces via store.TenantNamespace, which the
// provider turns into distinct MySQL databases; tenant B must read NONE of
// tenant A's rows. Gated on TEST_MYSQL_DSN.
func TestMySQL_CrossTenantIsolation(t *testing.T) {
	dsn := requireLiveDSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Unique per-run tenant ids so the derived databases don't collide with a
	// leftover from a prior run; cleanup drops both.
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenantA := "acme-" + suffix
	tenantB := "globex-" + suffix

	openTenant := func(tenant string) *store.Conn {
		ns := store.TenantNamespace(tenant)
		full := dsn
		if strings.Contains(full, "?") {
			full += "&namespace=" + ns
		} else {
			full += "?namespace=" + ns
		}
		c, err := store.Open(ctx, full)
		if err != nil {
			t.Fatalf("open %s: %v", tenant, err)
		}
		t.Cleanup(func() {
			if raw, ok := c.Raw.(interface {
				ExecContext(context.Context, string, ...any) (any, error)
			}); ok {
				_, _ = raw.ExecContext(context.Background(), fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", ns))
			}
			_ = c.Close()
		})
		return c
	}

	a := openTenant(tenantA)
	b := openTenant(tenantB)

	now := time.Now().UTC()
	ev := domain.Event{
		ID: "iso-mysql", RunID: "r", SchemaVersion: "1",
		Content: "tenant-A secret", SourceInputID: "in",
		Timestamp: now, IngestedAt: now, CreatedBy: domain.SystemUser,
	}
	if err := a.Events.Append(ctx, ev); err != nil {
		t.Fatalf("tenant A append: %v", err)
	}

	bEvents, err := b.Events.ListAll(ctx)
	if err != nil {
		t.Fatalf("tenant B list: %v", err)
	}
	for _, e := range bEvents {
		if e.ID == ev.ID {
			t.Fatalf("CROSS-TENANT LEAK: tenant B read tenant A's event %q", e.ID)
		}
	}

	aEvents, err := a.Events.ListAll(ctx)
	if err != nil {
		t.Fatalf("tenant A list: %v", err)
	}
	found := false
	for _, e := range aEvents {
		if e.ID == ev.ID {
			found = true
		}
	}
	if !found {
		t.Error("tenant A cannot see its own event")
	}
}
