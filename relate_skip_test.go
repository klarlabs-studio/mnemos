package mnemos

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/pipeline"
	"go.klarlabs.de/mnemos/internal/ports"
)

// unreadableClaims fails the corpus read relate depends on. Embedding the
// interface hides the store's RelateCandidateSource, so the read goes through
// ListAll, the fallback every store has.
type unreadableClaims struct{ ports.ClaimRepository }

func (unreadableClaims) ListAll(context.Context) ([]domain.Claim, error) {
	return nil, errors.New("too many SQL variables")
}

// #428: when relate cannot read the corpus, the write is kept, but the skip is
// logged, counted and recorded in the write's evidence. Before, the capture
// committed without edges and nothing anywhere showed it, which is how #427
// removed contradiction detection from every large brain unnoticed.
func TestWrites_ReportARelateSkip(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	ctx := context.Background()
	m := benchMem(t, "relate_skip")
	if err := m.Remember(ctx, Item{Content: "The deploy pipeline runs integration tests before release."}); err != nil {
		t.Fatal(err)
	}
	m.conn.Claims = unreadableClaims{m.conn.Claims}

	skippedFor := func(write string) any {
		t.Helper()
		m.sessionMu.Lock()
		s := m.lastSession
		m.sessionMu.Unlock()
		if s == nil {
			t.Fatalf("%s: no session recorded", write)
		}
		for _, r := range s.Evidence() {
			if v, ok := r.Value.(map[string]any); ok {
				if reason, ok := v["relate_skipped"]; ok {
					return reason
				}
			}
		}
		return nil
	}

	before := pipeline.RelateSkips()
	if err := m.Remember(ctx, Item{Content: "The deploy pipeline skips integration tests on hotfix releases."}); err != nil {
		t.Fatalf("Remember must keep the write when relate fails: %v", err)
	}
	if got := pipeline.RelateSkips() - before; got != 1 {
		t.Errorf("Remember: RelateSkips grew by %d, want 1", got)
	}
	if r := skippedFor("Remember"); r == nil || !strings.Contains(r.(string), "too many SQL variables") {
		t.Errorf("Remember: evidence relate_skipped = %v, want the read error", r)
	}

	if err := m.RememberEvent(ctx, Event{ID: "ev-skip", At: time.Now().UTC(), Type: "observation", Content: "release notes"}); err != nil {
		t.Fatal(err)
	}
	before = pipeline.RelateSkips()
	if _, err := m.RememberClaim(ctx, ClaimItem{Text: "Hotfix releases bypass the test gate.", EventIDs: []string{"ev-skip"}}); err != nil {
		t.Fatalf("RememberClaim must keep the write when relate fails: %v", err)
	}
	if got := pipeline.RelateSkips() - before; got != 1 {
		t.Errorf("RememberClaim: RelateSkips grew by %d, want 1", got)
	}
	if r := skippedFor("RememberClaim"); r == nil {
		t.Error("RememberClaim: evidence has no relate_skipped")
	}

	if n := strings.Count(logs.String(), "relate skipped"); n != 2 {
		t.Errorf("logged %d relate-skipped warnings, want 2:\n%s", n, logs.String())
	}
}
