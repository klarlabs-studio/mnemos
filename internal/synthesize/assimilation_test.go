package synthesize

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// synthConfidence seeds a fresh 3-success payments/rollback cluster and returns
// the single emitted lesson plus the result, running with the supplied options.
func synthConfidence(t *testing.T, now time.Time, priors []domain.Schema) (domain.Lesson, Result) {
	t.Helper()
	conn := openTestStore(t)
	seedClusterSuccess(t, conn, "payments", domain.ActionKindRollback, 3, now.Add(-12*time.Hour))
	res, err := Synthesize(context.Background(), conn.Actions, conn.Outcomes, conn.Lessons, Options{
		Now:          func() time.Time { return now },
		PriorSchemas: priors,
	})
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	got, err := conn.Lessons.ListAll(context.Background())
	if err != nil {
		t.Fatalf("list lessons: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 lesson, got %d", len(got))
	}
	return got[0], res
}

// TestSynthesize_Assimilation_ConsistentSchemaBoostsConfidence wires ADR 0013 §5
// into synthesis: a cluster whose statement matches an established prior schema
// consolidates with higher confidence (faster), while an unrelated prior leaves
// confidence unchanged and a contradicting prior earns no boost.
func TestSynthesize_Assimilation_ConsistentSchemaBoostsConfidence(t *testing.T) {
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)

	// Baseline: no priors.
	baseLesson, baseRes := synthConfidence(t, now, nil)
	if baseRes.Assimilated != 0 {
		t.Fatalf("baseline should assimilate nothing, got %d", baseRes.Assimilated)
	}
	baseConf := baseLesson.Confidence
	stmt := baseLesson.Statement

	// Consistent prior (identical statement, distinct id) → boosted confidence.
	consistentPrior := domain.Schema{
		ID:           "prior_consistent",
		Statement:    stmt,
		SubjectClass: domain.SubjectClassClass,
		Confidence:   0.9,
		Evidence:     []string{"seed"},
		DerivedAt:    now,
	}
	boosted, boostRes := synthConfidence(t, now, []domain.Schema{consistentPrior})
	if boostRes.Assimilated != 1 {
		t.Fatalf("consistent prior should assimilate 1 cluster, got %d", boostRes.Assimilated)
	}
	if boosted.Confidence <= baseConf {
		t.Fatalf("consistent prior should raise confidence: base=%.4f boosted=%.4f", baseConf, boosted.Confidence)
	}
	if got := boosted.Confidence - baseConf; got > 0.15+1e-9 {
		t.Fatalf("boost must be bounded (≤0.15), got +%.4f", got)
	}

	// Contradicting prior → accommodation, no boost.
	contraPrior := domain.Schema{
		ID:           "prior_contra",
		Statement:    fmt.Sprintf("%s on payments does not reliably succeed", baseLesson.Kind),
		SubjectClass: domain.SubjectClassClass,
		Confidence:   0.9,
		Evidence:     []string{"seed"},
		DerivedAt:    now,
	}
	contra, contraRes := synthConfidence(t, now, []domain.Schema{contraPrior})
	if contraRes.Assimilated != 0 {
		t.Fatalf("contradicting prior must not assimilate, got %d", contraRes.Assimilated)
	}
	if contra.Confidence != baseConf {
		t.Fatalf("contradicting prior must leave confidence unchanged: base=%.4f got=%.4f", baseConf, contra.Confidence)
	}
}

// TestSynthesize_Assimilation_SelfSchemaDoesNotBoost verifies a cluster never
// assimilates into its own prior lesson across re-runs (self-reinforcement guard).
func TestSynthesize_Assimilation_SelfSchemaDoesNotBoost(t *testing.T) {
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)

	conn := openTestStore(t)
	seedClusterSuccess(t, conn, "payments", domain.ActionKindRollback, 3, now.Add(-12*time.Hour))

	// First pass to learn the cluster's own id + statement.
	if _, err := Synthesize(context.Background(), conn.Actions, conn.Outcomes, conn.Lessons, Options{
		Now: func() time.Time { return now },
	}); err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	got, err := conn.Lessons.ListAll(context.Background())
	if err != nil {
		t.Fatalf("list lessons: %v", err)
	}
	self := got[0] // its ID == clusterID(c), so it must be excluded from priors

	res, err := Synthesize(context.Background(), conn.Actions, conn.Outcomes, conn.Lessons, Options{
		Now:          func() time.Time { return now },
		PriorSchemas: []domain.Schema{self},
	})
	if err != nil {
		t.Fatalf("synthesize (self prior): %v", err)
	}
	if res.Assimilated != 0 {
		t.Fatalf("a cluster must not assimilate into itself, got %d", res.Assimilated)
	}
}
