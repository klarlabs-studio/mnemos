package main

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/client"
	"go.klarlabs.de/mnemos/internal/domain"
)

func seedActionsAndOutcomes(t *testing.T, f runScopeFixture) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	for _, a := range []domain.Action{
		{ID: "a-1", RunID: "run-a", Kind: "rollback", Subject: "api", Actor: "grace", At: now},
		{ID: "a-2", RunID: "run-b", Kind: "rollback", Subject: "api", Actor: "grace", At: now},
		{ID: "a-3", RunID: "run-b", Kind: "deploy", Subject: "web", Actor: "grace", At: now},
	} {
		if err := f.conn.Actions.Append(ctx, a); err != nil {
			t.Fatalf("seed action %s: %v", a.ID, err)
		}
	}
	for _, o := range []domain.Outcome{
		{ID: "o-1", ActionID: "a-1", Result: domain.OutcomeResultSuccess, ObservedAt: now, Source: "push"},
		{ID: "o-2", ActionID: "a-2", Result: domain.OutcomeResultFailure, ObservedAt: now, Source: "push"},
	} {
		if err := f.conn.Outcomes.Append(ctx, o); err != nil {
			t.Fatalf("seed outcome %s: %v", o.ID, err)
		}
	}
}

// The client's action and outcome listings against a real `serve` over
// SQLite: filters, paging, and the run boundary of a run-scoped token.
func TestClient_ListActionsAndOutcomes_AgainstServe(t *testing.T) {
	f := newRunScopeFixture(t)
	seedActionsAndOutcomes(t, f)
	ctx := context.Background()
	c := client.New(f.srv.URL, client.WithToken(f.openToken(t, []string{"claims:read"})))

	all, err := c.ListActions(ctx, client.ActionQuery{})
	if err != nil {
		t.Fatalf("ListActions: %v", err)
	}
	if all.Total != 3 || len(all.Actions) != 3 || all.Limit != 50 {
		t.Fatalf("ListActions = %+v, want 3 of 3 at the default limit", all)
	}
	page, err := c.ListActions(ctx, client.ActionQuery{Subject: "api", Limit: 1, Offset: 1})
	if err != nil || page.Total != 2 || len(page.Actions) != 1 || page.Actions[0].Subject != "api" {
		t.Fatalf("ListActions(subject=api, page 2) = %+v, %v", page, err)
	}

	outs, err := c.ListOutcomes(ctx, client.OutcomeQuery{ActionID: "a-2"})
	if err != nil || outs.Total != 1 || outs.Outcomes[0].ID != "o-2" || outs.Outcomes[0].Result != "failure" {
		t.Fatalf("ListOutcomes(a-2) = %+v, %v", outs, err)
	}

	scoped := client.New(f.srv.URL, client.WithToken(f.runScopedToken(t, []string{"claims:read"}, []string{"run-a"})))
	mine, err := scoped.ListActions(ctx, client.ActionQuery{})
	if err != nil || mine.Total != 1 || mine.Actions[0].ID != "a-1" {
		t.Fatalf("run-scoped ListActions = %+v, %v; want only a-1", mine, err)
	}
	var apiErr *client.APIError
	if _, err := scoped.ListActions(ctx, client.ActionQuery{RunID: "run-b"}); !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
		t.Errorf("naming another run: err = %v, want 403", err)
	}
	myOuts, err := scoped.ListOutcomes(ctx, client.OutcomeQuery{})
	if err != nil || myOuts.Total != 1 || myOuts.Outcomes[0].ID != "o-1" {
		t.Errorf("run-scoped ListOutcomes = %+v, %v; want only o-1", myOuts, err)
	}
}

