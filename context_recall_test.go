package mnemos

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// seedTextClaim persists an event + claim via the public write path (so the claim
// is indexed for recall) and returns the minted claim id.
func seedTextClaim(t *testing.T, m *memory, text string) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	evID := "ev-" + text[:min(8, len(text))] + time.Now().Format("150405.000000000")
	if err := m.RememberEvent(ctx, Event{ID: evID, At: now, Type: "observation", Content: text}); err != nil {
		t.Fatalf("RememberEvent: %v", err)
	}
	id, err := m.RememberClaim(ctx, ClaimItem{Text: text, EventIDs: []string{evID}, ValidFrom: now})
	if err != nil {
		t.Fatalf("RememberClaim: %v", err)
	}
	return id
}

func resultIDs(rs []Result) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.ClaimID
	}
	return out
}

// TestRecallWithContext_PromotesConnected verifies spreading activation: a query
// result connected to the current context over the epistemic graph is promoted
// above an equally-relevant result that is not — retrieval follows the train of
// thought. Claim A matches the query but NOT the context text; it is reachable
// only via a supports edge from the context claim C, so its promotion is purely
// graph spreading.
func TestRecallWithContext_PromotesConnected(t *testing.T) {
	m := calibMem(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// A and B both match the query. C matches the CONTEXT (not the query).
	aID := seedTextClaim(t, m, "the checkout service persists user carts")
	_ = seedTextClaim(t, m, "the checkout service persists customer orders") // B
	cID := seedTextClaim(t, m, "redis cache latency tuning notes and thresholds")

	// C supports A — A is one epistemic hop from the context.
	if err := m.conn.Relationships.Upsert(ctx, []domain.Relationship{
		{ID: "r_ca", Type: domain.RelationshipTypeSupports, FromClaimID: cID, ToClaimID: aID, CreatedAt: now},
	}); err != nil {
		t.Fatalf("seed relationship: %v", err)
	}

	q := Query{Text: "checkout service persistence"}
	base, err := m.Recall(ctx, q)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(base) < 2 {
		t.Fatalf("expected both A and B in base recall, got %v", resultIDs(base))
	}

	// With the context "redis cache latency", C is recalled and spreads activation
	// to A over the supports edge — A should now lead.
	withCtx, err := m.RecallWithContext(ctx, q, "redis cache latency tuning")
	if err != nil {
		t.Fatalf("RecallWithContext: %v", err)
	}
	if len(withCtx) == 0 || withCtx[0].ClaimID != aID {
		t.Errorf("expected activated claim A (%s) promoted to front; got %v (base %v)", aID, resultIDs(withCtx), resultIDs(base))
	}

	// Empty context is exactly Recall (passthrough — no reordering).
	passthrough, err := m.RecallWithContext(ctx, q, "")
	if err != nil {
		t.Fatalf("RecallWithContext empty: %v", err)
	}
	if len(passthrough) != len(base) {
		t.Fatalf("empty-context length %d != base %d", len(passthrough), len(base))
	}
	for i := range base {
		if passthrough[i].ClaimID != base[i].ClaimID {
			t.Errorf("empty context reordered results at %d: %v vs base %v", i, resultIDs(passthrough), resultIDs(base))
		}
	}
}
