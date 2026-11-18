package mnemos_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/mnemos"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
	_ "go.klarlabs.de/mnemos/internal/store/sqlite"
)

func newSkillMemory(t *testing.T) (mnemos.Memory, string) {
	t.Helper()
	clearMnemosEnv(t)
	path := filepath.Join(t.TempDir(), "skills.db")
	mem, err := mnemos.New(
		mnemos.WithSQLite(path),
		mnemos.WithPassiveMode(),
		mnemos.WithActor("lib-operator"),
	)
	if err != nil {
		t.Fatalf("New sqlite: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	return mem, path
}

// The skill-loop reads return what RecordAction / RecordActionOutcome /
// Synthesize stored, through the same internal/skills listing every transport
// uses.
func TestListSkillRecords_ReadBackWhatWasRecorded(t *testing.T) {
	mem, _ := newSkillMemory(t)
	ctx := context.Background()

	var actionIDs []string
	for i := 0; i < 3; i++ {
		id, err := mem.RecordAction(ctx, mnemos.ActionItem{Kind: "rollback", Subject: "api", Actor: "grace"})
		if err != nil {
			t.Fatalf("RecordAction: %v", err)
		}
		actionIDs = append(actionIDs, id)
		if err := mem.RecordActionOutcome(ctx, mnemos.OutcomeItem{ActionID: id, Result: "success"}); err != nil {
			t.Fatalf("RecordActionOutcome: %v", err)
		}
	}
	if _, err := mem.RecordAction(ctx, mnemos.ActionItem{Kind: "deploy", Subject: "web", Actor: "grace"}); err != nil {
		t.Fatalf("RecordAction: %v", err)
	}

	actions, err := mem.ListActions(ctx, mnemos.ActionQuery{Subject: "api"})
	if err != nil {
		t.Fatalf("ListActions: %v", err)
	}
	if len(actions) != 3 || actions[0].Kind != "rollback" || actions[0].Subject != "api" {
		t.Fatalf("ListActions(subject=api) = %+v, want the three api rollbacks", actions)
	}
	if page, _ := mem.ListActions(ctx, mnemos.ActionQuery{Limit: 2, Offset: 1}); len(page) != 2 {
		t.Errorf("ListActions page = %d records, want 2", len(page))
	}

	outcomes, err := mem.ListOutcomes(ctx, mnemos.OutcomeQuery{ActionID: actionIDs[0]})
	if err != nil {
		t.Fatalf("ListOutcomes: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].ActionID != actionIDs[0] || outcomes[0].Result != "success" {
		t.Fatalf("ListOutcomes(action) = %+v", outcomes)
	}

	if _, err := mem.Synthesize(ctx); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	lessons, err := mem.ListLessons(ctx, mnemos.LessonQuery{Service: "api"})
	if err != nil {
		t.Fatalf("ListLessons: %v", err)
	}
	if len(lessons) == 0 || len(lessons[0].Evidence) == 0 || lessons[0].Scope.Service != "api" {
		t.Fatalf("ListLessons(service=api) = %+v, want a synthesized api lesson with evidence", lessons)
	}
	if none, _ := mem.ListLessons(ctx, mnemos.LessonQuery{Service: "nope"}); len(none) != 0 {
		t.Errorf("ListLessons(service=nope) = %+v, want none", none)
	}
	playbooks, err := mem.ListPlaybooks(ctx, mnemos.PlaybookQuery{})
	if err != nil {
		t.Fatalf("ListPlaybooks: %v", err)
	}
	if len(playbooks) == 0 || len(playbooks[0].DerivedFromLessons) == 0 {
		t.Fatalf("ListPlaybooks = %+v, want a playbook derived from the lesson", playbooks)
	}
}

func TestListGlobalSchemas_ReadsThePromotedStore(t *testing.T) {
	mem, path := newSkillMemory(t)
	ctx := context.Background()

	// The library has no promotion write; seed the promoted store the way
	// `mnemos promote --apply` does, through the store itself.
	conn, err := store.Open(ctx, "sqlite://"+path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	now := time.Now().UTC()
	for _, gs := range []domain.GlobalSchema{
		{ID: "gs-active", Statement: "rollbacks fix deploy regressions", Status: domain.GlobalSchemaStatusActive, DistinctTenants: 3, EvidenceCount: 9, Confidence: 0.8, PromotedAt: now},
		{ID: "gs-pending", Statement: "scale before sales", Status: domain.GlobalSchemaStatusPending, DistinctTenants: 2, EvidenceCount: 4, Confidence: 0.6, PromotedAt: now},
	} {
		if err := conn.GlobalSchemas.Upsert(ctx, gs); err != nil {
			t.Fatalf("seed %s: %v", gs.ID, err)
		}
	}
	_ = conn.Close()

	all, err := mem.ListGlobalSchemas(ctx, mnemos.GlobalSchemaQuery{})
	if err != nil || len(all) != 2 {
		t.Fatalf("ListGlobalSchemas = %+v, %v; want 2", all, err)
	}
	active, err := mem.ListGlobalSchemas(ctx, mnemos.GlobalSchemaQuery{Status: "active"})
	if err != nil || len(active) != 1 || active[0].ID != "gs-active" || active[0].DistinctTenants != 3 {
		t.Fatalf("ListGlobalSchemas(active) = %+v, %v", active, err)
	}
	if _, err := mem.ListGlobalSchemas(ctx, mnemos.GlobalSchemaQuery{Status: "bogus"}); err == nil {
		t.Error("an unknown status must be refused")
	}
}

func TestIncidents_OpenListGetResolveAnalyse(t *testing.T) {
	mem, _ := newSkillMemory(t)
	ctx := context.Background()

	decisionID, err := mem.RecordDecision(ctx, mnemos.Decision{Statement: "ship on friday", RiskLevel: "high"})
	if err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}
	inc, err := mem.OpenIncident(ctx, mnemos.IncidentItem{
		Title: "checkout down", Summary: "5xx after deploy", Severity: "critical", DecisionIDs: []string{decisionID},
	})
	if err != nil {
		t.Fatalf("OpenIncident: %v", err)
	}
	if inc.ID == "" || inc.Status != "open" || inc.CreatedBy != "lib-operator" {
		t.Fatalf("OpenIncident = %+v, want an open incident attributed to the Memory's actor", inc)
	}

	got, err := mem.GetIncident(ctx, inc.ID)
	if err != nil || got.Title != "checkout down" || got.Severity != "critical" {
		t.Fatalf("GetIncident = %+v, %v", got, err)
	}
	if list, err := mem.ListIncidents(ctx, mnemos.IncidentQuery{Severity: "critical"}); err != nil || len(list) != 1 {
		t.Fatalf("ListIncidents(critical) = %+v, %v", list, err)
	}

	analysis, err := mem.WhyWereWeWrong(ctx, inc.ID)
	if err != nil {
		t.Fatalf("WhyWereWeWrong: %v", err)
	}
	if len(analysis.AffectedDecisions) != 1 || analysis.AffectedDecisions[0].ID != decisionID || analysis.Explanation == "" {
		t.Errorf("analysis = %+v, want the linked decision and an explanation", analysis)
	}

	resolved, err := mem.ResolveIncident(ctx, inc.ID)
	if err != nil || resolved.Status != "resolved" || resolved.ResolvedAt.IsZero() {
		t.Fatalf("ResolveIncident = %+v, %v", resolved, err)
	}
	if list, _ := mem.ListIncidents(ctx, mnemos.IncidentQuery{Status: "open"}); len(list) != 0 {
		t.Errorf("no incident should still be open, got %+v", list)
	}

	for name, err := range map[string]error{
		"GetIncident":     second(mem.GetIncident(ctx, "inc_missing")),
		"ResolveIncident": second(mem.ResolveIncident(ctx, "inc_missing")),
		"WhyWereWeWrong":  second(mem.WhyWereWeWrong(ctx, "inc_missing")),
	} {
		if !errors.Is(err, mnemos.ErrIncidentNotFound) {
			t.Errorf("%s(unknown): got %v, want ErrIncidentNotFound", name, err)
		}
	}
	if _, err := mem.OpenIncident(ctx, mnemos.IncidentItem{Title: "  "}); !errors.Is(err, mnemos.ErrInvalidIncident) {
		t.Errorf("OpenIncident(blank title): got %v, want ErrInvalidIncident", err)
	}
}

func second[T any](_ T, err error) error { return err }
