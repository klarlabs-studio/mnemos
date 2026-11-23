package brainbench

import (
	"context"
	"strings"
	"testing"
)

// shiftingYAML is an inline shifting world: one fact changes (evidence in r2),
// one co-rested fact stays true, and one belief gets a single outlier surprise
// with no contrary evidence. Inline so the tests pin harness behaviour, not
// the shipped fixture.
const shiftingYAML = `
id: unit_shifting
description: inline shifting world
corpus:
  - id: t1
    age_days: 30
    text: The payments API timeout is 30 seconds.
  - id: t2
    age_days: 20
    text: The payments API timeout is 30 seconds.
  - id: retries
    age_days: 30
    text: Payment retries are idempotent.
  - id: ledger
    age_days: 30
    text: The ledger database is PostgreSQL 16.
probes:
  - id: seed
    query: what is the payments API timeout
    expect: timeout is 30 seconds
shifting_world:
  sleep: {forget_refuted: true, reinforce_validated: true, credit: true}
  rounds:
    - id: r1
      decisions:
        - id: pay
          recall: payments API timeout and whether payment retries are idempotent
          rests_on: [timeout is 30 seconds, retries are idempotent]
          predicted: 30
          tolerance: 5
          observed: 5
        - id: ledger
          recall: which database does the ledger use
          rests_on: [ledger database is PostgreSQL 16]
          predicted: 100
          tolerance: 20
          observed: 400
    - id: r2
      docs:
        - id: change
          text: The payments API timeout is 5 seconds.
      decisions:
        - id: pay
          recall: payments API timeout and whether payment retries are idempotent
          rests_on: [timeout is 30 seconds, retries are idempotent]
          predicted: 30
          tolerance: 5
          observed: 5
    - id: r3
      decisions:
        - id: ledger
          recall: which database does the ledger use
          rests_on: [ledger database is PostgreSQL 16]
          predicted: 100
          tolerance: 20
          observed: 101
  probes:
    - id: timeout
      role: changed
      evidence_in: r2
      query: what is the payments API timeout
      expect: timeout is 5 seconds
      must_not: [timeout is 30 seconds]
    - id: retries
      role: stable
      query: are payment retries idempotent
      expect: retries are idempotent
    - id: ledger
      role: stable
      query: which database does the ledger use
      expect: ledger database is PostgreSQL 16
`

func shiftArm(t *testing.T, sw *ShiftingResult, name string) ShiftArm {
	t.Helper()
	for _, a := range sw.Arms {
		if a.Name == name {
			return a
		}
	}
	t.Fatalf("no arm %s in %+v", name, sw.Arms)
	return ShiftArm{}
}

func shiftMetric(t *testing.T, a ShiftArm, name string) Metric {
	t.Helper()
	m, ok := a.Metrics.Get(name)
	if !ok {
		t.Fatalf("arm %s has no metric %s", a.Name, name)
	}
	return m
}

