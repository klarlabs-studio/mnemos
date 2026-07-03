package mnemos_test

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos"

	_ "go.klarlabs.de/mnemos/internal/store/memory"
)

// seedEventFor writes an evidence event so a claim can be linked to it.
func seedEventFor(t *testing.T, mem mnemos.Memory, id, text string) {
	t.Helper()
	if err := mem.RememberEvent(context.Background(), mnemos.Event{ID: id, At: time.Now(), Type: "observation", Content: text}); err != nil {
		t.Fatalf("RememberEvent: %v", err)
	}
}

// TestWithClaimTypes_RegistersCustomType verifies a consumer type registered via
// WithClaimTypes is accepted as first-class, while an UNregistered type is still
// rejected at the write boundary.
func TestWithClaimTypes_RegistersCustomType(t *testing.T) {
	clearMnemosEnv(t)
	mem, err := mnemos.New(
		mnemos.WithStorage("memory://?namespace=claimtypes"),
		mnemos.WithPassiveMode(),
		mnemos.WithClaimTypes("knowledge", "  ", "incident"), // blanks ignored
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	ctx := context.Background()

	seedEventFor(t, mem, "ev-k", "a registered-type claim")
	if _, err := mem.RememberClaim(ctx, mnemos.ClaimItem{
		Text: "deploys ship on version tags", Type: "knowledge", EventIDs: []string{"ev-k"},
	}); err != nil {
		t.Fatalf("RememberClaim(knowledge) should succeed once registered: %v", err)
	}

	seedEventFor(t, mem, "ev-i", "an incident claim")
	if _, err := mem.RememberClaim(ctx, mnemos.ClaimItem{
		Text: "payments latency spiked", Type: "incident", EventIDs: []string{"ev-i"},
	}); err != nil {
		t.Fatalf("RememberClaim(incident) should succeed once registered: %v", err)
	}

	// A type that was NOT registered is still rejected.
	seedEventFor(t, mem, "ev-x", "an unregistered-type claim")
	if _, err := mem.RememberClaim(ctx, mnemos.ClaimItem{
		Text: "something", Type: "runbook", EventIDs: []string{"ev-x"},
	}); err == nil {
		t.Fatal("RememberClaim(runbook) should be rejected — not built-in nor registered")
	}
}

// TestWithClaimTypes_DefaultRejectsCustom verifies that WITHOUT registration, a
// non-built-in type is rejected — the default (today's) behaviour is unchanged.
func TestWithClaimTypes_DefaultRejectsCustom(t *testing.T) {
	clearMnemosEnv(t)
	mem, err := mnemos.New(
		mnemos.WithStorage("memory://?namespace=claimtypes_default"),
		mnemos.WithPassiveMode(),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	ctx := context.Background()

	seedEventFor(t, mem, "ev-d", "a claim")
	if _, err := mem.RememberClaim(ctx, mnemos.ClaimItem{
		Text: "x", Type: "knowledge", EventIDs: []string{"ev-d"},
	}); err == nil {
		t.Fatal("without WithClaimTypes, a custom type must be rejected")
	}
	// A built-in still works.
	seedEventFor(t, mem, "ev-f", "a fact")
	if _, err := mem.RememberClaim(ctx, mnemos.ClaimItem{
		Text: "x", Type: "fact", EventIDs: []string{"ev-f"},
	}); err != nil {
		t.Fatalf("built-in type must still work: %v", err)
	}
}
