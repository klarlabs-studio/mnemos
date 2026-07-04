package mnemos_test

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos"

	_ "go.klarlabs.de/mnemos/internal/store/memory"
)

// TestHypercorrection_SurfacesContradictionOfPromoted is the flagship C3 case:
// new evidence contradicting human-PROMOTED knowledge. It exercises both halves —
// the contradicts edge is generated on the RememberClaim write path (Part A), and
// Hypercorrections surfaces it with the promoted claim identified as the
// established side and the newer claim as the challenger (Part B).
func TestHypercorrection_SurfacesContradictionOfPromoted(t *testing.T) {
	clearMnemosEnv(t)
	mem, err := mnemos.New(mnemos.WithStorage("memory://?namespace=hypercorrection"), mnemos.WithPassiveMode())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	ctx := context.Background()
	now := time.Now()

	if err := mem.RememberEvent(ctx, mnemos.Event{ID: "ev-1", At: now, Type: "deploy", Content: "deploy log a"}); err != nil {
		t.Fatal(err)
	}
	if err := mem.RememberEvent(ctx, mnemos.Event{ID: "ev-2", At: now, Type: "deploy", Content: "deploy log b"}); err != nil {
		t.Fatal(err)
	}

	// Established, human-promoted belief.
	established, err := mem.RememberClaim(ctx, mnemos.ClaimItem{
		Text: "The service deployment succeeded in production", Confidence: 0.9,
		EventIDs: []string{"ev-1"}, Lifecycle: mnemos.ClaimLifecyclePromoted,
	})
	if err != nil {
		t.Fatalf("RememberClaim(established): %v", err)
	}
	// New evidence that contradicts it (negation).
	challenger, err := mem.RememberClaim(ctx, mnemos.ClaimItem{
		Text: "The service deployment didn't succeed in production", Confidence: 0.8,
		EventIDs: []string{"ev-2"},
	})
	if err != nil {
		t.Fatalf("RememberClaim(challenger): %v", err)
	}

	hcs, err := mem.Hypercorrections(ctx)
	if err != nil {
		t.Fatalf("Hypercorrections: %v", err)
	}
	if len(hcs) != 1 {
		t.Fatalf("expected 1 hypercorrection alert, got %d: %+v", len(hcs), hcs)
	}
	hc := hcs[0]
	if !hc.ContradictedPromoted {
		t.Error("the promoted claim must be identified as the established (contradicted) side")
	}
	if hc.ContradictedClaimID != established {
		t.Errorf("ContradictedClaimID = %s, want the promoted claim %s", hc.ContradictedClaimID, established)
	}
	if hc.ChallengingClaimID != challenger {
		t.Errorf("ChallengingClaimID = %s, want the new claim %s", hc.ChallengingClaimID, challenger)
	}
}

// TestHypercorrection_IgnoresUnestablishedContradiction proves the gate is not
// noisy: a contradiction between two ordinary, unpromoted, unscored claims is
// ordinary epistemic churn, not an alert. Establishment requires human promotion
// or accrued trust — a fresh claim's self-reported confidence alone doesn't count.
func TestHypercorrection_IgnoresUnestablishedContradiction(t *testing.T) {
	clearMnemosEnv(t)
	mem, err := mnemos.New(mnemos.WithStorage("memory://?namespace=hypercorrection_quiet"), mnemos.WithPassiveMode())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	ctx := context.Background()
	now := time.Now()

	if err := mem.RememberEvent(ctx, mnemos.Event{ID: "ev-1", At: now, Type: "note", Content: "note a"}); err != nil {
		t.Fatal(err)
	}
	if err := mem.RememberEvent(ctx, mnemos.Event{ID: "ev-2", At: now, Type: "note", Content: "note b"}); err != nil {
		t.Fatal(err)
	}
	// Both low-confidence, neither promoted → neither is "established".
	if _, err := mem.RememberClaim(ctx, mnemos.ClaimItem{
		Text: "The service deployment succeeded in production", Confidence: 0.1, EventIDs: []string{"ev-1"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.RememberClaim(ctx, mnemos.ClaimItem{
		Text: "The service deployment didn't succeed in production", Confidence: 0.1, EventIDs: []string{"ev-2"},
	}); err != nil {
		t.Fatal(err)
	}

	hcs, err := mem.Hypercorrections(ctx)
	if err != nil {
		t.Fatalf("Hypercorrections: %v", err)
	}
	if len(hcs) != 0 {
		t.Fatalf("expected no alerts for a contradiction between unestablished claims, got %d: %+v", len(hcs), hcs)
	}
}

// TestHypercorrection_SupersedingResolvesAlert proves the resolution hook: once a
// reviewer transitions either side of the conflict to superseded (retiring the
// stale belief or dismissing the bad challenger), the alert clears.
func TestHypercorrection_SupersedingResolvesAlert(t *testing.T) {
	clearMnemosEnv(t)
	mem, err := mnemos.New(mnemos.WithStorage("memory://?namespace=hypercorrection_resolve"), mnemos.WithPassiveMode())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	ctx := context.Background()
	now := time.Now()

	if err := mem.RememberEvent(ctx, mnemos.Event{ID: "ev-1", At: now, Type: "deploy", Content: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := mem.RememberEvent(ctx, mnemos.Event{ID: "ev-2", At: now, Type: "deploy", Content: "b"}); err != nil {
		t.Fatal(err)
	}
	established, err := mem.RememberClaim(ctx, mnemos.ClaimItem{
		Text: "The service deployment succeeded in production", Confidence: 0.9,
		EventIDs: []string{"ev-1"}, Lifecycle: mnemos.ClaimLifecyclePromoted,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.RememberClaim(ctx, mnemos.ClaimItem{
		Text: "The service deployment didn't succeed in production", Confidence: 0.8,
		EventIDs: []string{"ev-2"},
	}); err != nil {
		t.Fatal(err)
	}

	// The alert exists.
	if hcs, err := mem.Hypercorrections(ctx); err != nil || len(hcs) != 1 {
		t.Fatalf("precondition: want 1 alert, got %d (err=%v)", len(hcs), err)
	}

	// Reviewer retires the established belief → the conflict is resolved.
	if err := mem.SetClaimLifecycle(ctx, established, mnemos.ClaimLifecycleSuperseded); err != nil {
		t.Fatalf("SetClaimLifecycle: %v", err)
	}
	hcs, err := mem.Hypercorrections(ctx)
	if err != nil {
		t.Fatalf("Hypercorrections after resolve: %v", err)
	}
	if len(hcs) != 0 {
		t.Fatalf("superseding a side must clear the alert, got %d: %+v", len(hcs), hcs)
	}
}

// TestHypercorrection_EmptyStore is a clean no-op.
func TestHypercorrection_EmptyStore(t *testing.T) {
	clearMnemosEnv(t)
	mem, err := mnemos.New(mnemos.WithStorage("memory://?namespace=hypercorrection_empty"), mnemos.WithPassiveMode())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })

	hcs, err := mem.Hypercorrections(context.Background())
	if err != nil {
		t.Fatalf("Hypercorrections: %v", err)
	}
	if len(hcs) != 0 {
		t.Fatalf("empty store must yield no alerts, got %d", len(hcs))
	}
}
