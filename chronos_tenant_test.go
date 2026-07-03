package mnemos

import (
	"testing"
	"time"
)

// TestEventToEntityState_TenantIsolation proves the fix that lets a single
// shared chronos engine stay tenant-safe: the same event (type + run id)
// produces DIFFERENT chronos EntityID/ScopeID under different tenants, so two
// tenants' temporal series never merge. An empty tenant preserves the legacy
// (un-prefixed) keys for backward compatibility.
func TestEventToEntityState_TenantIsolation(t *testing.T) {
	ev := Event{Type: "deployment", RunID: "run-1", At: time.Unix(0, 0).UTC()}

	a := (&memory{tenant: "tenant_a"}).eventToEntityState(ev, "evt-1")
	b := (&memory{tenant: "tenant_b"}).eventToEntityState(ev, "evt-1")
	none := (&memory{tenant: ""}).eventToEntityState(ev, "evt-1")

	if a.EntityID == b.EntityID {
		t.Fatalf("different tenants must yield different EntityID, both = %s", a.EntityID)
	}
	if a.ScopeID == b.ScopeID {
		t.Fatalf("different tenants must yield different ScopeID, both = %s", a.ScopeID)
	}
	// Tenant-scoped keys must also differ from the unscoped (legacy) keys.
	if a.EntityID == none.EntityID || a.ScopeID == none.ScopeID {
		t.Fatalf("tenant-scoped keys must differ from unscoped keys")
	}
	// The tenant is surfaced in metadata for downstream (tenant-filtered) reads.
	if a.Meta["tenant"] != "tenant_a" {
		t.Fatalf("tenant must be stamped in meta, got %q", a.Meta["tenant"])
	}
	if _, ok := none.Meta["tenant"]; ok {
		t.Fatalf("unscoped state must not stamp a tenant meta key")
	}

	// Same tenant + same event = stable keys (idempotent mapping).
	a2 := (&memory{tenant: "tenant_a"}).eventToEntityState(ev, "evt-1")
	if a.EntityID != a2.EntityID || a.ScopeID != a2.ScopeID {
		t.Fatalf("same tenant+event must map to stable keys")
	}
}
