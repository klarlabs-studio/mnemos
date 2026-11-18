package query_test

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/query"
	_ "go.klarlabs.de/mnemos/internal/store/sqlite"
)

// ADR 0037: a retracted episode is not evidence. Retraction already retires
// the beliefs that rested on it alone, but recall ranks EVENTS first, so the
// retracted episode still took one of the top event slots — displacing a live
// episode's beliefs — and was reported on the answer's timeline. A read
// recorded before the retraction must still see it (ADR 0036).
func TestRecall_SkipsRetractedEpisodes(t *testing.T) {
	ctx := context.Background()
	w, err := govwrite.New(ctx, "sqlite://"+filepath.Join(t.TempDir(), "retracted.db"), nil)
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	conn := w.Conn()

	seed := func(eventID, claimID, text string) {
		t.Helper()
		now := time.Now().UTC()
		if _, err := w.Events(ctx, []domain.Event{{
			ID: eventID, RunID: "r", SchemaVersion: "v1", SourceInputID: eventID,
			Content: text, Timestamp: now, IngestedAt: now,
		}}); err != nil {
			t.Fatalf("event: %v", err)
		}
		if err := conn.Claims.Upsert(ctx, []domain.Claim{{
			ID: claimID, Text: text, Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive,
			Confidence: 0.8, TrustScore: 0.8, CreatedAt: now, ValidFrom: now.Add(-time.Hour),
		}}); err != nil {
			t.Fatalf("claim: %v", err)
		}
		if _, err := w.EvidenceLinks(ctx, []domain.ClaimEvidence{{ClaimID: claimID, EventID: eventID}}); err != nil {
			t.Fatalf("link: %v", err)
		}
	}
	seed("ev-wrong", "cl-wrong", "the billing outage was caused by the billing cache")
	seed("ev-right", "cl-right", "the billing outage was caused by an expired certificate")
	time.Sleep(5 * time.Millisecond)
	before := time.Now().UTC()
	time.Sleep(5 * time.Millisecond)
	if _, err := w.RetractEvent(ctx, "ev-wrong", domain.ChangeNote{Reason: "source was wrong"}); err != nil {
		t.Fatalf("retract: %v", err)
	}

	engine := query.NewEngineWith(conn.Events, conn.Claims, conn.Relationships, query.EngineDeps{Journal: conn.Journal})
	recall := func(opts query.AnswerOptions) domain.Answer {
		t.Helper()
		ans, err := engine.Recall(ctx, query.RecallRequest{Question: "billing outage caused by", Options: opts})
		if err != nil {
			t.Fatalf("recall: %v", err)
		}
		return ans
	}

	now := recall(query.AnswerOptions{})
	if slices.Contains(now.TimelineEventIDs, "ev-wrong") {
		t.Errorf("today's timeline cites the retracted episode: %v", now.TimelineEventIDs)
	}
	if !slices.Contains(now.TimelineEventIDs, "ev-right") {
		t.Errorf("today's timeline is missing the live episode: %v", now.TimelineEventIDs)
	}

	then := recall(query.AnswerOptions{RecordedAsOf: before})
	if !slices.Contains(then.TimelineEventIDs, "ev-wrong") {
		t.Errorf("a read recorded before the retraction lost the episode: %v", then.TimelineEventIDs)
	}
}

// ADR 0037 §8: an un-retracted episode is evidence again, and its belief is
// back. A recorded-as-of read inside the retracted window still hides both;
// a read after the restoration shows both.
func TestRecall_UnretractedEpisodeComesBack(t *testing.T) {
	ctx := context.Background()
	w, err := govwrite.New(ctx, "sqlite://"+filepath.Join(t.TempDir(), "unretracted.db"), nil)
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	conn := w.Conn()
	now := time.Now().UTC()
	if _, err := w.Events(ctx, []domain.Event{{
		ID: "ev-doubted", RunID: "r", SchemaVersion: "v1", SourceInputID: "ev-doubted",
		Content: "the billing outage was caused by the billing cache", Timestamp: now, IngestedAt: now,
	}}); err != nil {
		t.Fatalf("event: %v", err)
	}
	if _, err := w.Claims(ctx, []domain.Claim{{
		ID: "cl-doubted", Text: "the billing outage was caused by the billing cache", Type: domain.ClaimTypeFact,
		Status: domain.ClaimStatusActive, Confidence: 0.8, TrustScore: 0.8, CreatedAt: now, ValidFrom: now.Add(-time.Hour),
	}}, govwrite.ClaimReason{Reason: "seed"}); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := w.EvidenceLinks(ctx, []domain.ClaimEvidence{{ClaimID: "cl-doubted", EventID: "ev-doubted"}}); err != nil {
		t.Fatalf("link: %v", err)
	}
	pause := func() time.Time {
		time.Sleep(5 * time.Millisecond)
		at := time.Now().UTC()
		time.Sleep(5 * time.Millisecond)
		return at
	}
	if _, err := w.RetractEvent(ctx, "ev-doubted", domain.ChangeNote{Reason: "looked wrong"}); err != nil {
		t.Fatalf("retract: %v", err)
	}
	inWindow := pause()
	if _, err := w.UnretractEvent(ctx, "ev-doubted", domain.ChangeNote{Reason: "it was right"}); err != nil {
		t.Fatalf("unretract: %v", err)
	}
	afterRestore := pause()

	engine := query.NewEngineWith(conn.Events, conn.Claims, conn.Relationships, query.EngineDeps{Journal: conn.Journal})
	recall := func(opts query.AnswerOptions) (episodes []string, beliefs []string) {
		t.Helper()
		ans, err := engine.Recall(ctx, query.RecallRequest{Question: "billing outage caused by", Options: opts})
		if err != nil {
			t.Fatalf("recall: %v", err)
		}
		for _, c := range ans.Claims {
			beliefs = append(beliefs, c.ID)
		}
		return ans.TimelineEventIDs, beliefs
	}

	for _, tc := range []struct {
		name  string
		opts  query.AnswerOptions
		shown bool
	}{
		{"now", query.AnswerOptions{}, true},
		{"recorded inside the retracted window", query.AnswerOptions{RecordedAsOf: inWindow}, false},
		{"recorded after the restoration", query.AnswerOptions{RecordedAsOf: afterRestore}, true},
	} {
		episodes, beliefs := recall(tc.opts)
		if got := slices.Contains(episodes, "ev-doubted"); got != tc.shown {
			t.Errorf("%s: episode shown = %v, want %v (timeline %v)", tc.name, got, tc.shown, episodes)
		}
		if got := slices.Contains(beliefs, "cl-doubted"); got != tc.shown {
			t.Errorf("%s: belief shown = %v, want %v (beliefs %v)", tc.name, got, tc.shown, beliefs)
		}
	}
}
