package brainbench

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// reproducibleJSON renders a scenario result as the report would, with the
// fields that measure the machine rather than the brain zeroed: run time and
// the cost report's latencies. Those are wall-clock by design and never feed
// a metric. Everything else — every metric on every arm, per-question ranks,
// activity counters, seed shape, phase instants, the retraction track — must
// be identical between two runs of the same scenario.
func reproducibleJSON(t *testing.T, res ScenarioResult) []string {
	t.Helper()
	res.DurationMS = 0
	systems := make([]SystemCost, len(res.Cost.Systems))
	for i, s := range res.Cost.Systems {
		s.BuildMS, s.CurationMS = 0, 0
		s.QueryMeanMS, s.QueryP50MS, s.QueryMaxMS = 0, 0, 0
		systems[i] = s
	}
	res.Cost.Systems = systems
	raw, err := json.MarshalIndent(res, "", " ")
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	return strings.Split(string(raw), "\n")
}

// TestRunAblation_IsReproducibleAcrossRuns is the ablation-mode half of issue
// #396. The ablation packs whole ranked lists into token budgets, so it is
// sensitive to the order of equally-scored beliefs in the tail — an order that
// followed the extractor's random claim ids, and so differed between seeds
// while the within-run noise arm still agreed.
func TestRunAblation_IsReproducibleAcrossRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds every shipped scenario twice")
	}
	scenarios, err := LoadScenarios(filepath.Join("..", "..", "data", "eval", "brainbench"))
	if err != nil {
		t.Fatalf("load scenarios: %v", err)
	}
	var first string
	for i := range 2 {
		rep, err := RunAblation(context.Background(), scenarios, t.TempDir(), nil)
		if err != nil {
			t.Fatalf("ablation run %d: %v", i, err)
		}
		raw, err := json.MarshalIndent(rep, "", " ")
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if i == 0 {
			first = string(raw)
			continue
		}
		got, want := strings.Split(string(raw), "\n"), strings.Split(first, "\n")
		if len(got) != len(want) {
			t.Fatalf("ablation run %d report has %d lines, run 0 has %d", i, len(got), len(want))
		}
		diffs := 0
		for j := range got {
			if got[j] != want[j] {
				diffs++
				if diffs <= 10 {
					t.Errorf("ablation run %d differs from run 0 at line %d:\n  run0: %s\n  run%d: %s",
						i, j, strings.TrimSpace(want[j]), i, strings.TrimSpace(got[j]))
				}
			}
		}
	}
}

// TestRun_IsReproducibleAcrossRuns pins issue #396. The harness is documented
// as offline and deterministic, and Validity.Deterministic checks that two arms
// of ONE run agree, but nothing checked that two separate runs agree: the
// noisy_capture scenario's answer_mrr moved between 0.55 and 0.383 from run to
// run, and mean_trust in the 8th decimal, so any before/after comparison of a
// change was confounded with that drift. Every shipped scenario, seeded fresh
// three times, must report the same result.
func TestRun_IsReproducibleAcrossRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds and measures every shipped scenario three times")
	}
	scenarios, err := LoadScenarios(filepath.Join("..", "..", "data", "eval", "brainbench"))
	if err != nil {
		t.Fatalf("load scenarios: %v", err)
	}
	const runs = 3
	for _, sc := range scenarios {
		t.Run(sc.ID, func(t *testing.T) {
			var first []string
			for i := range runs {
				res, err := Run(context.Background(), sc, filepath.Join(t.TempDir(), fmt.Sprintf("run%d", i)))
				if err != nil {
					t.Fatalf("run %d: %v", i, err)
				}
				got := reproducibleJSON(t, res)
				if i == 0 {
					first = got
					continue
				}
				if len(got) != len(first) {
					t.Fatalf("run %d report has %d lines, run 0 has %d", i, len(got), len(first))
				}
				diffs := 0
				for j := range got {
					if got[j] != first[j] {
						diffs++
						if diffs <= 10 {
							t.Errorf("run %d differs from run 0 at line %d:\n  run0: %s\n  run%d: %s",
								i, j, strings.TrimSpace(first[j]), i, strings.TrimSpace(got[j]))
						}
					}
				}
			}
		})
	}
}