// Appending lessons and playbooks over REST writes through the governed
// writer; the listing reads back what the store holds.
func TestClient_LessonsAndPlaybooks_AgainstServe(t *testing.T) {
	f := newRunScopeFixture(t)
	seedActionsAndOutcomes(t, f)
	ctx := context.Background()
	reader := client.New(f.srv.URL, client.WithToken(f.openToken(t, []string{"claims:read"})))
	writer := client.New(f.srv.URL, client.WithToken(f.openToken(t, []string{domain.ScopeClaimsWrite})))
	var apiErr0 *client.APIError
	if _, err := writer.AppendLessons(ctx, []client.Lesson{{ID: "ls-dangling", Statement: "s", Evidence: []string{"a-missing"}, Confidence: 0.5}}); !errors.As(err, &apiErr0) || apiErr0.Status != http.StatusBadRequest {
		t.Fatalf("lesson citing an unknown action: err = %v, want 400", err)
	}

	lesson := client.Lesson{
		ID: "ls-1", Statement: "roll back api on 5xx", Scope: client.Scope{Service: "api"},
		Trigger: "5xx", Evidence: []string{"a-1"}, Confidence: 0.8,
	}
	var apiErr *client.APIError
	if _, err := reader.AppendLessons(ctx, []client.Lesson{lesson}); !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
		t.Fatalf("append with claims:read: err = %v, want 403", err)
	}
	if _, err := writer.AppendLessons(ctx, []client.Lesson{{ID: "ls-bad", Statement: "no evidence", Confidence: 0.5}}); !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		t.Fatalf("append invalid lesson: err = %v, want 400", err)
	}
	n, err := writer.AppendLessons(ctx, []client.Lesson{lesson})
	if err != nil || n != 1 {
		t.Fatalf("AppendLessons = %d, %v", n, err)
	}
	stored, err := f.conn.Lessons.GetByID(ctx, "ls-1")
	if err != nil || stored.Scope.Service != "api" || stored.CreatedBy != "agt_open" {
		t.Fatalf("stored lesson = %+v, %v; want the api lesson attributed to the token", stored, err)
	}
	lessons, err := reader.ListLessons(ctx, client.LessonQuery{Service: "api"})
	if err != nil || lessons.Total != 1 || lessons.Lessons[0].Trigger != "5xx" || len(lessons.Lessons[0].Evidence) != 1 {
		t.Fatalf("ListLessons = %+v, %v", lessons, err)
	}

	playbook := client.Playbook{
		ID: "pb-1", Trigger: "5xx", Statement: "respond to 5xx", Scope: client.Scope{Service: "api"},
		Steps: []client.PlaybookStep{{Order: 1, Action: "rollback"}}, DerivedFromLessons: []string{"ls-1"}, Confidence: 0.7,
	}
	if n, err := writer.AppendPlaybooks(ctx, []client.Playbook{playbook}); err != nil || n != 1 {
		t.Fatalf("AppendPlaybooks = %d, %v", n, err)
	}
	pbs, err := reader.ListPlaybooks(ctx, client.PlaybookQuery{Trigger: "5xx"})
	if err != nil || pbs.Total != 1 || len(pbs.Playbooks[0].Steps) != 1 || pbs.Playbooks[0].Steps[0].Action != "rollback" {
		t.Fatalf("ListPlaybooks = %+v, %v", pbs, err)
	}
}

func TestClient_ListGlobalSchemas_AgainstServe(t *testing.T) {
	f := newRunScopeFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, gs := range []domain.GlobalSchema{
		{ID: "gs-a", Statement: "a", Status: domain.GlobalSchemaStatusActive, DistinctTenants: 3, EvidenceCount: 5, Confidence: 0.8, PromotedAt: now},
		{ID: "gs-p", Statement: "p", Status: domain.GlobalSchemaStatusPending, DistinctTenants: 2, EvidenceCount: 3, Confidence: 0.6, PromotedAt: now},
	} {
		if err := f.conn.GlobalSchemas.Upsert(ctx, gs); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	c := client.New(f.srv.URL, client.WithToken(f.openToken(t, []string{"claims:read"})))
	got, err := c.ListGlobalSchemas(ctx, client.GlobalSchemaQuery{Status: "pending"})
	if err != nil || got.Total != 1 || got.Schemas[0].ID != "gs-p" || got.Schemas[0].DistinctTenants != 2 {
		t.Fatalf("ListGlobalSchemas(pending) = %+v, %v", got, err)
	}
	var apiErr *client.APIError
	if _, err := c.ListGlobalSchemas(ctx, client.GlobalSchemaQuery{Status: "bogus"}); !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		t.Errorf("bogus status: err = %v, want 400", err)
	}
}

// The incident calls against a real `serve`: open (attributed to the token),
// get, list, why-wrong, resolve, and the not-found paths.
func TestClient_Incidents_AgainstServe(t *testing.T) {
	f := newRunScopeFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := f.conn.Decisions.Append(ctx, domain.Decision{ID: "dc-1", Statement: "ship friday", RiskLevel: domain.RiskLevelHigh, ChosenAt: now}); err != nil {
		t.Fatalf("seed decision: %v", err)
	}
	c := client.New(f.srv.URL, client.WithToken(f.openToken(t, []string{domain.ScopeClaimsWrite})))

	inc, err := c.OpenIncident(ctx, client.IncidentItem{Title: "checkout down", Severity: "critical", DecisionIDs: []string{"dc-1"}})
	if err != nil {
		t.Fatalf("OpenIncident: %v", err)
	}
	if inc.ID == "" || inc.Status != "open" || inc.CreatedBy != "agt_open" {
		t.Fatalf("OpenIncident = %+v", inc)
	}
	got, err := c.GetIncident(ctx, inc.ID)
	if err != nil || got == nil || got.Severity != "critical" || len(got.DecisionIDs) != 1 {
		t.Fatalf("GetIncident = %+v, %v", got, err)
	}
	list, err := c.ListIncidents(ctx, client.IncidentQuery{Status: "open"})
	if err != nil || list.Total != 1 {
		t.Fatalf("ListIncidents(open) = %+v, %v", list, err)
	}
	why, err := c.WhyWereWeWrong(ctx, inc.ID)
	if err != nil || len(why.AffectedDecisions) != 1 || why.AffectedDecisions[0].ID != "dc-1" || why.Incident.ID != inc.ID {
		t.Fatalf("WhyWereWeWrong = %+v, %v", why, err)
	}
	if err := c.ResolveIncident(ctx, inc.ID); err != nil {
		t.Fatalf("ResolveIncident: %v", err)
	}
	stored, _, err := f.conn.Incidents.GetByID(ctx, inc.ID)
	if err != nil || stored.Status != domain.IncidentStatusResolved {
		t.Errorf("stored status = %q (%v), want resolved", stored.Status, err)
	}

	if missing, err := c.GetIncident(ctx, "inc_missing"); err != nil || missing != nil {
		t.Errorf("GetIncident(unknown) = %+v, %v; want nil, nil", missing, err)
	}
	var apiErr *client.APIError
	if err := c.ResolveIncident(ctx, "inc_missing"); !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Errorf("ResolveIncident(unknown): err = %v, want 404", err)
	}
	if _, err := c.WhyWereWeWrong(ctx, "inc_missing"); !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Errorf("WhyWereWeWrong(unknown): err = %v, want 404", err)
	}
	if _, err := c.OpenIncident(ctx, client.IncidentItem{Title: "t", Severity: "sev9"}); !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		t.Errorf("OpenIncident(bad severity): err = %v, want 400", err)
	}
}

