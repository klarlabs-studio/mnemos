package mnemos

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// seedPlaybook wires a playbook → lessons → evidence-actions chain and records
// one outcome per action, so reinforcePlaybooks has a success rate to measure.
// Each lesson gets exactly one evidence action; results[i] is that action's
// outcome verdict. Returns the playbook id.
func seedPlaybook(t *testing.T, m *memory, id string, confidence float64, results []domain.OutcomeResult) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	lessonIDs := make([]string, 0, len(results))
	for i, r := range results {
		aid := id + "_a" + string(rune('0'+i))
		lid := id + "_l" + string(rune('0'+i))
		if err := m.conn.Lessons.Append(ctx, domain.Lesson{
			ID: lid, Statement: "lesson " + lid, Confidence: 0.8,
			Evidence: []string{aid}, DerivedAt: now, Source: "synthesize",
		}); err != nil {
			t.Fatalf("seed lesson %s: %v", lid, err)
		}
		if err := m.conn.Outcomes.Append(ctx, domain.Outcome{
			ID: id + "_o" + string(rune('0'+i)), ActionID: aid,
			Result: r, ObservedAt: now, Source: "push",
		}); err != nil {
			t.Fatalf("seed outcome for %s: %v", aid, err)
		}
		lessonIDs = append(lessonIDs, lid)
	}
	if err := m.conn.Playbooks.Append(ctx, domain.Playbook{
		ID: id, Trigger: "trigger_" + id, Statement: "playbook " + id,
		DerivedFromLessons: lessonIDs, Confidence: confidence,
		DerivedAt: now, Source: "synthesize",
	}); err != nil {
		t.Fatalf("seed playbook %s: %v", id, err)
	}
	return id
}

func playbookConfidence(t *testing.T, m *memory, id string) float64 {
	t.Helper()
	p, err := m.conn.Playbooks.GetByID(context.Background(), id)
	if err != nil {
		t.Fatalf("get playbook %s: %v", id, err)
	}
	return p.Confidence
}

// TestConsolidate_ReinforcesPlaybooks verifies the skill loop: a playbook whose
// underlying actions succeed is reinforced upward, one whose actions fail decays
// downward, and both move toward the observed success rate by the retention
// blend (0.7·prior + 0.3·rate).
func TestConsolidate_ReinforcesPlaybooks(t *testing.T) {
	m := calibMem(t)
	ctx := context.Background()

	// pbWin: prior 0.5, all 3 actions succeeded → rate 1.0 →
	// 0.7*0.5 + 0.3*1.0 = 0.65 (reinforced up).
	pbWin := seedPlaybook(t, m, "pb_win", 0.5, []domain.OutcomeResult{
		domain.OutcomeResultSuccess, domain.OutcomeResultSuccess, domain.OutcomeResultSuccess,
	})
	// pbFail: prior 0.8, all 3 actions failed → rate 0.0 →
	// 0.7*0.8 + 0.3*0.0 = 0.56 (decayed down).
	pbFail := seedPlaybook(t, m, "pb_fail", 0.8, []domain.OutcomeResult{
		domain.OutcomeResultFailure, domain.OutcomeResultFailure, domain.OutcomeResultFailure,
	})

	res, err := m.Consolidate(ctx, ConsolidateOptions{ReinforcePlaybooks: true})
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	if res.PlaybooksReinforced != 2 {
		t.Fatalf("PlaybooksReinforced = %d, want 2", res.PlaybooksReinforced)
	}
	if got := playbookConfidence(t, m, pbWin); !approx(got, 0.65, 1e-6) {
		t.Errorf("pb_win confidence = %v, want ~0.65 (reinforced toward success)", got)
	}
	if got := playbookConfidence(t, m, pbFail); !approx(got, 0.56, 1e-6) {
		t.Errorf("pb_fail confidence = %v, want ~0.56 (decayed toward failure)", got)
	}
}

// TestConsolidate_ReinforcePartialAndMixed verifies partial counts as 0.5 and a
// mixed success/failure batch reinforces to the mean.
func TestConsolidate_ReinforcePartialAndMixed(t *testing.T) {
	m := calibMem(t)
	ctx := context.Background()

	// prior 0.6; outcomes success/failure/partial → weights 1,0,0.5 → rate 0.5 →
	// 0.7*0.6 + 0.3*0.5 = 0.57.
	pb := seedPlaybook(t, m, "pb_mixed", 0.6, []domain.OutcomeResult{
		domain.OutcomeResultSuccess, domain.OutcomeResultFailure, domain.OutcomeResultPartial,
	})
	if _, err := m.Consolidate(ctx, ConsolidateOptions{ReinforcePlaybooks: true}); err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	if got := playbookConfidence(t, m, pb); !approx(got, 0.57, 1e-6) {
		t.Errorf("pb_mixed confidence = %v, want ~0.57", got)
	}
}

// TestConsolidate_ReinforceRespectsMinOutcomes verifies a playbook with fewer
// than ReinforceMinOutcomes scored outcomes is left untouched (too noisy), and
// that "unknown" outcomes carry no signal.
func TestConsolidate_ReinforceRespectsMinOutcomes(t *testing.T) {
	m := calibMem(t)
	ctx := context.Background()

	// One scored outcome (success) + one "unknown" (ignored) → scored count 1 <
	// ReinforceMinOutcomes (2) → no change.
	pb := seedPlaybook(t, m, "pb_thin", 0.4, []domain.OutcomeResult{
		domain.OutcomeResultSuccess, domain.OutcomeResultUnknown,
	})
	res, err := m.Consolidate(ctx, ConsolidateOptions{ReinforcePlaybooks: true})
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	if res.PlaybooksReinforced != 0 {
		t.Fatalf("PlaybooksReinforced = %d, want 0 (below min outcomes)", res.PlaybooksReinforced)
	}
	if got := playbookConfidence(t, m, pb); !approx(got, 0.4, 1e-9) {
		t.Errorf("pb_thin confidence = %v, want unchanged 0.4", got)
	}
}

// TestConsolidate_ReinforceOffByDefault verifies the pass does nothing unless
// ReinforcePlaybooks is set.
func TestConsolidate_ReinforceOffByDefault(t *testing.T) {
	m := calibMem(t)
	ctx := context.Background()
	pb := seedPlaybook(t, m, "pb_default", 0.5, []domain.OutcomeResult{
		domain.OutcomeResultSuccess, domain.OutcomeResultSuccess,
	})
	res, err := m.Consolidate(ctx, ConsolidateOptions{})
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	if res.PlaybooksReinforced != 0 {
		t.Fatalf("PlaybooksReinforced = %d, want 0 (flag off)", res.PlaybooksReinforced)
	}
	if got := playbookConfidence(t, m, pb); !approx(got, 0.5, 1e-9) {
		t.Errorf("pb_default confidence = %v, want unchanged 0.5", got)
	}
}
