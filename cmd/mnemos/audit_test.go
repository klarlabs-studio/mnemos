package main

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

func TestBuildAuditExport_IncludesAllResourcesByDefault(t *testing.T) {
	_, conn := openTestStore(t)

	now := time.Now().UTC()
	seedEventConn(t, conn, "ev1", "r", "content", "in1", `{"source":"file"}`, now)
	seedClaimConn(t, conn, "cl1", "claim text", "fact", "active", 0.8, now)
	if err := conn.Claims.UpsertEvidence(context.Background(), []domain.ClaimEvidence{
		{ClaimID: "cl1", EventID: "ev1"},
	}); err != nil {
		t.Fatalf("seed evidence: %v", err)
	}
	seedClaimConn(t, conn, "cl2", "another", "fact", "active", 0.7, now)
	seedRelationshipConn(t, conn, "r1", "supports", "cl1", "cl2", now)

	exp, err := buildAuditExport(context.Background(), conn, "/tmp/test.db", false)
	if err != nil {
		t.Fatalf("buildAuditExport: %v", err)
	}

	if exp.SchemaVersion != auditSchemaVersion {
		t.Errorf("schema_version = %q, want %q", exp.SchemaVersion, auditSchemaVersion)
	}
	if exp.DBPath != "/tmp/test.db" {
		t.Errorf("db_path = %q, want '/tmp/test.db'", exp.DBPath)
	}
	if exp.Counts.Events != 1 || exp.Counts.Claims != 2 ||
		exp.Counts.Evidence != 1 || exp.Counts.Relationships != 1 {
		t.Errorf("counts wrong: %+v", exp.Counts)
	}
	if len(exp.Embeddings) != 0 {
		t.Errorf("embeddings included by default; want opt-in only")
	}
	if exp.GeneratedAt == "" {
		t.Errorf("generated_at empty")
	}
}

func TestBuildAuditExport_IncludesEmbeddingsOnRequest(t *testing.T) {
	_, conn := openTestStore(t)

	now := time.Now().UTC()
	seedEventConn(t, conn, "ev1", "r", "x", "in1", `{}`, now)
	if err := conn.Embeddings.Upsert(context.Background(), "ev1", "event", []float32{0.1, 0.2, 0.3}, "test-model", ""); err != nil {
		t.Fatalf("seed embedding: %v", err)
	}

	exp, err := buildAuditExport(context.Background(), conn, "/tmp/test.db", true)
	if err != nil {
		t.Fatalf("buildAuditExport: %v", err)
	}
	if exp.Counts.Embeddings != 1 {
		t.Fatalf("embeddings count = %d, want 1", exp.Counts.Embeddings)
	}
	if len(exp.Embeddings) != 1 || exp.Embeddings[0].EntityID != "ev1" {
		t.Fatalf("embeddings content wrong: %+v", exp.Embeddings)
	}
}

func TestBuildAuditExport_EmptyDBYieldsZeroCounts(t *testing.T) {
	_, conn := openTestStore(t)

	exp, err := buildAuditExport(context.Background(), conn, "/tmp/empty.db", false)
	if err != nil {
		t.Fatalf("buildAuditExport: %v", err)
	}
	if exp.Counts.Events != 0 || exp.Counts.Claims != 0 ||
		exp.Counts.Evidence != 0 || exp.Counts.Relationships != 0 {
		t.Errorf("expected all zero counts on empty DB, got %+v", exp.Counts)
	}
}
