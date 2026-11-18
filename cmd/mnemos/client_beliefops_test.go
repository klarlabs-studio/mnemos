package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/client"
	"go.klarlabs.de/mnemos/internal/domain"
)

// The Go client's belief operations against a real `serve` over SQLite,
// asserting on what the store holds afterwards.
func TestClient_BeliefOperations_AgainstServe(t *testing.T) {
	f := newBeliefOpsFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, id := range []string{"cl-a", "cl-b", "cl-c"} {
		seedClaimConn(t, f.conn, id, "belief "+id, "fact", "active", 0.8, now)
	}
	w := client.New(f.srv.URL, client.WithToken(f.token(t, domain.ScopeClaimsWrite, domain.ScopePromoteGlobal)))
	var apiErr *client.APIError

	reader := client.New(f.srv.URL, client.WithToken(f.token(t, "claims:read")))
	if _, err := reader.DeprecateClaim(ctx, "cl-c", "no"); !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
		t.Fatalf("claims:read DeprecateClaim: %v, want 403", err)
	}
	if _, err := w.DeprecateClaim(ctx, "cl-missing", ""); !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Fatalf("unknown claim: %v, want 404", err)
	}
	ch, err := w.DeprecateClaim(ctx, "cl-c", "stale")
	if err != nil || ch.NewStatus != "deprecated" || f.stored(t, "cl-c").Status != domain.ClaimStatusDeprecated {
		t.Fatalf("DeprecateClaim = %+v, %v", ch, err)
	}
	if rev, err := w.UpdateClaim(ctx, "cl-a", "revised", 0.6, ""); err != nil || rev.OldText != "belief cl-a" {
		t.Fatalf("UpdateClaim = %+v, %v", rev, err)
	}
	if c := f.stored(t, "cl-a"); c.Text != "revised" || c.Confidence != 0.6 {
		t.Errorf("stored after update = %q %.2f", c.Text, c.Confidence)
	}
	if err := w.ResolveContradiction(ctx, "cl-a", "cl-b", "observed"); err != nil {
		t.Fatalf("ResolveContradiction: %v", err)
	}
	if s := f.stored(t, "cl-b").Status; s != domain.ClaimStatusDeprecated {
		t.Errorf("loser status = %s", s)
	}
	if esc, err := w.EscalateClaim(ctx, "cl-a", "human please"); err != nil || esc.EscalationReason != "human please" {
		t.Errorf("EscalateClaim = %+v, %v", esc, err)
	}
	at, err := w.VerifyClaim(ctx, "cl-a")
	if err != nil || at.IsZero() || f.stored(t, "cl-a").VerifyCount != 1 {
		t.Fatalf("VerifyClaim = %v, %v", at, err)
	}
	fb, err := w.RecordFeedback(ctx, "cl-a", false, "off")
	if err != nil || fb.NegativeFeedbackStreak != 1 || fb.NewConfidence >= 0.6 {
		t.Fatalf("RecordFeedback = %+v, %v", fb, err)
	}
	if state, ok, err := f.conn.Feedback.Get(ctx, "cl-a"); err != nil || !ok || state.NegativeFeedbackStreak != 1 {
		t.Errorf("stored feedback = %+v ok=%v err=%v", state, ok, err)
	}

	md, err := w.ExportClaimMarkdown(ctx, "cl-a", false)
	if err != nil || !strings.HasPrefix(md, "---") || !strings.Contains(md, "revised") {
		t.Errorf("ExportClaimMarkdown = %q, %v", md, err)
	}
	if _, err := w.ExportClaimMarkdown(ctx, "cl-missing", true); !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Errorf("export unknown: %v, want 404", err)
	}
}

func TestClient_FactsScanAndPurge_AgainstServe(t *testing.T) {
	f := newBeliefOpsFixture(t)
	ctx := context.Background()
	w := client.New(f.srv.URL, client.WithToken(f.token(t, domain.ScopeClaimsWrite)))
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	id, err := w.RememberClaimWithEvidence(ctx, "The billing service runs in eu-west-1.", []string{"runbook p.4"}, from)
	if err != nil || id == "" {
		t.Fatalf("RememberClaimWithEvidence = %q, %v", id, err)
	}
	if c := f.stored(t, id); !c.ValidFrom.Equal(from) {
		t.Errorf("stored valid_from = %v, want %v", c.ValidFrom, from)
	}
	got, err := w.Scan(ctx, client.ScanRequest{ValidFrom: from, ValidUntil: from.AddDate(0, 1, 0)})
	if err != nil || len(got) != 1 || got[0].ID != id {
		t.Errorf("Scan = %+v, %v", got, err)
	}

	now := time.Now().UTC()
	seedEventConn(t, f.conn, "ev-gone", "run-gone", "erase me", "src", "{}", now)
	seedClaimConn(t, f.conn, "cl-gone", "erase me", "fact", "active", 0.9, now)
	if err := f.conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{{ClaimID: "cl-gone", EventID: "ev-gone"}}); err != nil {
		t.Fatal(err)
	}
	res, err := w.PurgeClaims(ctx, "run-gone")
	if err != nil || res.ClaimsDeleted != 1 || res.EventsDeleted != 1 {
		t.Fatalf("PurgeClaims = %+v, %v", res, err)
	}
	if left, err := f.conn.Claims.ListByIDs(ctx, []string{"cl-gone"}); err != nil || len(left) != 0 {
		t.Errorf("cl-gone after purge = %+v, %v", left, err)
	}
	var apiErr *client.APIError
	runScoped := client.New(f.srv.URL, client.WithToken(f.runToken(t, []string{domain.ScopeClaimsWrite}, []string{"run-a"})))
	if _, err := runScoped.PurgeClaims(ctx, "run-b"); !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
		t.Errorf("run-scoped purge of another run: %v, want 403", err)
	}
}

func TestClient_EntityRelationships_AgainstServe(t *testing.T) {
	f := newBeliefOpsFixture(t)
	ctx := context.Background()
	w := client.New(f.srv.URL, client.WithToken(f.token(t, domain.ScopeRelationshipsWrite)))
	n, err := w.AppendEntityRelationships(ctx, []client.EntityRelationship{{
		ID: "er-1", Kind: "action_of", FromID: "ac-1", FromType: "action", ToID: "oc-1", ToType: "outcome",
	}})
	if err != nil || n != 1 {
		t.Fatalf("AppendEntityRelationships = %d, %v", n, err)
	}
	stored, err := f.conn.EntityRels.ListAll(ctx)
	if err != nil || len(stored) != 1 || stored[0].ToID != "oc-1" {
		t.Fatalf("stored edges = %+v, %v", stored, err)
	}
	pg, err := w.ListEntityRelationships(ctx, client.EntityRelationshipFilter{EntityID: "ac-1", EntityType: "action"})
	if err != nil || pg.Total != 1 || pg.Edges[0].ID != "er-1" || pg.Edges[0].CreatedBy == "" {
		t.Errorf("ListEntityRelationships = %+v, %v", pg, err)
	}
}
