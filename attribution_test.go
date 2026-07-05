package mnemos_test

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos"

	_ "go.klarlabs.de/mnemos/internal/store/memory"
)

// TestClaimItem_CreatedByPowersWhoKnows verifies per-claim authorship: a claim
// remembered with a CreatedBy is attributed to that worker, so the transactive
// who-knows-what directory can route to it (the multi-worker substrate).
func TestClaimItem_CreatedByPowersWhoKnows(t *testing.T) {
	mem := newReadMemory(t, "attribution")
	ctx := context.Background()
	now := time.Now().UTC()

	if err := mem.RememberEvent(ctx, mnemos.Event{ID: "ev1", At: now, Type: "observation", Content: "kubernetes deploy rollout uses version tags"}); err != nil {
		t.Fatalf("RememberEvent: %v", err)
	}
	if _, err := mem.RememberClaim(ctx, mnemos.ClaimItem{
		Text:      "kubernetes deploy rollout uses version tags",
		EventIDs:  []string{"ev1"},
		ValidFrom: now,
		CreatedBy: "grace",
	}); err != nil {
		t.Fatalf("RememberClaim: %v", err)
	}

	experts, err := mem.WhoKnows(ctx, "kubernetes deploy rollout", 5)
	if err != nil {
		t.Fatalf("WhoKnows: %v", err)
	}
	if len(experts) == 0 || experts[0].Worker != "grace" {
		t.Errorf("CreatedBy should attribute the claim to grace; got %+v", experts)
	}
}
