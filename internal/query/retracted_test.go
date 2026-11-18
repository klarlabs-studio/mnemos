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

// ADR 0037 §8 with ADR 0036 step 3: retraction unlinks a belief that keeps
// other evidence, and un-retraction relinks it as a NEW link (recorded now).
// So a recorded-as-of read inside the retracted window reconstructs trust on
// the one remaining episode, and a read after the restoration on both.
func TestRecall_RelinkedBeliefTrustFollowsTheWindow(t *testing.T) {
	ctx := context.Background()
	w, err := govwrite.New(ctx, "sqlite://"+filepath.Join(t.TempDir(), "relinked.db"), nil)
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	conn := w.Conn()
	now := time.Now().UTC()
	if _, err := w.Events(ctx, []domain.Event{
		{ID: "ev-a", RunID: "r", SchemaVersion: "v1", SourceInputID: "a", CreatedBy: "alice",
			Content: "the checkout queue drains every hour", Timestamp: now, IngestedAt: now},
		{ID: "ev-b", RunID: "r", SchemaVersion: "v1", SourceInputID: "b", CreatedBy: "bob",
			Content: "the checkout queue drains every hour, per the runbook", Timestamp: now, IngestedAt: now},
	}); err != nil {
		t.Fatalf("events: %v", err)
	}
	if _, err := w.Claims(ctx, []domain.Claim{{
		ID: "cl-queue", Text: "the checkout queue drains every hour", Type: domain.ClaimTypeFact,
		Status: domain.ClaimStatusActive, Confidence: 0.8, CreatedAt: now, ValidFrom: now.Add(-time.Hour),
	}}, govwrite.ClaimReason{Reason: "seed"}); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := w.EvidenceLinks(ctx, []domain.ClaimEvidence{
		{ClaimID: "cl-queue", EventID: "ev-a"}, {ClaimID: "cl-queue", EventID: "ev-b"},
	}); err != nil {
		t.Fatalf("links: %v", err)
	}
	pause := func() time.Time {
		time.Sleep(5 * time.Millisecond)
		at := time.Now().UTC()
		time.Sleep(5 * time.Millisecond)
		return at
	}
	pause()
	if _, err := w.RetractEvent(ctx, "ev-b", domain.ChangeNote{}); err != nil {
		t.Fatalf("retract: %v", err)
	}
	inWindow := pause()
	un, err := w.UnretractEvent(ctx, "ev-b", domain.ChangeNote{})
	if err != nil {
		t.Fatalf("unretract: %v", err)
	}
	if len(un.Impact.Relinked) != 1 {
		t.Fatalf("relinked = %+v, want cl-queue", un.Impact.Relinked)
	}
	afterRestore := pause()

	engine := query.NewEngineWith(conn.Events, conn.Claims, conn.Relationships, query.EngineDeps{Journal: conn.Journal})
	trustAt := func(at time.Time) float64 {
		t.Helper()
		ans, err := engine.Recall(ctx, query.RecallRequest{Question: "checkout queue drains every hour", Options: query.AnswerOptions{RecordedAsOf: at}})
		if err != nil {
			t.Fatalf("recall: %v", err)
		}
		for _, c := range ans.Claims {
			if c.ID == "cl-queue" {
				return c.TrustScore
			}
		}
		t.Fatalf("cl-queue missing from the read recorded at %s", at)
		return 0
	}
	window, restored := trustAt(inWindow), trustAt(afterRestore)
	if restored <= window {
		t.Errorf("recorded trust after restoration %.4f, inside the window %.4f: want the relinked second source to count only after restoration", restored, window)
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

// ADR 0036 §8: retraction closes a rescored belief's link instead of deleting
// it, so a read recorded before the retraction is the read the brain would
// have given then. At T the belief rests on two independent episodes; a third
// is linked after T, and then one of the first two is retracted. The read at T
// must still count both of its sources, so its trust is unchanged.
func TestRecall_ReadBeforeRetractionKeepsTheClosedLink(t *testing.T) {
	ctx := context.Background()
	w, err := govwrite.New(ctx, "sqlite://"+filepath.Join(t.TempDir(), "closed.db"), nil)
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	conn := w.Conn()
	const text = "the invoice export runs at midnight"
	event := func(id, author string) domain.Event {
		now := time.Now().UTC()
		return domain.Event{ID: id, RunID: "r", SchemaVersion: "v1", SourceInputID: id, CreatedBy: author,
			Content: text + " (" + id + ")", Timestamp: now, IngestedAt: now}
	}
	if _, err := w.Events(ctx, []domain.Event{event("ev-first", "alice"), event("ev-also", "carol")}); err != nil {
		t.Fatalf("event: %v", err)
	}
	now := time.Now().UTC()
	if _, err := w.Claims(ctx, []domain.Claim{{
		ID: "cl-export", Text: text, Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive,
		Confidence: 0.8, CreatedAt: now, ValidFrom: now.Add(-time.Hour),
	}}, govwrite.ClaimReason{Reason: "seed"}); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := w.EvidenceLinks(ctx, []domain.ClaimEvidence{
		{ClaimID: "cl-export", EventID: "ev-first"}, {ClaimID: "cl-export", EventID: "ev-also"},
	}); err != nil {
		t.Fatalf("link: %v", err)
	}
	pause := func() time.Time {
		time.Sleep(5 * time.Millisecond)
		at := time.Now().UTC()
		time.Sleep(5 * time.Millisecond)
		return at
	}
	before := pause()
	if _, err := w.Events(ctx, []domain.Event{event("ev-second", "bob")}); err != nil {
		t.Fatalf("second event: %v", err)
	}
	if _, err := w.EvidenceLinks(ctx, []domain.ClaimEvidence{{ClaimID: "cl-export", EventID: "ev-second"}}); err != nil {
		t.Fatalf("second link: %v", err)
	}
	pause()

	engine := query.NewEngineWith(conn.Events, conn.Claims, conn.Relationships, query.EngineDeps{Journal: conn.Journal})
	readAt := func(at time.Time) (float64, bool) {
		t.Helper()
		ans, err := engine.Recall(ctx, query.RecallRequest{Question: "invoice export runs at midnight", Options: query.AnswerOptions{RecordedAsOf: at}})
		if err != nil {
			t.Fatalf("recall: %v", err)
		}
		for _, c := range ans.Claims {
			if c.ID == "cl-export" {
				return c.TrustScore, true
			}
		}
		return 0, false
	}
	trustThen, ok := readAt(before)
	if !ok {
		t.Fatalf("cl-export missing from the read at T before anything was retracted")
	}

	rep, err := w.RetractEvent(ctx, "ev-first", domain.ChangeNote{Reason: "wrong export log"})
	if err != nil {
		t.Fatalf("retract: %v", err)
	}
	if len(rep.Impact.Rescored) != 1 {
		t.Fatalf("rescored = %+v, want cl-export kept on ev-second", rep.Impact.Rescored)
	}
	live, err := conn.Claims.ListEvidenceByClaimIDs(ctx, []string{"cl-export"})
	if err != nil || len(live) != 2 || slices.ContainsFunc(live, func(l domain.ClaimEvidence) bool { return l.EventID == "ev-first" }) {
		t.Fatalf("live evidence after retraction = %+v (%v), want ev-also and ev-second", live, err)
	}

	trustAgain, ok := readAt(before)
	if !ok {
		t.Fatalf("the read at T lost cl-export after the retraction")
	}
	if trustAgain != trustThen {
		t.Errorf("trust at T changed after the retraction: %.9f -> %.9f, want the pre-retraction trust", trustThen, trustAgain)
	}
}
