package incident_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/incident"
	"go.klarlabs.de/mnemos/internal/store"
	_ "go.klarlabs.de/mnemos/internal/store/memory"
)

func openStore(t *testing.T) *store.Conn {
	t.Helper()
	conn, err := store.Open(context.Background(), "memory://")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func writer(conn *store.Conn) incident.WriteFunc {
	return func(ctx context.Context, inc domain.Incident) (string, error) {
		return inc.ID, conn.Incidents.Upsert(ctx, inc)
	}
}

func TestOpenListGetResolve(t *testing.T) {
	conn := openStore(t)
	ctx := context.Background()

	opened, err := incident.Open(ctx, conn.Incidents, writer(conn), incident.OpenRequest{
		Title: "api down", Summary: "5xx", Severity: "high", Actor: "agent-1",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !strings.HasPrefix(opened.ID, "inc_") {
		t.Errorf("generated id %q, want inc_ prefix", opened.ID)
	}

	got, err := incident.Get(ctx, conn.Incidents, opened.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != domain.IncidentStatusOpen || got.Severity != domain.IncidentSeverityHigh || got.CreatedBy != "agent-1" {
		t.Errorf("stored incident = %+v", got)
	}

	if _, err := incident.Open(ctx, conn.Incidents, writer(conn), incident.OpenRequest{ID: "inc-low", Title: "minor"}); err != nil {
		t.Fatalf("Open default severity: %v", err)
	}
	if got, _ := incident.Get(ctx, conn.Incidents, "inc-low"); got.Severity != domain.IncidentSeverityMedium {
		t.Errorf("default severity = %q, want medium", got.Severity)
	}

	resolved, err := incident.Resolve(ctx, conn.Incidents, func(ctx context.Context, id string, at time.Time) error {
		return conn.Incidents.Resolve(ctx, id, at)
	}, opened.ID)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.Status != domain.IncidentStatusResolved || resolved.ResolvedAt.IsZero() {
		t.Errorf("resolved incident = %+v", resolved)
	}

	high, err := incident.List(ctx, conn.Incidents, incident.Filter{Severity: "high"})
	if err != nil || len(high) != 1 {
		t.Fatalf("List high: %v %v", high, err)
	}
	both, err := incident.List(ctx, conn.Incidents, incident.Filter{Severity: "medium", Status: "resolved"})
	if err != nil || len(both) != 0 {
		t.Errorf("severity and status must both match: %v %v", both, err)
	}
}

func TestErrors(t *testing.T) {
	conn := openStore(t)
	ctx := context.Background()
	never := func(context.Context, string, time.Time) error {
		t.Fatal("resolve must not be called for an unknown incident")
		return nil
	}

	if _, err := incident.Open(ctx, conn.Incidents, writer(conn), incident.OpenRequest{Title: " "}); !errors.Is(err, incident.ErrInvalid) {
		t.Errorf("blank title: got %v, want ErrInvalid", err)
	}
	if _, err := incident.Open(ctx, conn.Incidents, writer(conn), incident.OpenRequest{Title: "t", Severity: "sev9"}); !errors.Is(err, incident.ErrInvalid) {
		t.Errorf("bad severity: got %v, want ErrInvalid", err)
	}
	if _, err := incident.Get(ctx, conn.Incidents, "nope"); !errors.Is(err, incident.ErrNotFound) {
		t.Errorf("Get unknown: got %v, want ErrNotFound", err)
	}
	if _, err := incident.Resolve(ctx, conn.Incidents, never, "nope"); !errors.Is(err, incident.ErrNotFound) {
		t.Errorf("Resolve unknown: got %v, want ErrNotFound", err)
	}
	if _, err := incident.WhyWereWeWrong(ctx, conn, "nope"); !errors.Is(err, incident.ErrNotFound) {
		t.Errorf("WhyWereWeWrong unknown: got %v, want ErrNotFound", err)
	}
	if _, err := incident.List(ctx, conn.Incidents, incident.Filter{Status: "closed"}); !errors.Is(err, incident.ErrInvalid) {
		t.Errorf("bad status filter: got %v, want ErrInvalid", err)
	}
	if _, err := incident.List(ctx, nil, incident.Filter{}); err == nil {
		t.Error("a connection without an incident repository must be an error, not a panic or an empty list")
	}
}

func TestWhyWereWeWrong_HydratesDecisions(t *testing.T) {
	conn := openStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := conn.Decisions.Append(ctx, domain.Decision{
		ID: "dc-1", Statement: "ship it", RiskLevel: domain.RiskLevelMedium, ChosenAt: now,
	}); err != nil {
		t.Fatalf("seed decision: %v", err)
	}
	if _, err := incident.Open(ctx, conn.Incidents, writer(conn), incident.OpenRequest{
		ID: "inc-1", Title: "bad ship", DecisionIDs: []string{"dc-1"},
	}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	rep, err := incident.WhyWereWeWrong(ctx, conn, "inc-1")
	if err != nil {
		t.Fatalf("WhyWereWeWrong: %v", err)
	}
	if rep.Incident.ID != "inc-1" || len(rep.AffectedDecisions) != 1 || rep.AffectedDecisions[0].ID != "dc-1" || rep.Explanation == "" {
		t.Errorf("report = %+v", rep)
	}
}
