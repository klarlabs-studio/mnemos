package synthesize

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
)

func seedOutcomes(t *testing.T, conn *store.Conn, prefix, subject string, kind domain.ActionKind, meta map[string]string, results []domain.OutcomeResult, base time.Time) {
	t.Helper()
	ctx := context.Background()
	for i, r := range results {
		aid := "ac_" + prefix + "_" + itoa(i)
		at := base.Add(time.Duration(i) * time.Hour)
		if err := conn.Actions.Append(ctx, domain.Action{ID: aid, Kind: kind, Subject: subject, At: at, Metadata: meta}); err != nil {
			t.Fatalf("seed action: %v", err)
		}
		if err := conn.Outcomes.Append(ctx, domain.Outcome{ID: "oc_" + prefix + "_" + itoa(i), ActionID: aid, Result: r, ObservedAt: at.Add(5 * time.Minute)}); err != nil {
			t.Fatalf("seed outcome: %v", err)
		}
	}
}

// The Context contract: synthesis clusters strictly within a context, so
// "rollback works in staging" cannot become a lesson that also covers prod.
// Actions carry env and team as metadata (domain.ActionMetadataEnv / Team).
func TestSynthesize_KeepsEnvironmentsApart(t *testing.T) {
	conn := openTestStore(t)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	ok := []domain.OutcomeResult{domain.OutcomeResultSuccess, domain.OutcomeResultSuccess, domain.OutcomeResultSuccess}
	bad := []domain.OutcomeResult{domain.OutcomeResultFailure, domain.OutcomeResultFailure, domain.OutcomeResultFailure}
	seedOutcomes(t, conn, "stg", "payments", domain.ActionKindRollback,
		map[string]string{domain.ActionMetadataEnv: "staging", domain.ActionMetadataTeam: "pay"}, ok, now.Add(-12*time.Hour))
	seedOutcomes(t, conn, "prd", "payments", domain.ActionKindRollback,
		map[string]string{domain.ActionMetadataEnv: "prod", domain.ActionMetadataTeam: "pay"}, bad, now.Add(-12*time.Hour))

	res, err := Synthesize(context.Background(), conn.Actions, conn.Outcomes, conn.Lessons, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	if res.LessonsEmitted != 2 {
		t.Fatalf("want one lesson per environment, got %d (clusters=%d)", res.LessonsEmitted, res.Clusters)
	}
	lessons, err := conn.Lessons.ListAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byEnv := map[string]domain.Lesson{}
	for _, l := range lessons {
		byEnv[l.Scope.Env] = l
	}
	if l := byEnv["staging"]; l.Polarity != domain.LessonPolarityPositive || l.Scope.Team != "pay" || l.Scope.Service != "payments" {
		t.Errorf("staging lesson = %+v, want a positive lesson scoped to payments/staging/pay", l)
	}
	if l := byEnv["prod"]; l.Polarity != domain.LessonPolarityNegative || l.Scope.Team != "pay" {
		t.Errorf("prod lesson = %+v, want an anti-lesson scoped to payments/prod/pay", l)
	}
}

// Outcomes nobody could read teach nothing. A cluster whose dominant result is
// unknown used to become a lesson with positive polarity.
func TestSynthesize_UnknownDominatedClusterTeachesNothing(t *testing.T) {
	conn := openTestStore(t)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	seedOutcomes(t, conn, "unk", "search", domain.ActionKindDeploy, nil,
		[]domain.OutcomeResult{domain.OutcomeResultUnknown, domain.OutcomeResultUnknown, domain.OutcomeResultUnknown}, now.Add(-12*time.Hour))

	res, err := Synthesize(context.Background(), conn.Actions, conn.Outcomes, conn.Lessons, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	if res.LessonsEmitted != 0 {
		lessons, _ := conn.Lessons.ListAll(context.Background())
		t.Fatalf("an unknown-dominated cluster produced %d lesson(s): %+v", res.LessonsEmitted, lessons)
	}
}

// Ties between results must resolve the same way every run.
func TestDominantResult_TieIsDeterministic(t *testing.T) {
	os := []domain.Outcome{{Result: domain.OutcomeResultSuccess}, {Result: domain.OutcomeResultFailure}}
	first, _ := dominantResult(os)
	for i := 0; i < 200; i++ {
		if got, _ := dominantResult(os); got != first {
			t.Fatalf("tie resolved to %q, then %q", first, got)
		}
	}
}