func TestRun_ShiftingWorldTrack(t *testing.T) {
	sc, err := ParseScenario([]byte(shiftingYAML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	res, err := Run(context.Background(), sc, t.TempDir())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	sw := res.ShiftingWorld
	if sw == nil {
		t.Fatal("no shifting_world result")
	}
	if !sw.Validity.Deterministic {
		t.Fatalf("noise arms disagreed: %v", sw.Validity.NondeterministicMetrics)
	}

	evidence := shiftArm(t, sw, ShiftArmEvidenceOnly)
	current := shiftArm(t, sw, ShiftArmCurrentLoop)
	revision := shiftArm(t, sw, ShiftArmRevision)

	// Evidence alone never retires the better-corroborated stale belief.
	if m := shiftMetric(t, evidence, MetricShiftCorrectionLag); m.Known {
		t.Errorf("evidence_only corrected (lag %v); the stale belief should outlive one contrary document", m.Value)
	}
	// Both prediction loops correct the changed belief once evidence exists.
	for _, a := range []ShiftArm{current, revision} {
		if m := shiftMetric(t, a, MetricShiftChangedCorrected); !m.Known || m.Value != 1 {
			t.Errorf("%s changed_corrected = %+v, want 1", a.Name, m)
		}
	}
	// The current loop archives every refuted belief, stable ones included;
	// revision keeps them current.
	if m := shiftMetric(t, current, MetricShiftArchived); m.Value == 0 {
		t.Error("current_loop archived nothing: the control is not the old loop")
	}
	if c, r := shiftMetric(t, current, MetricShiftStableDisturbed), shiftMetric(t, revision, MetricShiftStableDisturbed); r.Value >= c.Value {
		t.Errorf("stable_disturbed revision %v, current_loop %v; want revision lower", r.Value, c.Value)
	}
	// The co-rested belief is exonerated once the stale belief explains the
	// surprise; the outlier stays flagged (nothing explained it), and was
	// confirmed later without being revised again.
	byProbe := map[string]ShiftBeliefState{}
	for _, s := range revision.Stable {
		byProbe[s.Probe] = s
	}
	if s := byProbe["retries"]; !s.Current || s.Status != "active" {
		t.Errorf("retries ended %+v, want current and active (explained away)", s)
	}
	if s := byProbe["ledger"]; !s.Current || s.Status != "contested" {
		t.Errorf("ledger ended %+v, want current and flagged contested", s)
	}
	var superseded bool
	for _, r := range revision.Rounds {
		for _, rv := range r.Revisions {
			superseded = superseded || strings.HasPrefix(rv, "supersede:The payments API timeout is 30 seconds")
		}
	}
	if !superseded {
		t.Errorf("revision never superseded the stale timeout: %+v", revision.Rounds)
	}
	// r3 takes only a confirmed decision: recall plus confirmation revises nothing.
	if r3 := revision.Rounds[2]; len(r3.Revisions) != 0 {
		t.Errorf("round r3 revised %v; a confirmed decision must leave beliefs alone", r3.Revisions)
	}
	if sw.Hypothesis.Verdict == HypothesisUnknown {
		t.Errorf("hypothesis unknown: %v", sw.Hypothesis.Reasons)
	}
}

func TestJudgeShifting(t *testing.T) {
	lag := func(ctrl, trt float64) Comparison {
		return Compare(KnownMetric(MetricShiftCorrectionLag, LowerBetter, ctrl, 1, "rounds"),
			KnownMetric(MetricShiftCorrectionLag, LowerBetter, trt, 1, "rounds"))
	}
	rate := func(name string, dir Direction, ctrl, trt float64) Comparison {
		return Compare(KnownMetric(name, dir, ctrl, 10, "ratio"), KnownMetric(name, dir, trt, 10, "ratio"))
	}
	steady := []Comparison{
		rate(MetricShiftChangedAccuracy, HigherBetter, 0.5, 0.5),
		rate(MetricShiftStableAccuracy, HigherBetter, 1, 1),
		rate(MetricShiftStableDisturbed, LowerBetter, 0, 0),
	}
	tests := []struct {
		name          string
		deterministic bool
		cmp           []Comparison
		want          HypothesisVerdict
	}{
		{"faster and nothing worse", true, append([]Comparison{lag(2, 0)}, steady...), HypothesisSupported},
		{"a tie is not faster", true, append([]Comparison{lag(0, 0)}, steady...), HypothesisNotSupported},
		{"slower", true, append([]Comparison{lag(0, 1)}, steady...), HypothesisNotSupported},
		{"faster but loses a stable belief", true, []Comparison{
			lag(2, 0), rate(MetricShiftChangedAccuracy, HigherBetter, 0.5, 0.5),
			rate(MetricShiftStableAccuracy, HigherBetter, 1, 0.8), rate(MetricShiftStableDisturbed, LowerBetter, 0, 0),
		}, HypothesisNotSupported},
		{"lag unmeasurable", true, append([]Comparison{Compare(
			UnknownMetric(MetricShiftCorrectionLag, LowerBetter, "never settled"),
			KnownMetric(MetricShiftCorrectionLag, LowerBetter, 0, 1, "rounds"))}, steady...), HypothesisUnknown},
		{"nondeterministic", false, append([]Comparison{lag(2, 0)}, steady...), HypothesisUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := judgeShifting(&ShiftingResult{Validity: Validity{Deterministic: tt.deterministic}, VsCurrentLoop: tt.cmp})
			if got.Verdict != tt.want {
				t.Fatalf("verdict = %s (%v), want %s", got.Verdict, got.Reasons, tt.want)
			}
		})
	}
}

func TestShiftingWorld_Validation(t *testing.T) {
	base := func() Scenario {
		sc, err := ParseScenario([]byte(shiftingYAML))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		return sc
	}
	tests := []struct {
		name   string
		mutate func(*ShiftingWorld)
		want   string
	}{
		{"no rounds", func(w *ShiftingWorld) { w.Rounds = nil }, "at least one round"},
		{"duplicate round", func(w *ShiftingWorld) { w.Rounds[1].ID = "r1" }, "duplicated"},
		{"decision without rests_on", func(w *ShiftingWorld) { w.Rounds[0].Decisions[0].RestsOn = nil }, "recall and rests_on"},
		{"round doc reuses a corpus id", func(w *ShiftingWorld) { w.Rounds[1].Docs[0].ID = "t1" }, "unique id"},
		{"no probes", func(w *ShiftingWorld) { w.Probes = nil }, "needs probes"},
		{"unknown role", func(w *ShiftingWorld) { w.Probes[1].Role = "other" }, "role must be"},
		{"changed probe without evidence round", func(w *ShiftingWorld) { w.Probes[0].EvidenceIn = "r9" }, "evidence_in"},
		{"changed probe without stale state", func(w *ShiftingWorld) { w.Probes[0].MustNot = nil }, "must_not"},
		{"stable probe with evidence round", func(w *ShiftingWorld) { w.Probes[1].EvidenceIn = "r2" }, "evidence_in is for changed"},
		{"no changed probe", func(w *ShiftingWorld) { w.Probes = w.Probes[1:] }, "at least one changed probe"},
		{"negative recall limit", func(w *ShiftingWorld) { w.RecallLimit = -1 }, "recall_limit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sc := base()
			tt.mutate(sc.ShiftingWorld)
			err := sc.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

// The shipped scenario must keep exercising both halves of the claim: a
// changed belief with lagging evidence, and stable beliefs a careless
// revision would destroy.
func TestLoadScenarios_ShiftingWorldCoversTheExperiment(t *testing.T) {
	scenarios, err := LoadScenarios("../../data/eval/brainbench")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, sc := range scenarios {
		if sc.ID != "shifting_world" {
			continue
		}
		roles := map[string]int{}
		for _, p := range sc.ShiftingWorld.Probes {
			roles[p.Role]++
		}
		if roles[ShiftRoleChanged] < 2 || roles[ShiftRoleStable] < 2 {
			t.Fatalf("roles = %v, want at least two changed and two stable probes", roles)
		}
		return
	}
	t.Fatal("shifting_world scenario is not shipped")
}
