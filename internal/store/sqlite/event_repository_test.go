package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

func TestEventRepositoryAppendAndGetByID(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)

	repo := NewEventRepository(db)
	now := time.Date(2026, 4, 12, 12, 30, 0, 0, time.UTC)
	event := domain.Event{
		ID:            "ev_1",
		RunID:         "run_1",
		SchemaVersion: "v1",
		Content:       "Claim moved from active to contested",
		SourceInputID: "in_1",
		Timestamp:     now,
		IngestedAt:    now,
		Metadata: map[string]string{
			"chunk_kind": "text",
		},
	}

	if err := repo.Append(context.Background(), event); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	got, err := repo.GetByID(context.Background(), "ev_1")
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if got.ID != event.ID {
		t.Fatalf("GetByID() id = %q, want %q", got.ID, event.ID)
	}
	if got.RunID != event.RunID {
		t.Fatalf("GetByID() run_id = %q, want %q", got.RunID, event.RunID)
	}
	if got.Metadata["chunk_kind"] != "text" {
		t.Fatalf("GetByID() metadata chunk_kind = %q, want text", got.Metadata["chunk_kind"])
	}
}

func TestEventRepositoryListByIDsOrder(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)

	repo := NewEventRepository(db)
	now := time.Now().UTC()
	seed := []domain.Event{
		{ID: "ev_a", RunID: "run_a", SchemaVersion: "v1", Content: "a", SourceInputID: "in_1", Timestamp: now, IngestedAt: now, Metadata: map[string]string{}},
		{ID: "ev_b", RunID: "run_b", SchemaVersion: "v1", Content: "b", SourceInputID: "in_1", Timestamp: now, IngestedAt: now, Metadata: map[string]string{}},
		{ID: "ev_c", RunID: "run_a", SchemaVersion: "v1", Content: "c", SourceInputID: "in_1", Timestamp: now, IngestedAt: now, Metadata: map[string]string{}},
	}
	for _, event := range seed {
		if err := repo.Append(context.Background(), event); err != nil {
			t.Fatalf("Append() error = %v", err)
		}
	}

	got, err := repo.ListByIDs(context.Background(), []string{"ev_c", "ev_a", "ev_missing"})
	if err != nil {
		t.Fatalf("ListByIDs() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListByIDs() len = %d, want 2", len(got))
	}
	if got[0].ID != "ev_c" || got[1].ID != "ev_a" {
		t.Fatalf("ListByIDs() order = [%s, %s], want [ev_c, ev_a]", got[0].ID, got[1].ID)
	}
}

func TestEventRepositoryListAll(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)

	repo := NewEventRepository(db)
	now := time.Now().UTC()
	seed := []domain.Event{
		{ID: "ev_1", RunID: "run_1", SchemaVersion: "v1", Content: "one", SourceInputID: "in_1", Timestamp: now, IngestedAt: now, Metadata: map[string]string{}},
		{ID: "ev_2", RunID: "run_2", SchemaVersion: "v1", Content: "two", SourceInputID: "in_1", Timestamp: now.Add(time.Second), IngestedAt: now, Metadata: map[string]string{}},
	}
	for _, event := range seed {
		if err := repo.Append(context.Background(), event); err != nil {
			t.Fatalf("Append() error = %v", err)
		}
	}

	events, err := repo.ListAll(context.Background())
	if err != nil {
		t.Fatalf("ListAll() error = %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("ListAll() len = %d, want 2", len(events))
	}
}

func TestEventRepositoryListByRunID(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)

	repo := NewEventRepository(db)
	now := time.Now().UTC()
	seed := []domain.Event{
		{ID: "ev_1", RunID: "run_1", SchemaVersion: "v1", Content: "one", SourceInputID: "in_1", Timestamp: now, IngestedAt: now, Metadata: map[string]string{}},
		{ID: "ev_2", RunID: "run_2", SchemaVersion: "v1", Content: "two", SourceInputID: "in_1", Timestamp: now.Add(time.Second), IngestedAt: now, Metadata: map[string]string{}},
		{ID: "ev_3", RunID: "run_1", SchemaVersion: "v1", Content: "three", SourceInputID: "in_1", Timestamp: now.Add(2 * time.Second), IngestedAt: now, Metadata: map[string]string{}},
	}
	for _, event := range seed {
		if err := repo.Append(context.Background(), event); err != nil {
			t.Fatalf("Append() error = %v", err)
		}
	}

	events, err := repo.ListByRunID(context.Background(), "run_1")
	if err != nil {
		t.Fatalf("ListByRunID() error = %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("ListByRunID() len = %d, want 2", len(events))
	}
	if events[0].RunID != "run_1" || events[1].RunID != "run_1" {
		t.Fatalf("ListByRunID() unexpected run ids: %q, %q", events[0].RunID, events[1].RunID)
	}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := open(path)
	if err != nil {
		t.Fatalf("open() error = %v", err)
	}
	return db
}
