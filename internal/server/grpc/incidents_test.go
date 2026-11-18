package grpc_test

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	mnemosv1 "go.klarlabs.de/mnemos/proto/gen/mnemos/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The incident RPCs run internal/incident, the implementation REST, MCP and
// the Go library share: open → get → list → why-wrong → resolve, read back
// through the RPCs themselves.
func TestGRPC_IncidentLifecycle(t *testing.T) {
	client, conn, issuer := startAuthServer(t)
	ctx := bearerCtx(t, mintToken(t, issuer, []string{domain.ScopeClaimsWrite}, nil))
	now := time.Now().UTC()
	if err := conn.Decisions.Append(context.Background(), domain.Decision{
		ID: "dc-1", Statement: "ship it", RiskLevel: domain.RiskLevelHigh, ChosenAt: now,
	}); err != nil {
		t.Fatalf("seed decision: %v", err)
	}

	opened, err := client.OpenIncident(ctx, &mnemosv1.OpenIncidentRequest{
		Title: "checkout down", Severity: "critical", DecisionIds: []string{"dc-1"},
	})
	if err != nil {
		t.Fatalf("OpenIncident: %v", err)
	}
	if opened.Id == "" || opened.Status != "open" || opened.CreatedBy != "agent-1" {
		t.Fatalf("OpenIncident = %+v, want an open incident attributed to the token subject", opened)
	}

	got, err := client.GetIncident(ctx, &mnemosv1.GetIncidentRequest{Id: opened.Id})
	if err != nil || got.Title != "checkout down" || got.Severity != "critical" {
		t.Fatalf("GetIncident = %+v, %v", got, err)
	}
	list, err := client.ListIncidents(ctx, &mnemosv1.ListIncidentsRequest{Severity: "critical"})
	if err != nil || list.Total != 1 || len(list.Incidents) != 1 {
		t.Fatalf("ListIncidents = %+v, %v", list, err)
	}

	why, err := client.WhyWereWeWrong(ctx, &mnemosv1.WhyWereWeWrongRequest{IncidentId: opened.Id})
	if err != nil {
		t.Fatalf("WhyWereWeWrong: %v", err)
	}
	if len(why.AffectedDecisions) != 1 || why.AffectedDecisions[0].Id != "dc-1" || why.Explanation == "" {
		t.Errorf("WhyWereWeWrong = %+v", why)
	}

	resolved, err := client.ResolveIncident(ctx, &mnemosv1.ResolveIncidentRequest{Id: opened.Id})
	if err != nil || resolved.Status != "resolved" || resolved.ResolvedAt == nil {
		t.Fatalf("ResolveIncident = %+v, %v", resolved, err)
	}
	stored, _, err := conn.Incidents.GetByID(context.Background(), opened.Id)
	if err != nil || stored.Status != domain.IncidentStatusResolved {
		t.Errorf("stored incident status = %q (%v), want resolved", stored.Status, err)
	}

	for name, call := range map[string]func() error{
		"GetIncident": func() error { _, err := client.GetIncident(ctx, &mnemosv1.GetIncidentRequest{Id: "nope"}); return err },
		"ResolveIncident": func() error {
			_, err := client.ResolveIncident(ctx, &mnemosv1.ResolveIncidentRequest{Id: "nope"})
			return err
		},
		"WhyWereWeWrong": func() error {
			_, err := client.WhyWereWeWrong(ctx, &mnemosv1.WhyWereWeWrongRequest{IncidentId: "nope"})
			return err
		},
	} {
		if code := status.Code(call()); code != codes.NotFound {
			t.Errorf("%s(unknown): got %v, want NotFound", name, code)
		}
	}
	if _, err := client.OpenIncident(ctx, &mnemosv1.OpenIncidentRequest{Title: "t", Severity: "sev9"}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("OpenIncident(bad severity): got %v, want InvalidArgument", status.Code(err))
	}
}

func TestGRPC_ListGlobalSchemas(t *testing.T) {
	client, conn, issuer := startAuthServer(t)
	ctx := bearerCtx(t, mintToken(t, issuer, []string{domain.ScopeWildcard}, nil))
	now := time.Now().UTC()
	for _, gs := range []domain.GlobalSchema{
		{ID: "gs-a", Statement: "a", Status: domain.GlobalSchemaStatusActive, DistinctTenants: 3, EvidenceCount: 5, Confidence: 0.8, PromotedAt: now},
		{ID: "gs-p", Statement: "p", Status: domain.GlobalSchemaStatusPending, DistinctTenants: 2, EvidenceCount: 3, Confidence: 0.6, PromotedAt: now},
	} {
		if err := conn.GlobalSchemas.Upsert(context.Background(), gs); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	resp, err := client.ListGlobalSchemas(ctx, &mnemosv1.ListGlobalSchemasRequest{Status: "active"})
	if err != nil {
		t.Fatalf("ListGlobalSchemas: %v", err)
	}
	if resp.Total != 1 || resp.GlobalSchemas[0].Id != "gs-a" || resp.GlobalSchemas[0].DistinctTenants != 3 {
		t.Fatalf("ListGlobalSchemas(active) = %+v", resp)
	}
	if _, err := client.ListGlobalSchemas(ctx, &mnemosv1.ListGlobalSchemasRequest{Status: "bogus"}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("bogus status: got %v, want InvalidArgument", status.Code(err))
	}
}

// An outcome belongs to its action's run: a run-scoped token lists only the
// outcomes of actions inside its whitelist.
func TestGRPC_ListOutcomesFollowsActionRun(t *testing.T) {
	client, conn, issuer := startAuthServer(t)
	bg := context.Background()
	now := time.Now().UTC()
	for _, a := range []domain.Action{
		{ID: "a-mine", RunID: "run-mine", Kind: "deploy", Subject: "api", At: now},
		{ID: "a-theirs", RunID: "run-theirs", Kind: "deploy", Subject: "api", At: now},
	} {
		if err := conn.Actions.Append(bg, a); err != nil {
			t.Fatalf("seed action: %v", err)
		}
	}
	for _, o := range []domain.Outcome{
		{ID: "o-mine", ActionID: "a-mine", Result: domain.OutcomeResultSuccess, ObservedAt: now},
		{ID: "o-theirs", ActionID: "a-theirs", Result: domain.OutcomeResultFailure, ObservedAt: now},
	} {
		if err := conn.Outcomes.Append(bg, o); err != nil {
			t.Fatalf("seed outcome: %v", err)
		}
	}
	scoped := bearerCtx(t, mintToken(t, issuer, []string{domain.ScopeWildcard}, []string{"run-mine"}))
	resp, err := client.ListOutcomes(scoped, &mnemosv1.ListOutcomesRequest{})
	if err != nil {
		t.Fatalf("ListOutcomes: %v", err)
	}
	if len(resp.Outcomes) != 1 || resp.Outcomes[0].Id != "o-mine" {
		t.Errorf("run-scoped ListOutcomes = %+v, want only o-mine", resp.Outcomes)
	}
}
