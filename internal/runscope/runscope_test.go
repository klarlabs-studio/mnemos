package runscope_test

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/runscope"
	"go.klarlabs.de/mnemos/internal/store"
	_ "go.klarlabs.de/mnemos/internal/store/memory"
)

// This is the F.4.b token boundary. It had no direct test and lived
// package-private to cmd/mnemos, which is how gRPC ended up unable to call it
// and returning Unimplemented while REST enforced it — the same token behaving
// differently depending on the transport it arrived on.
func TestCheckEventRunsAllowed(t *testing.T) {
	ctx := context.Background()

	conn, err := store.Open(ctx, "memory://runscope-test")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	now := time.Now().UTC()
	for _, ev := range []domain.Event{
		{ID: "ev-mine", Content: "a", SourceInputID: "s", RunID: "run-mine", Timestamp: now, IngestedAt: now},
		{ID: "ev-theirs", Content: "b", SourceInputID: "s", RunID: "run-theirs", Timestamp: now, IngestedAt: now},
	} {
		if err := conn.Events.Append(ctx, ev); err != nil {
			t.Fatalf("append %s: %v", ev.ID, err)
		}
	}

	t.Run("an empty allowlist imposes no restriction", func(t *testing.T) {
		bad, _, err := runscope.CheckEventRunsAllowed(ctx, conn, []string{"ev-theirs"}, nil)
		if err != nil || bad != "" {
			t.Errorf("unrestricted token must pass: bad=%q err=%v", bad, err)
		}
	})

	t.Run("events inside the allowlist pass", func(t *testing.T) {
		bad, _, err := runscope.CheckEventRunsAllowed(ctx, conn, []string{"ev-mine"}, []string{"run-mine"})
		if err != nil || bad != "" {
			t.Errorf("own-run evidence must pass: bad=%q err=%v", bad, err)
		}
	})

	// The boundary itself: naming another run's event must be refused, not
	// silently dropped from the write.
	t.Run("an event outside the allowlist is refused, naming the run", func(t *testing.T) {
		bad, badRun, err := runscope.CheckEventRunsAllowed(ctx, conn, []string{"ev-theirs"}, []string{"run-mine"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if bad != "ev-theirs" || badRun != "run-theirs" {
			t.Errorf("got (%q,%q), want the offending event and its run identified", bad, badRun)
		}
	})

	// A nonexistent id is treated as outside the boundary. Distinguishing
	// "unknown" from "cross-run" would tell a caller which ids exist, and an
	// agent naming an event it cannot see is not a case worth being generous to.
	t.Run("an unknown event id is refused", func(t *testing.T) {
		bad, _, err := runscope.CheckEventRunsAllowed(ctx, conn, []string{"ev-nope"}, []string{"run-mine"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if bad != "ev-nope" {
			t.Errorf("got %q, want the unknown id refused rather than allowed through", bad)
		}
	})

	t.Run("one bad id among good ones still fails", func(t *testing.T) {
		bad, _, err := runscope.CheckEventRunsAllowed(ctx, conn,
			[]string{"ev-mine", "ev-theirs", "ev-mine"}, []string{"run-mine"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if bad != "ev-theirs" {
			t.Errorf("got %q, want the single cross-run id caught in a mixed batch", bad)
		}
	})
}

// A run-restricted bearer may touch a claim only when all of its evidence lies
// in its runs; a claim with no evidence, or with any foreign evidence, is not
// its to touch.
func TestClaimAllowed(t *testing.T) {
	ctx := context.Background()
	conn, err := store.Open(ctx, "memory://claim-allowed-test")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	now := time.Now().UTC()
	for _, ev := range []domain.Event{
		{ID: "ev-a", Content: "a", SourceInputID: "s", RunID: "run-a", Timestamp: now, IngestedAt: now},
		{ID: "ev-b", Content: "b", SourceInputID: "s", RunID: "run-b", Timestamp: now, IngestedAt: now},
	} {
		if err := conn.Events.Append(ctx, ev); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	var claims []domain.Claim
	for _, id := range []string{"cl-a", "cl-b", "cl-both", "cl-none"} {
		claims = append(claims, domain.Claim{ID: id, Text: id, Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive, Confidence: 0.5, CreatedAt: now})
	}
	if err := conn.Claims.Upsert(ctx, claims); err != nil {
		t.Fatalf("claims: %v", err)
	}
	if err := conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{
		{ClaimID: "cl-a", EventID: "ev-a"}, {ClaimID: "cl-b", EventID: "ev-b"},
		{ClaimID: "cl-both", EventID: "ev-a"}, {ClaimID: "cl-both", EventID: "ev-b"},
	}); err != nil {
		t.Fatalf("evidence: %v", err)
	}
	for _, tc := range []struct {
		claim   string
		allowed []string
		want    bool
	}{
		{"cl-b", nil, true},
		{"cl-a", []string{"run-a"}, true},
		{"cl-b", []string{"run-a"}, false},
		{"cl-both", []string{"run-a"}, false},
		{"cl-both", []string{"run-a", "run-b"}, true},
		{"cl-none", []string{"run-a"}, false},
		{"cl-missing", []string{"run-a"}, false},
	} {
		got, err := runscope.ClaimAllowed(ctx, conn, tc.claim, tc.allowed)
		if err != nil || got != tc.want {
			t.Errorf("ClaimAllowed(%s, %v) = %v, %v; want %v", tc.claim, tc.allowed, got, err, tc.want)
		}
	}
}
