package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

func newTestIncident(id, title string) domain.Incident {
	return domain.Incident{
		ID:       id,
		Title:    title,
		Severity: domain.IncidentSeverityHigh,
		Status:   domain.IncidentStatusOpen,
		OpenedAt: time.Now().UTC().Truncate(time.Second),
	}
}

func TestIncidentRepository_UpsertAndGet(t *testing.T) {
	db, err := open(filepath.Join(t.TempDir(), "inc.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	repo := NewIncidentRepository(db)

	inc := newTestIncident("inc-1", "Production outage")
	inc.Summary = "Everything is on fire"
	inc.RootCauseClaimID = "cl-99"

	if err := repo.Upsert(ctx, inc); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, found, err := repo.GetByID(ctx, "inc-1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !found {
		t.Fatal("expected incident to be found")
	}
	if got.Title != inc.Title {
		t.Errorf("title: got %q, want %q", got.Title, inc.Title)
	}
	if got.RootCauseClaimID != inc.RootCauseClaimID {
		t.Errorf("root_cause_claim_id: got %q, want %q", got.RootCauseClaimID, inc.RootCauseClaimID)
	}
}

func TestIncidentRepository_GetByID_NotFound(t *testing.T) {
	db, err := open(filepath.Join(t.TempDir(), "inc.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	repo := NewIncidentRepository(db)

	_, found, err := repo.GetByID(ctx, "no-such")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("expected not found")
	}
}

func TestIncidentRepository_ListAll(t *testing.T) {
	db, err := open(filepath.Join(t.TempDir(), "inc.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	repo := NewIncidentRepository(db)

	for _, id := range []string{"inc-a", "inc-b", "inc-c"} {
		if err := repo.Upsert(ctx, newTestIncident(id, "incident "+id)); err != nil {
			t.Fatalf("Upsert %s: %v", id, err)
		}
	}

	all, err := repo.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3, got %d", len(all))
	}
}

func TestIncidentRepository_ListBySeverity(t *testing.T) {
	db, err := open(filepath.Join(t.TempDir(), "inc.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	repo := NewIncidentRepository(db)

	high := newTestIncident("h1", "High sev")
	low := newTestIncident("l1", "Low sev")
	low.Severity = domain.IncidentSeverityLow

	_ = repo.Upsert(ctx, high)
	_ = repo.Upsert(ctx, low)

	highs, err := repo.ListBySeverity(ctx, domain.IncidentSeverityHigh)
	if err != nil {
		t.Fatalf("ListBySeverity: %v", err)
	}
	if len(highs) != 1 || highs[0].ID != "h1" {
		t.Fatalf("expected [h1], got %v", highs)
	}
}

func TestIncidentRepository_ListByStatus(t *testing.T) {
	db, err := open(filepath.Join(t.TempDir(), "inc.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	repo := NewIncidentRepository(db)

	open1 := newTestIncident("o1", "Open one")
	_ = repo.Upsert(ctx, open1)
	_ = repo.Resolve(ctx, "o1", time.Now().UTC())

	open2 := newTestIncident("o2", "Still open")
	_ = repo.Upsert(ctx, open2)

	resolved, err := repo.ListByStatus(ctx, domain.IncidentStatusResolved)
	if err != nil {
		t.Fatalf("ListByStatus resolved: %v", err)
	}
	if len(resolved) != 1 || resolved[0].ID != "o1" {
		t.Fatalf("expected [o1] resolved, got %v", resolved)
	}

	opens, err := repo.ListByStatus(ctx, domain.IncidentStatusOpen)
	if err != nil {
		t.Fatalf("ListByStatus open: %v", err)
	}
	if len(opens) != 1 || opens[0].ID != "o2" {
		t.Fatalf("expected [o2] open, got %v", opens)
	}
}

func TestIncidentRepository_Resolve(t *testing.T) {
	db, err := open(filepath.Join(t.TempDir(), "inc.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	repo := NewIncidentRepository(db)

	_ = repo.Upsert(ctx, newTestIncident("r1", "Resolve me"))
	resolvedAt := time.Now().UTC().Truncate(time.Second)
	if err := repo.Resolve(ctx, "r1", resolvedAt); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	got, _, _ := repo.GetByID(ctx, "r1")
	if got.Status != domain.IncidentStatusResolved {
		t.Errorf("status: got %q, want resolved", got.Status)
	}
}

func TestIncidentRepository_AttachDecisionAndOutcome(t *testing.T) {
	db, err := open(filepath.Join(t.TempDir(), "inc.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	repo := NewIncidentRepository(db)

	_ = repo.Upsert(ctx, newTestIncident("a1", "Attach test"))
	_ = repo.AttachDecision(ctx, "a1", "dc-1")
	_ = repo.AttachDecision(ctx, "a1", "dc-2")
	_ = repo.AttachDecision(ctx, "a1", "dc-1") // idempotent
	_ = repo.AttachOutcome(ctx, "a1", "oc-1")

	got, _, _ := repo.GetByID(ctx, "a1")
	if len(got.DecisionIDs) != 2 {
		t.Errorf("expected 2 decision ids, got %d: %v", len(got.DecisionIDs), got.DecisionIDs)
	}
	if len(got.OutcomeIDs) != 1 {
		t.Errorf("expected 1 outcome id, got %d", len(got.OutcomeIDs))
	}
}

func TestIncidentRepository_SetPlaybook(t *testing.T) {
	db, err := open(filepath.Join(t.TempDir(), "inc.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	repo := NewIncidentRepository(db)

	_ = repo.Upsert(ctx, newTestIncident("p1", "Playbook test"))
	_ = repo.SetPlaybook(ctx, "p1", "pb-42")

	got, _, _ := repo.GetByID(ctx, "p1")
	if got.PlaybookID != "pb-42" {
		t.Errorf("playbook_id: got %q, want pb-42", got.PlaybookID)
	}
}

func TestIncidentRepository_CountAllAndDeleteAll(t *testing.T) {
	db, err := open(filepath.Join(t.TempDir(), "inc.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	repo := NewIncidentRepository(db)

	for _, id := range []string{"d1", "d2"} {
		_ = repo.Upsert(ctx, newTestIncident(id, "incident"))
	}
	n, err := repo.CountAll(ctx)
	if err != nil || n != 2 {
		t.Fatalf("CountAll: got %d, %v", n, err)
	}
	if err := repo.DeleteAll(ctx); err != nil {
		t.Fatalf("DeleteAll: %v", err)
	}
	n, _ = repo.CountAll(ctx)
	if n != 0 {
		t.Fatalf("expected 0 after DeleteAll, got %d", n)
	}
}
