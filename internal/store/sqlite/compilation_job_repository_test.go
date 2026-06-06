package sqlite

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

func TestCompilationJobRepositoryUpsertAndGetByID(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)

	repo := NewCompilationJobRepository(db)
	now := time.Date(2026, 4, 12, 18, 0, 0, 0, time.UTC)
	job := domain.CompilationJob{
		ID:        "job_1",
		Kind:      "extract",
		Status:    "extracting",
		Scope:     map[string]string{"event_ids": "ev_1,ev_2"},
		StartedAt: now,
		UpdatedAt: now,
		Error:     "",
	}

	if err := repo.Upsert(context.Background(), job); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	got, err := repo.GetByID(context.Background(), "job_1")
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if got.Kind != "extract" {
		t.Fatalf("GetByID() kind = %q, want extract", got.Kind)
	}
	if got.Scope["event_ids"] != "ev_1,ev_2" {
		t.Fatalf("GetByID() scope event_ids = %q, want ev_1,ev_2", got.Scope["event_ids"])
	}
}
