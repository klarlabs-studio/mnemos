package mnemos

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// Recall reports the belief's canonical trust — the value Get returns — and
// its ranking credibility separately (ADR 0026 §2). Recall used to overwrite
// TrustScore with credibility, so the same belief showed one trust through Get
// and another through Recall.
func TestRecall_ReportsCanonicalTrustAndSeparateCredibility(t *testing.T) {
	m := calibMem(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := m.conn.Events.Append(ctx, domain.Event{
		ID: "ev1", Content: "the ledger runs on postgres", SchemaVersion: "v1", SourceInputID: "src1",
		Timestamp: now.Add(-20 * 24 * time.Hour), IngestedAt: now, CreatedBy: "alice",
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.conn.Claims.Upsert(ctx, []domain.Claim{{
		ID: "cl1", Text: "The ledger runs on Postgres", Type: domain.ClaimTypeFact,
		Confidence: 0.85, Status: domain.ClaimStatusActive, CreatedAt: now, ValidFrom: now,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := m.conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{{ClaimID: "cl1", EventID: "ev1"}}); err != nil {
		t.Fatal(err)
	}
	if err := m.rescoreClaims(ctx, []string{"cl1"}, now); err != nil {
		t.Fatal(err)
	}

	got, err := m.Get(ctx, "cl1")
	if err != nil {
		t.Fatal(err)
	}
	results, err := m.Recall(ctx, Query{Text: "ledger postgres"})
	if err != nil {
		t.Fatal(err)
	}
	var r *Result
	for i := range results {
		if results[i].ClaimID == "cl1" {
			r = &results[i]
		}
	}
	if r == nil {
		t.Fatalf("cl1 not recalled: %+v", results)
	}
	if r.TrustScore != got.TrustScore {
		t.Errorf("Recall reports trust %v, Get reports %v — one belief, two trust values", r.TrustScore, got.TrustScore)
	}
	if r.Credibility == 0 {
		t.Error("Recall did not report credibility")
	}
	if r.Credibility == r.TrustScore {
		t.Logf("credibility happens to equal trust (%v); acceptable, but the fixture no longer distinguishes them", r.Credibility)
	}
}
