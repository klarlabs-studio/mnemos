package synthesize

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"

	_ "go.klarlabs.de/mnemos/internal/store/memory"
)

func openTestStore(t *testing.T) *store.Conn {
	t.Helper()
	conn, err := store.Open(context.Background(), "memory://")
	if err != nil {
		t.Fatalf("open memory store: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// seedClusterSuccess inserts a (subject, kind) cluster with N
// successful outcomes. Returns the action ids in insertion order.
func seedClusterSuccess(t *testing.T, conn *store.Conn, subject string, kind domain.ActionKind, n int, base time.Time) []string {
	t.Helper()
	ctx := context.Background()
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		aid := actionIDForTest(subject, string(kind), i)
		oid := outcomeIDForTest(subject, string(kind), i)
		at := base.Add(time.Duration(i) * time.Hour)
		if err := conn.Actions.Append(ctx, domain.Action{
			ID: aid, Kind: kind, Subject: subject, At: at,
		}); err != nil {
			t.Fatalf("seed action: %v", err)
		}
		if err := conn.Outcomes.Append(ctx, domain.Outcome{
			ID: oid, ActionID: aid, Result: domain.OutcomeResultSuccess,
			ObservedAt: at.Add(5 * time.Minute),
		}); err != nil {
			t.Fatalf("seed outcome: %v", err)
		}
		ids = append(ids, aid)
	}
	return ids
}

func actionIDForTest(subject, kind string, idx int) string {
	return "ac_" + subject + "_" + kind + "_" + itoa(idx)
}
func outcomeIDForTest(subject, kind string, idx int) string {
	return "oc_" + subject + "_" + kind + "_" + itoa(idx)
}
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	digits := []byte{}
	for ; i > 0; i /= 10 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
	}
	return string(digits)
}

