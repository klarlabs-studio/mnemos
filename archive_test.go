package mnemos_test

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos"
)

// ADR 0034: forgetting a belief for low trust stops surfacing it; it does not
// say the world changed. Default recall drops it, an as-of query for a later
// instant and --include-history still find it. Restating it brings it back;
// internal/pipeline covers that (TestPersistArtifacts_RestatementUnarchives).
func TestConsolidate_ForgettingArchivesInsteadOfEndingValidTime(t *testing.T) {
	clearMnemosEnv(t)
	mem, err := mnemos.New(mnemos.WithStorage("memory://?namespace=consolidate_archive"), mnemos.WithPassiveMode())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	ctx := context.Background()

	const text = "the zebra migration finished"
	if err := mem.RememberEvent(ctx, mnemos.Event{ID: "ev-z", At: time.Now(), Type: "observation", Content: "zebra"}); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.RememberClaim(ctx, mnemos.ClaimItem{Text: text, Confidence: 0.05, EventIDs: []string{"ev-z"}}); err != nil {
		t.Fatalf("RememberClaim: %v", err)
	}
	res, err := mem.Consolidate(ctx, mnemos.ConsolidateOptions{ForgetBelowTrust: 0.5})
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	if res.Forgotten != 1 {
		t.Fatalf("Forgotten = %d, want 1", res.Forgotten)
	}

	count := func(q mnemos.Query) int {
		t.Helper()
		q.Text = "zebra migration"
		got, err := mem.Recall(ctx, q)
		if err != nil {
			t.Fatalf("Recall(%+v): %v", q, err)
		}
		n := 0
		for _, r := range got {
			if r.Text == text {
				n++
			}
		}
		return n
	}
	if n := count(mnemos.Query{}); n != 0 {
		t.Fatalf("default recall returned the forgotten belief %d time(s)", n)
	}
	if n := count(mnemos.Query{AsOf: time.Now().Add(time.Hour)}); n != 1 {
		t.Fatalf("as-of recall after forgetting found it %d time(s), want 1: forgetting is not the world changing", n)
	}
	if n := count(mnemos.Query{IncludeHistory: true}); n != 1 {
		t.Fatalf("--include-history found it %d time(s), want 1", n)
	}

}
