package mnemos_test

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos"

	_ "go.klarlabs.de/mnemos/internal/store/memory"
)

// TestRecallWithSufficiency_Insufficient verifies the "feeling of knowing"
// reports thin memory: a query that matches nothing returns zero claims, zero
// confidence, and Sufficient=false — the signal a caller uses to abstain or go
// seek evidence rather than confabulate.
func TestRecallWithSufficiency_Insufficient(t *testing.T) {
	mem := newReadMemory(t, "suf_insufficient")
	ctx := context.Background()

	// Seed one unrelated claim so the corpus is non-empty but nothing matches.
	seedClaim(t, mem, "The billing service runs in eu-west-1.", time.Now())

	results, suf, err := mem.RecallWithSufficiency(ctx, mnemos.Query{
		Text: "quantum teleportation latency budget",
	})
	if err != nil {
		t.Fatalf("RecallWithSufficiency: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no matching claims, got %d", len(results))
	}
	if suf.Sufficient {
		t.Error("Sufficient = true on an unmatched query; want false (thin memory)")
	}
	if suf.ClaimCount != 0 {
		t.Errorf("ClaimCount = %d, want 0", suf.ClaimCount)
	}
	if suf.Confidence != 0 {
		t.Errorf("Confidence = %v, want 0 for no claims", suf.Confidence)
	}
	if suf.Floor != mnemos.RecallSufficiencyFloor {
		t.Errorf("Floor = %v, want %v", suf.Floor, mnemos.RecallSufficiencyFloor)
	}
}

// TestRecallWithSufficiency_Sufficient verifies a well-supported query clears
// the floor: several fresh, corroborating claims (default confidence 1.0) push
// the aggregate confidence above RecallSufficiencyFloor and Sufficient=true.
func TestRecallWithSufficiency_Sufficient(t *testing.T) {
	mem := newReadMemory(t, "suf_sufficient")
	ctx := context.Background()

	now := time.Now()
	for _, text := range []string{
		"The checkout service latency spikes after a deploy.",
		"Checkout latency spikes correlate with the deploy rollout.",
		"After each checkout deploy, latency spikes for ten minutes.",
		"Deploys to checkout cause a transient latency spike.",
		"Checkout latency returns to baseline once the deploy settles.",
	} {
		seedClaim(t, mem, text, now)
	}

	results, suf, err := mem.RecallWithSufficiency(ctx, mnemos.Query{
		Text: "checkout deploy latency spike",
	})
	if err != nil {
		t.Fatalf("RecallWithSufficiency: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected matching claims, got none")
	}
	if !suf.Sufficient {
		t.Errorf("Sufficient = false (confidence %.3f, floor %.2f, %d claims); want true — well-supported query",
			suf.Confidence, suf.Floor, suf.ClaimCount)
	}
	if suf.Confidence < mnemos.RecallSufficiencyFloor {
		t.Errorf("Confidence = %.3f, want >= floor %.2f", suf.Confidence, mnemos.RecallSufficiencyFloor)
	}
	if suf.ClaimCount != len(results) {
		t.Errorf("ClaimCount = %d, want %d (== len results)", suf.ClaimCount, len(results))
	}
}

// TestRecallWithSufficiency_ResultsMatchRecall verifies the sufficiency variant
// returns exactly what plain Recall returns — it only adds the verdict.
func TestRecallWithSufficiency_ResultsMatchRecall(t *testing.T) {
	mem := newReadMemory(t, "suf_parity")
	ctx := context.Background()
	now := time.Now()
	seedClaim(t, mem, "The API gateway enforces a 30s tool-call timeout.", now)
	seedClaim(t, mem, "Tool calls exceeding the gateway timeout are cancelled.", now)

	q := mnemos.Query{Text: "gateway tool-call timeout"}
	plain, err := mem.Recall(ctx, q)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	withSuf, suf, err := mem.RecallWithSufficiency(ctx, q)
	if err != nil {
		t.Fatalf("RecallWithSufficiency: %v", err)
	}
	if len(plain) != len(withSuf) {
		t.Fatalf("result count differs: Recall %d vs RecallWithSufficiency %d", len(plain), len(withSuf))
	}
	for i := range plain {
		if plain[i].ClaimID != withSuf[i].ClaimID {
			t.Errorf("result %d claim id differs: %q vs %q", i, plain[i].ClaimID, withSuf[i].ClaimID)
		}
	}
	if suf.ClaimCount != len(plain) {
		t.Errorf("ClaimCount = %d, want %d", suf.ClaimCount, len(plain))
	}
}
