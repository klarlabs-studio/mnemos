package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/incident"
	"go.klarlabs.de/mnemos/internal/store"
)

// Incidents must exist and round-trip identically on every backend. Until
// this test, only memory and sqlite wired an IncidentRepository: libSQL (which
// reuses the sqlite repositories) had simply never been given one, and
// Postgres/MySQL had no table, so every incident surface answered 501 on a
// hosted brain. A backend whose conn.Incidents is nil fails here.
//
// Assertions are on what the STORE returns, through the same internal/incident
// path every transport uses (open → get → list → why-wrong → resolve).
func TestIncidents_RoundTripAcrossBackends(t *testing.T) {
	backends := openBackends(t)
	libsqlConn, err := store.Open(context.Background(), "libsql://"+filepath.Join(t.TempDir(), "incidents.db"))
	if err != nil {
		t.Fatalf("open libsql: %v", err)
	}
	t.Cleanup(func() { _ = libsqlConn.Close() })
	backends = append(backends, backendConn{name: "libsql", conn: libsqlConn})

	ctx := context.Background()
	opened := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	resolved := opened.Add(2 * time.Hour)

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			conn := b.conn
			if conn.Incidents == nil {
				t.Fatal("backend has no incident repository")
			}
			if err := conn.Decisions.Append(ctx, domain.Decision{
				ID: "dc-inc-parity", Statement: "ship on friday", RiskLevel: domain.RiskLevelHigh, ChosenAt: opened,
			}); err != nil {
				t.Fatalf("seed decision: %v", err)
			}
			write := func(ctx context.Context, inc domain.Incident) (string, error) {
				return inc.ID, conn.Incidents.Upsert(ctx, inc)
			}
			older := domain.Incident{
				ID: "inc-older", Title: "queue backlog", Severity: domain.IncidentSeverityLow,
				Status: domain.IncidentStatusOpen, OpenedAt: opened.Add(-time.Hour), CreatedBy: "agent-0",
			}
			if err := conn.Incidents.Upsert(ctx, older); err != nil {
				t.Fatalf("upsert older: %v", err)
			}
			inc, err := incident.Open(ctx, conn.Incidents, write, incident.OpenRequest{
				ID: "inc-parity", Title: "checkout down", Summary: "5xx after deploy", Severity: "critical",
				RootCauseClaimID: "cl-missing", DecisionIDs: []string{"dc-inc-parity"},
				OutcomeIDs: []string{"out-1"}, TimelineEventIDs: []string{"ev-1", "ev-2"}, Actor: "agent-1",
			})
			if err != nil {
				t.Fatalf("open: %v", err)
			}

			got, err := incident.Get(ctx, conn.Incidents, inc.ID)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if got.Title != "checkout down" || got.Summary != "5xx after deploy" ||
				got.Severity != domain.IncidentSeverityCritical || got.Status != domain.IncidentStatusOpen ||
				got.RootCauseClaimID != "cl-missing" || got.CreatedBy != "agent-1" ||
				!sameIDs(got.DecisionIDs, "dc-inc-parity") || !sameIDs(got.OutcomeIDs, "out-1") ||
				!sameIDs(got.TimelineEventIDs, "ev-1", "ev-2") || !got.ResolvedAt.IsZero() ||
				got.OpenedAt.Sub(inc.OpenedAt).Abs() > time.Millisecond {
				t.Errorf("stored incident = %+v, want the opened one back", got)
			}

			all, err := incident.List(ctx, conn.Incidents, incident.Filter{})
			if err != nil || len(all) != 2 || all[0].ID != "inc-parity" || all[1].ID != "inc-older" {
				t.Fatalf("list all = %v (%v), want newest-opened first", incidentIDs(all), err)
			}
			crit, err := incident.List(ctx, conn.Incidents, incident.Filter{Severity: "critical"})
			if err != nil || len(crit) != 1 || crit[0].ID != "inc-parity" {
				t.Errorf("list critical = %v (%v)", incidentIDs(crit), err)
			}

			if err := conn.Incidents.AttachDecision(ctx, inc.ID, "dc-2"); err != nil {
				t.Fatalf("attach decision: %v", err)
			}
			if err := conn.Incidents.AttachDecision(ctx, inc.ID, "dc-2"); err != nil {
				t.Fatalf("attach decision again: %v", err)
			}
			if err := conn.Incidents.AttachDecision(ctx, "inc-missing", "dc-2"); err == nil {
				t.Error("attaching to an unknown incident must fail")
			}

			why, err := incident.WhyWereWeWrong(ctx, conn, inc.ID)
			if err != nil {
				t.Fatalf("why-wrong: %v", err)
			}
			if why.Incident.ID != inc.ID || len(why.AffectedDecisions) != 1 || why.AffectedDecisions[0].ID != "dc-inc-parity" || why.Explanation == "" {
				t.Errorf("why-wrong = %+v", why)
			}

			if err := conn.Incidents.Resolve(ctx, inc.ID, resolved); err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if err := conn.Incidents.Resolve(ctx, inc.ID, resolved); err != nil {
				t.Fatalf("resolve is idempotent: %v", err)
			}
			if err := conn.Incidents.Resolve(ctx, "inc-missing", resolved); err == nil {
				t.Error("resolving an unknown incident must fail")
			}
			after, err := incident.Get(ctx, conn.Incidents, inc.ID)
			if err != nil {
				t.Fatalf("get after resolve: %v", err)
			}
			if after.Status != domain.IncidentStatusResolved || !after.ResolvedAt.Equal(resolved) ||
				!sameIDs(after.DecisionIDs, "dc-inc-parity", "dc-2") {
				t.Errorf("after resolve = %+v", after)
			}
			open, err := incident.List(ctx, conn.Incidents, incident.Filter{Status: "open"})
			if err != nil || len(open) != 1 || open[0].ID != "inc-older" {
				t.Errorf("list open = %v (%v)", incidentIDs(open), err)
			}
			if n, err := conn.Incidents.CountAll(ctx); err != nil || n != 2 {
				t.Errorf("count = %d (%v), want 2", n, err)
			}
		})
	}
}

func sameIDs(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func incidentIDs(xs []domain.Incident) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		out = append(out, x.ID)
	}
	return out
}