func TestSynthesize_EmitsLessonOnConsistentSuccess(t *testing.T) {
	conn := openTestStore(t)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	seedClusterSuccess(t, conn, "payments", domain.ActionKindRollback, 3, now.Add(-12*time.Hour))

	res, err := Synthesize(context.Background(), conn.Actions, conn.Outcomes, conn.Lessons, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	if res.LessonsEmitted != 1 {
		t.Fatalf("want 1 lesson, got %d (clusters=%d skipped=%d)", res.LessonsEmitted, res.Clusters, res.Skipped)
	}
	got, err := conn.Lessons.ListAll(context.Background())
	if err != nil {
		t.Fatalf("list lessons: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 stored lesson, got %d", len(got))
	}
	l := got[0]
	if l.Scope.Service != "payments" || l.Kind != string(domain.ActionKindRollback) {
		t.Fatalf("scope/kind mismatch: %+v", l)
	}
	if l.Confidence < domain.LessonConfidenceMin {
		t.Fatalf("confidence below floor: %v", l.Confidence)
	}
	if len(l.Evidence) != 3 {
		t.Fatalf("want 3 evidence ids, got %d", len(l.Evidence))
	}
}

func TestSynthesize_SkipsBelowMinCorroboration(t *testing.T) {
	conn := openTestStore(t)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	seedClusterSuccess(t, conn, "search", domain.ActionKindDeploy, 2, now.Add(-1*time.Hour))

	res, err := Synthesize(context.Background(), conn.Actions, conn.Outcomes, conn.Lessons, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	if res.LessonsEmitted != 0 {
		t.Fatalf("want 0 lessons (below MinCorroboration), got %d", res.LessonsEmitted)
	}
	if res.Skipped != 1 {
		t.Fatalf("want 1 skipped, got %d", res.Skipped)
	}
}

func TestSynthesize_DropsContradictoryCluster(t *testing.T) {
	conn := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	base := now.Add(-6 * time.Hour)

	// 3 actions, 2 success, 2 failure across them = 50/50 split = below 2/3 consistency.
	for i := 0; i < 3; i++ {
		aid := actionIDForTest("orders", "deploy", i)
		at := base.Add(time.Duration(i) * time.Hour)
		if err := conn.Actions.Append(ctx, domain.Action{
			ID: aid, Kind: domain.ActionKindDeploy, Subject: "orders", At: at,
		}); err != nil {
			t.Fatalf("seed action: %v", err)
		}
		// Pair each action with one success + one failure to force 50/50.
		if err := conn.Outcomes.Append(ctx, domain.Outcome{
			ID: "oc_s_" + itoa(i), ActionID: aid, Result: domain.OutcomeResultSuccess, ObservedAt: at.Add(5 * time.Minute),
		}); err != nil {
			t.Fatalf("seed outcome s: %v", err)
		}
		if err := conn.Outcomes.Append(ctx, domain.Outcome{
			ID: "oc_f_" + itoa(i), ActionID: aid, Result: domain.OutcomeResultFailure, ObservedAt: at.Add(10 * time.Minute),
		}); err != nil {
			t.Fatalf("seed outcome f: %v", err)
		}
	}

	res, err := Synthesize(ctx, conn.Actions, conn.Outcomes, conn.Lessons, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	if res.LessonsEmitted != 0 {
		t.Fatalf("want 0 lessons (contradictory), got %d", res.LessonsEmitted)
	}
}

func TestSynthesize_IsIdempotent(t *testing.T) {
	conn := openTestStore(t)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	seedClusterSuccess(t, conn, "payments", domain.ActionKindRollback, 4, now.Add(-2*time.Hour))

	for i := 0; i < 3; i++ {
		if _, err := Synthesize(context.Background(), conn.Actions, conn.Outcomes, conn.Lessons, Options{Now: func() time.Time { return now }}); err != nil {
			t.Fatalf("synthesize iter %d: %v", i, err)
		}
	}
	got, err := conn.Lessons.ListAll(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 lesson after 3 runs, got %d", len(got))
	}
	count, err := conn.Lessons.CountAll(context.Background())
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("count: want 1, got %d", count)
	}
}

func TestSynthesize_ConfidenceDecaysWithAge(t *testing.T) {
	conn := openTestStore(t)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	// 3 actions a year ago — recency factor should drop confidence
	// below threshold even with perfect corroboration + consistency.
	seedClusterSuccess(t, conn, "legacy", domain.ActionKindRestart, 3, now.AddDate(-1, 0, 0))

	res, err := Synthesize(context.Background(), conn.Actions, conn.Outcomes, conn.Lessons, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	if res.LessonsEmitted != 0 {
		t.Fatalf("expected aged-out cluster to be skipped, got %d emitted", res.LessonsEmitted)
	}
}

func TestSynthesizePlaybooks_EmitsFromLessonCluster(t *testing.T) {
	conn := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)

	// Seed two high-confidence lessons sharing the same trigger.
	for i, statement := range []string{"rollback recovers payments fast", "rollback succeeded again"} {
		if err := conn.Lessons.Append(ctx, domain.Lesson{
			ID:         "ls_" + itoa(i),
			Statement:  statement,
			Scope:      domain.LessonScope{Service: "payments"},
			Trigger:    "latency_spike_after_deploy",
			Kind:       "rollback",
			Evidence:   []string{"ac_" + itoa(i)},
			Confidence: 0.78,
			DerivedAt:  now,
		}); err != nil {
			t.Fatalf("seed lesson: %v", err)
		}
	}

	res, err := Playbooks(ctx, conn.Lessons, conn.Playbooks, PlaybookOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("synthesize playbooks: %v", err)
	}
	if res.PlaybooksEmitted != 1 {
		t.Fatalf("want 1 playbook, got %d (clusters=%d skipped=%d)", res.PlaybooksEmitted, res.TriggerClusters, res.Skipped)
	}
	got, err := conn.Playbooks.ListByTrigger(ctx, "latency_spike_after_deploy")
	if err != nil {
		t.Fatalf("list by trigger: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 playbook stored, got %d", len(got))
	}
	pb := got[0]
	if pb.Trigger != "latency_spike_after_deploy" || pb.Scope.Service != "payments" {
		t.Fatalf("trigger/scope mismatch: %+v", pb)
	}
	if len(pb.Steps) < 3 {
		t.Fatalf("want at least 3 steps, got %d", len(pb.Steps))
	}
	if pb.Confidence < domain.PlaybookConfidenceMin {
		t.Fatalf("confidence below floor: %v", pb.Confidence)
	}
	if len(pb.DerivedFromLessons) != 2 {
		t.Fatalf("want 2 lesson links, got %d", len(pb.DerivedFromLessons))
	}
}

func TestSynthesizePlaybooks_SkipsTriggerlessLessons(t *testing.T) {
	conn := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	if err := conn.Lessons.Append(ctx, domain.Lesson{
		ID: "ls_a", Statement: "no trigger", Scope: domain.LessonScope{Service: "payments"},
		Kind: "rollback", Evidence: []string{"ac_a"}, Confidence: 0.9, DerivedAt: now,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, err := Playbooks(ctx, conn.Lessons, conn.Playbooks, PlaybookOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	if res.PlaybooksEmitted != 0 {
		t.Fatalf("want 0 playbooks (no trigger label), got %d", res.PlaybooksEmitted)
	}
}

func TestSynthesizePlaybooks_IsIdempotent(t *testing.T) {
	conn := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	if err := conn.Lessons.Append(ctx, domain.Lesson{
		ID: "ls_a", Statement: "x", Scope: domain.LessonScope{Service: "payments"},
		Trigger: "x_trigger", Kind: "rollback", Evidence: []string{"ac_a"}, Confidence: 0.8, DerivedAt: now,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := Playbooks(ctx, conn.Lessons, conn.Playbooks, PlaybookOptions{Now: func() time.Time { return now }}); err != nil {
			t.Fatalf("synthesize iter %d: %v", i, err)
		}
	}
	count, err := conn.Playbooks.CountAll(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("want 1 playbook after 3 runs, got %d", count)
	}
}

// seedClusterFailure inserts a (subject, kind) cluster with N failed outcomes.
func seedClusterFailure(t *testing.T, conn *store.Conn, subject string, kind domain.ActionKind, n int, base time.Time) []string {
	t.Helper()
	ctx := context.Background()
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		aid := "fail_ac_" + subject + "_" + string(kind) + "_" + itoa(i)
		oid := "fail_oc_" + subject + "_" + string(kind) + "_" + itoa(i)
		at := base.Add(time.Duration(i) * time.Hour)
		if err := conn.Actions.Append(ctx, domain.Action{
			ID: aid, Kind: kind, Subject: subject, At: at,
		}); err != nil {
			t.Fatalf("seed action: %v", err)
		}
		if err := conn.Outcomes.Append(ctx, domain.Outcome{
			ID: oid, ActionID: aid, Result: domain.OutcomeResultFailure,
			ObservedAt: at.Add(5 * time.Minute),
		}); err != nil {
			t.Fatalf("seed outcome: %v", err)
		}
		ids = append(ids, aid)
	}
	return ids
}

// TestSynthesize_EmitsAntiLessonOnConsistentFailure verifies that a
// cluster of ≥3 failures is synthesised into a negative-polarity lesson.
func TestSynthesize_EmitsAntiLessonOnConsistentFailure(t *testing.T) {
	conn := openTestStore(t)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	seedClusterFailure(t, conn, "payments", domain.ActionKindDeploy, 3, now.Add(-12*time.Hour))

	res, err := Synthesize(context.Background(), conn.Actions, conn.Outcomes, conn.Lessons, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	if res.LessonsEmitted != 1 {
		t.Fatalf("want 1 anti-lesson, got %d (clusters=%d skipped=%d)", res.LessonsEmitted, res.Clusters, res.Skipped)
	}
	lessons, err := conn.Lessons.ListAll(context.Background())
	if err != nil {
		t.Fatalf("list lessons: %v", err)
	}
	if len(lessons) != 1 {
		t.Fatalf("want 1 lesson stored, got %d", len(lessons))
	}
	l := lessons[0]
	if l.Polarity != domain.LessonPolarityNegative {
		t.Errorf("polarity = %q, want %q", l.Polarity, domain.LessonPolarityNegative)
	}
	if l.Statement == "" {
		t.Error("statement must not be empty for anti-lesson")
	}
}

// TestSynthesize_PositiveLessonHasPositivePolarity verifies that a
// success cluster is tagged with positive polarity.
func TestSynthesize_PositiveLessonHasPositivePolarity(t *testing.T) {
	conn := openTestStore(t)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	seedClusterSuccess(t, conn, "payments", domain.ActionKindRollback, 3, now.Add(-12*time.Hour))

	_, err := Synthesize(context.Background(), conn.Actions, conn.Outcomes, conn.Lessons, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	lessons, err := conn.Lessons.ListAll(context.Background())
	if err != nil {
		t.Fatalf("list lessons: %v", err)
	}
	if len(lessons) != 1 {
		t.Fatalf("want 1 lesson, got %d", len(lessons))
	}
	if lessons[0].Polarity != domain.LessonPolarityPositive {
		t.Errorf("polarity = %q, want %q", lessons[0].Polarity, domain.LessonPolarityPositive)
	}
}

// TestSynthesize_AntiLessonBelowCorroborationThreshold verifies that
// a failure cluster with only 2 actions is not emitted (below MinCorroboration=3).
func TestSynthesize_AntiLessonBelowCorroborationThreshold(t *testing.T) {
	conn := openTestStore(t)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	seedClusterFailure(t, conn, "search", domain.ActionKindDeploy, 2, now.Add(-12*time.Hour))

	res, err := Synthesize(context.Background(), conn.Actions, conn.Outcomes, conn.Lessons, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	if res.LessonsEmitted != 0 {
		t.Fatalf("want 0 lessons for under-corroborated failure cluster, got %d", res.LessonsEmitted)
	}
}
