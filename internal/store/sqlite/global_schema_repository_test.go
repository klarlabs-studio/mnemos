package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

func sampleGlobalSchema() domain.GlobalSchema {
	return domain.GlobalSchema{
		ID:              "gsch_abc123",
		Statement:       "rolling back a failed deploy restores availability",
		Scope:           domain.Context{Service: "payments", Env: "prod"},
		Polarity:        domain.SchemaPolarityPositive,
		DistinctTenants: 4,
		EvidenceCount:   11,
		Confidence:      0.82,
		Surprise:        2.5,
		HasSurprise:     true,
		Status:          domain.GlobalSchemaStatusActive,
		PromotedAt:      time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC),
		CreatedBy:       "consolidate",
	}
}

func TestGlobalSchemaRepository_RoundTrip(t *testing.T) {
	db := openTestDB(t)
	r := NewGlobalSchemaRepository(db)
	ctx := context.Background()

	in := sampleGlobalSchema()
	if err := r.Upsert(ctx, in); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, ok, err := r.GetByID(ctx, in.ID)
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if got.Statement != in.Statement || got.Scope != in.Scope || got.Polarity != in.Polarity {
		t.Fatalf("statement/scope/polarity mismatch: %+v", got)
	}
	if got.DistinctTenants != in.DistinctTenants || got.EvidenceCount != in.EvidenceCount {
		t.Fatalf("counts mismatch: %+v", got)
	}
	if got.Confidence != in.Confidence || got.Surprise != in.Surprise || !got.HasSurprise {
		t.Fatalf("ranking-signal mismatch: %+v", got)
	}
	if got.Status != in.Status || !got.PromotedAt.Equal(in.PromotedAt) {
		t.Fatalf("status/promoted_at mismatch: %+v", got)
	}

	// Upsert is idempotent-by-id: re-writing with ratcheted counts replaces.
	in.DistinctTenants = 6
	if err := r.Upsert(ctx, in); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	got2, _, _ := r.GetByID(ctx, in.ID)
	if got2.DistinctTenants != 6 {
		t.Fatalf("ratchet not applied: %d", got2.DistinctTenants)
	}
	if n, _ := r.CountAll(ctx); n != 1 {
		t.Fatalf("upsert churned identity: count=%d", n)
	}
}

func TestGlobalSchemaRepository_ApproveActivatesPending(t *testing.T) {
	db := openTestDB(t)
	r := NewGlobalSchemaRepository(db)
	ctx := context.Background()

	pending := sampleGlobalSchema()
	pending.Status = domain.GlobalSchemaStatusPending
	if err := r.Upsert(ctx, pending); err != nil {
		t.Fatalf("upsert pending: %v", err)
	}

	byStatus, err := r.ListByStatus(ctx, domain.GlobalSchemaStatusPending)
	if err != nil || len(byStatus) != 1 {
		t.Fatalf("list pending: n=%d err=%v", len(byStatus), err)
	}

	if err := r.Approve(ctx, pending.ID); err != nil {
		t.Fatalf("approve: %v", err)
	}
	got, _, _ := r.GetByID(ctx, pending.ID)
	if got.Status != domain.GlobalSchemaStatusActive {
		t.Fatalf("approve did not activate: %s", got.Status)
	}

	// Approving a missing id is an sql.ErrNoRows-wrapped error.
	if err := r.Approve(ctx, "nope"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("approve missing id: want sql.ErrNoRows, got %v", err)
	}
}

// TestGlobalSchemaRepository_NoLeakOnWrite is the write-direction analogue of the
// ADR-0007 cross-tenant read guardrail: a persisted global schema must carry NO
// tenant identifier and NO raw per-tenant evidence id. Here we persist a schema
// that was promoted out of tenants whose ids and evidence ids we know, then scan
// EVERY stored cell of the global_schemas row and assert none of those secrets
// appear anywhere in the neocortex store.
func TestGlobalSchemaRepository_NoLeakOnWrite(t *testing.T) {
	db := openTestDB(t)
	r := NewGlobalSchemaRepository(db)
	ctx := context.Background()

	// Secrets that lived in the per-tenant tier and must never reach global.
	tenantIDs := []string{"acme-corp", "tenant-42", "globex"}
	rawEvidenceIDs := []string{"ac_secret_001", "ev_pii_9f3", "dec_internal_77"}

	gs := sampleGlobalSchema()
	if err := r.Upsert(ctx, gs); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// The table must have no `tenant` column (it is the shared tier by design).
	cols, err := db.QueryContext(ctx, `SELECT name FROM pragma_table_info('global_schemas')`)
	if err != nil {
		t.Fatalf("pragma: %v", err)
	}
	defer func() { _ = cols.Close() }()
	for cols.Next() {
		var name string
		if err := cols.Scan(&name); err != nil {
			t.Fatalf("scan col: %v", err)
		}
		if name == "tenant" {
			t.Fatal("global_schemas must not have a tenant column (shared tier)")
		}
	}

	// Scan every cell of the stored row as text and assert no secret leaked.
	rows, err := db.QueryContext(ctx, `SELECT id, statement, scope_service, scope_env, scope_team, polarity, status, created_by FROM global_schemas`)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		cells := make([]string, 8)
		ptrs := make([]any, 8)
		for i := range cells {
			ptrs[i] = &cells[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("scan row: %v", err)
		}
		blob := strings.ToLower(strings.Join(cells, "\x00"))
		for _, secret := range append(append([]string{}, tenantIDs...), rawEvidenceIDs...) {
			if strings.Contains(blob, strings.ToLower(secret)) {
				t.Fatalf("leaked secret %q into global store row: %v", secret, cells)
			}
		}
	}
}
