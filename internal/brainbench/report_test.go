package brainbench

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func sampleReport() Report {
	return BuildReport([]ScenarioResult{
		{
			ID:       "s1",
			Activity: Activity{Total: 5, Forgotten: 5},
			Validity: Validity{Deterministic: true, ProbeCount: 4, MinResolvableRateDelta: 0.25},
			Comparisons: []Comparison{
				Compare(
					KnownMetric(MetricGoldSurvival, HigherBetter, 1, 4, "ratio"),
					KnownMetric(MetricGoldSurvival, HigherBetter, 0.5, 4, "ratio"),
				),
				Compare(
					KnownMetric(MetricAnswerMRR, HigherBetter, 0.3, 4, "ratio"),
					KnownMetric(MetricAnswerMRR, HigherBetter, 0.9, 4, "ratio"),
				),
			},
			Summary: Summary{Better: 1, Worse: 1},
			Verdict: VerdictMixed,
		},
	})
}

// TestReport_JSONIsMachineReadable checks the contract the make target depends
// on: valid JSON carrying the deltas, the verdict, and the caveats.
func TestReport_JSONIsMachineReadable(t *testing.T) {
	var buf bytes.Buffer
	if err := sampleReport().WriteJSON(&buf); err != nil {
		t.Fatalf("write json: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	for _, key := range []string{"harness", "version", "config", "limitations", "scenarios", "summary", "verdicts"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("report JSON is missing %q", key)
		}
	}
}

// TestReport_LimitationsTravelWithTheNumbers pins the rule that the caveats
// ship INSIDE the machine-readable output. A JSON file gets pasted into an
// issue or a prompt without its README, and an unqualified
// "consolidation improved MRR" is a materially misleading claim.
func TestReport_LimitationsTravelWithTheNumbers(t *testing.T) {
	r := sampleReport()
	if len(r.Limitations) == 0 {
		t.Fatal("report carries no limitations block")
	}
	joined := strings.ToLower(strings.Join(r.Limitations, " "))
	for _, must := range []string{"llm", "stub", "scale", "promotion", "downstream"} {
		if !strings.Contains(joined, must) {
			t.Errorf("limitations do not mention %q", must)
		}
	}

	var buf bytes.Buffer
	if err := r.WriteJSON(&buf); err != nil {
		t.Fatalf("write json: %v", err)
	}
	if !strings.Contains(buf.String(), "limitations") {
		t.Error("limitations must be embedded in the JSON, not only in docs")
	}
}

// TestReport_HumanOutputShowsRegressions checks that a degraded metric is
// rendered as prominently as an improved one — the report must not bury bad
// news.
func TestReport_HumanOutputShowsRegressions(t *testing.T) {
	var buf bytes.Buffer
	if err := sampleReport().WriteHuman(&buf); err != nil {
		t.Fatalf("write human: %v", err)
	}
	out := buf.String()
	for _, want := range []string{MetricGoldSurvival, "worse", "better", string(VerdictMixed), "DOES NOT MEASURE"} {
		if !strings.Contains(out, want) {
			t.Errorf("human report does not contain %q", want)
		}
	}
}

func TestReport_HasRegression(t *testing.T) {
	if !sampleReport().HasRegression() {
		t.Fatal("a report with a degraded metric must report a regression")
	}
	clean := BuildReport([]ScenarioResult{{Summary: Summary{Better: 3, Unchanged: 4}}})
	if clean.HasRegression() {
		t.Fatal("a report with no degraded metric must not report a regression")
	}
}

func TestReport_VerdictTally(t *testing.T) {
	r := BuildReport([]ScenarioResult{
		{Verdict: VerdictImproved, Summary: Summary{Better: 2}},
		{Verdict: VerdictImproved, Summary: Summary{Better: 1}},
		{Verdict: VerdictInert},
	})
	if r.Verdicts[VerdictImproved] != 2 || r.Verdicts[VerdictInert] != 1 {
		t.Fatalf("verdict tally = %v", r.Verdicts)
	}
	if r.Summary.Better != 3 {
		t.Fatalf("aggregate better = %d, want 3", r.Summary.Better)
	}
}

// TestReport_BaselineWinIsShownAndNotAProcessRegression pins how a baseline
// win is reported: visibly, in the scenario table and the totals, and outside
// the process A/B summary. Folding it into Summary.Worse would make
// -fail-on-regression blame consolidation for a property of retrieval.
func TestReport_BaselineWinIsShownAndNotAProcessRegression(t *testing.T) {
	mn := MetricSet{Metrics: []Metric{KnownMetric(MetricAnswerMRR, HigherBetter, 0.5, 4, "ratio")}}
	base := MetricSet{Metrics: []Metric{KnownMetric(MetricAnswerMRR, HigherBetter, 1, 4, "ratio")}}
	cs, sum := compareWithBaseline(mn, base)
	r := BuildReport([]ScenarioResult{{
		ID:        "s",
		Validity:  Validity{Deterministic: true, ProbeCount: 4},
		Verdict:   VerdictNoEffect,
		Questions: []string{"a", "b", "c", "d"},
		Baselines: []BaselineResult{{Name: BaselineBM25, Metrics: base, Comparisons: cs, Summary: sum}},
	}})
	if r.HasRegression() {
		t.Fatal("a baseline win was counted as a process regression")
	}
	if got := r.BaselineSummary[BaselineBM25].BaselineBetter; got != 1 {
		t.Fatalf("baseline_summary baseline_better = %d, want 1", got)
	}
	var buf bytes.Buffer
	if err := r.WriteHuman(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"BASELINE", "baseline-better", "MNEMOS vs raw_bm25: 0 mnemos-better, 1 baseline-better"} {
		if !strings.Contains(out, want) {
			t.Errorf("human report missing %q", want)
		}
	}
	if !strings.Contains(strings.ToLower(strings.Join(r.Limitations, " ")), "baselines see the documents only") {
		t.Error("limitations do not state what the baselines cannot see")
	}
}