// The MCP tools run the same internal/skills and internal/incident paths.
func TestMCP_SkillAndIncidentTools(t *testing.T) {
	setIsolatedDB(t)
	ctx := context.Background()

	if _, err := mcpRunRecordAction(ctx, "agent-1", mcpRecordActionInput{Kind: "rollback", Subject: "api", RunID: "run-a"}); err != nil {
		t.Fatalf("record_action: %v", err)
	}
	acts, err := mcpRunListActions(ctx, mcpListActionsInput{Subject: "api"})
	if err != nil || acts.Total != 1 || acts.Actions[0].RunID != "run-a" {
		t.Fatalf("list_actions = %+v, %v", acts, err)
	}
	if _, err := mcpRunRecordOutcome(ctx, "agent-1", mcpRecordOutcomeInput{ActionID: acts.Actions[0].ID, Result: "success"}); err != nil {
		t.Fatalf("record_outcome: %v", err)
	}
	outs, err := mcpRunListOutcomes(ctx, mcpListOutcomesInput{ActionID: acts.Actions[0].ID})
	if err != nil || outs.Total != 1 || outs.Outcomes[0].Result != "success" {
		t.Fatalf("list_outcomes = %+v, %v", outs, err)
	}

	gs, err := mcpRunListGlobalSchemas(ctx, mcpListGlobalSchemasInput{})
	if err != nil || gs.Total != 0 {
		t.Fatalf("list_global_schemas on an empty brain = %+v, %v", gs, err)
	}
	if _, err := mcpRunListGlobalSchemas(ctx, mcpListGlobalSchemasInput{Status: "bogus"}); !isUserInputError(err) {
		t.Errorf("list_global_schemas(bogus): err = %v, want an invalid-params error", err)
	}

	inc, err := mcpRunOpenIncident(ctx, "agent-1", mcpOpenIncidentInput{Title: "api down", Severity: "high"})
	if err != nil || inc.Status != "open" || inc.CreatedBy != "agent-1" {
		t.Fatalf("open_incident = %+v, %v", inc, err)
	}
	if got, err := mcpRunGetIncident(ctx, mcpIncidentIDInput{ID: inc.ID}); err != nil || got.Title != "api down" {
		t.Fatalf("get_incident = %+v, %v", got, err)
	}
	if list, err := mcpRunListIncidents(ctx, mcpListIncidentsInput{Severity: "high"}); err != nil || list.Total != 1 {
		t.Fatalf("list_incidents = %+v, %v", list, err)
	}
	if why, err := mcpRunWhyWereWeWrong(ctx, mcpWhyWereWeWrongInput{IncidentID: inc.ID}); err != nil || why.Incident.ID != inc.ID || why.Explanation == "" {
		t.Fatalf("why_were_we_wrong = %+v, %v", why, err)
	}
	resolved, err := mcpRunResolveIncident(ctx, mcpIncidentIDInput{ID: inc.ID})
	if err != nil || resolved.Status != "resolved" || resolved.ResolvedAt == "" {
		t.Fatalf("resolve_incident = %+v, %v", resolved, err)
	}
	if _, err := mcpRunResolveIncident(ctx, mcpIncidentIDInput{ID: "inc_missing"}); err == nil {
		t.Error("resolve_incident(unknown) must fail")
	}
	if _, err := mcpRunOpenIncident(ctx, "agent-1", mcpOpenIncidentInput{Title: " "}); !isUserInputError(err) {
		t.Errorf("open_incident(blank title): err = %v, want an invalid-params error", err)
	}
}
