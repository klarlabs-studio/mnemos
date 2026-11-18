package brainbench

import (
	"context"
	"strings"
	"testing"
)

// timelineYAML is an inline incident timeline. Kept inline rather than reusing
// the shipped fixture so these tests pin harness behaviour and do not move
// when the fixture's corpus is tuned.
const timelineYAML = `
id: unit_timeline
description: inline incident timeline
timeline:
  - phase: monday
    docs:
      - id: deploy
        text: The v2 deploy changed the payments connection pool settings.
      - id: pool-50
        text: The payments connection pool size is 50.
  - phase: tuesday
    docs:
      - id: diagnosis
        text: The payments outage was caused by database disk saturation.
  - phase: thursday
    docs:
      - id: root-cause
        valid_from: monday
        text: The payments outage was caused by the v2 connection pool change starving the payments database.
      - id: pool-200
        text: The payments connection pool size is 200.
    changes:
      - retract: database disk saturation
      - supersede: pool size is 50
probes:
  - id: now-cause
    query: what caused the payments outage
    expect: starving the payments database
    must_not: [disk saturation]
as_of_probes:
  - id: believed-tue-cause
    recorded_as_of: tuesday
    query: what caused the payments outage
    expect: disk saturation
    must_not: [starving]
  - id: believed-tue-pool
    recorded_as_of: tuesday
    query: what is the payments connection pool size
    expect: pool size is 50
    must_not: [pool size is 200]
  - id: known-tue-cause
    valid_as_of: tuesday
    query: what caused the payments outage
    expect: starving the payments database
    must_not: [disk saturation]
  - id: known-tue-pool
    valid_as_of: tuesday
    query: what is the payments connection pool size
    expect: pool size is 50
    must_not: [pool size is 200]
`

func timelineScenario(t *testing.T, process Process) Scenario {
	t.Helper()
	sc, err := ParseScenario([]byte(timelineYAML))
	if err != nil {
		t.Fatalf("parse inline timeline: %v", err)
	}
	sc.Process = process
	return sc
}

// TestRun_TimelinePhasesAreOrderedOnTheWriteClock pins the premise of the
// whole track: each phase boundary sits strictly after the previous one, so a
// recorded-as-of read at a boundary separates the phases' writes.
func TestRun_TimelinePhasesAreOrderedOnTheWriteClock(t *testing.T) {
	res, err := Run(context.Background(), timelineScenario(t, Process{}), t.TempDir())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	mon, tue, thu := res.Seed.Phases["monday"], res.Seed.Phases["tuesday"], res.Seed.Phases["thursday"]
	for _, w := range []PhaseWindow{mon, tue, thu} {
		if w.Start.IsZero() || !w.End.After(w.Start) {
			t.Fatalf("phase window %+v is not a forward span", w)
		}
	}
	if !tue.Start.After(mon.End) || !thu.Start.After(tue.End) {
		t.Fatalf("phases overlap: monday %+v tuesday %+v thursday %+v", mon, tue, thu)
	}
	if res.Seed.Changes != 2 {
		t.Fatalf("changes applied = %d, want 2", res.Seed.Changes)
	}
	if !res.Validity.Deterministic {
		t.Fatalf("timeline measurement not deterministic: %v", res.Validity.NondeterministicMetrics)
	}
	for _, name := range []string{MetricAsOfRecorded, MetricAsOfValid} {
		c, ok := comparisonNamed(res, name)
		if !ok || !c.Control.Known || c.Control.N != 2 {
			t.Fatalf("%s: want a known metric over 2 probes on the control arm, got %+v", name, c)
		}
	}
}

// TestRun_ReportsRegressionWhenAsOfAnswersBreak mirrors
// TestRun_ReportsRegressionWhenKnowledgeIsDestroyed for the temporal track.
//
// Dedupe at a threshold the stub embedder clears for unrelated sentences merges
// distinct timeline beliefs into one another, and the merged-away belief is
// deprecated. At the time of writing that breaks "what is known now about
// Tuesday" while the recorded-as-of read survives, because the loser's status
// change is rewound. The
// test does not pin which axis breaks — ADR 0036 step 2 may change it — only
// that a broken reconstruction is reported as a regression, not as a neutral
// or improved run.
func TestRun_ReportsRegressionWhenAsOfAnswersBreak(t *testing.T) {
	res, err := Run(context.Background(), timelineScenario(t, Process{DedupeThreshold: 0.05}), t.TempDir())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Validity.Deterministic {
		t.Fatalf("measurement not deterministic: %v", res.Validity.NondeterministicMetrics)
	}
	if res.Activity.Merged == 0 {
		t.Fatal("precondition failed: the destructive dedupe merged nothing")
	}
	var worse []string
	for _, name := range []string{MetricAsOfRecorded, MetricAsOfValid} {
		c, ok := comparisonNamed(res, name)
		if !ok {
			t.Fatalf("%s must be measured on a timeline scenario", name)
		}
		t.Logf("%s: control %.3f treatment %.3f (%s), merged %d",
			name, c.Control.Value, c.Treatment.Value, c.Verdict, res.Activity.Merged)
		if c.Verdict == VerdictWorse {
			worse = append(worse, name)
		}
		if c.Verdict == VerdictBetter {
			t.Errorf("%s improved under a merge-everything dedupe (control %.3f, treatment %.3f)",
				name, c.Control.Value, c.Treatment.Value)
		}
	}
	if len(worse) == 0 {
		t.Fatalf("no as-of metric regressed; a process that merges the timeline's beliefs " +
			"MUST be reported as breaking the reconstruction")
	}
	if res.Summary.Worse == 0 || res.Verdict == VerdictImproved {
		t.Fatalf("verdict %q with %d worse: the harness is reporting only good news", res.Verdict, res.Summary.Worse)
	}
}

// TestScoreAsOf_WrongReconstructionScoresAsWorse checks the scoring rule on its
// own: a time-travel answer that contains the expected belief AND one the
// brain did not hold then is wrong, and a drop in accuracy compares as worse.
func TestScoreAsOf_WrongReconstructionScoresAsWorse(t *testing.T) {
	qs := []question{
		{ID: "a", Text: "q", Limit: 10, Expect: "disk", MustNot: []string{"starving"}, RecordedAsOf: phaseInstant},
		{ID: "b", Text: "q", Limit: 10, Expect: "starving", MustNot: []string{"disk"}, AsOf: phaseInstant},
	}
	right := fakeRetriever{"a": {"disk saturation"}, "b": {"pool change starving the database"}}
	wrong := fakeRetriever{"a": {"disk saturation", "pool change starving the database"}, "b": {"disk saturation"}}

	var asked []string
	good, err := scoreAsOf(context.Background(), right, qs, &asked)
	if err != nil {
		t.Fatal(err)
	}
	bad, err := scoreAsOf(context.Background(), wrong, qs, &asked)
	if err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{MetricAsOfRecorded, MetricAsOfValid} {
		if good[i].Name != name || good[i].Value != 1 || bad[i].Value != 0 {
			t.Fatalf("%s: right=%+v wrong=%+v", name, good[i], bad[i])
		}
		if c := Compare(good[i], bad[i]); c.Verdict != VerdictWorse {
			t.Fatalf("%s: a wrong reconstruction compares as %q, want %q", name, c.Verdict, VerdictWorse)
		}
	}
}

// TestScoreAsOf_UnaskedAxisIsUnknown: a scenario that asks no question on an
// axis must not score 0 on it, which would read as "every reconstruction was
// wrong".
func TestScoreAsOf_UnaskedAxisIsUnknown(t *testing.T) {
	var asked []string
	ms, err := scoreAsOf(context.Background(), fakeRetriever{}, nil, &asked)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.Known || !strings.Contains(m.Unknown, "nothing was measured") {
			t.Errorf("%s = %+v, want unknown", m.Name, m)
		}
	}
}

func TestScenario_ValidateTimeline(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Scenario)
		want string
	}{
		{"corpus and timeline", func(s *Scenario) { s.Corpus = []Doc{{ID: "x", Text: "t"}} }, "not both"},
		{"valid_from an unknown phase", func(s *Scenario) { s.Timeline[0].Docs[0].ValidFrom = "friday" }, "valid_from"},
		{"supersede_by without new", func(s *Scenario) {
			s.Timeline[2].Changes[0] = Change{SupersedeBy: &SupersedeEdge{Old: "pool size is 50"}}
		}, "supersede_by needs old and new"},
		{"supersede and supersede_by", func(s *Scenario) {
			s.Timeline[2].Changes[1].SupersedeBy = &SupersedeEdge{Old: "a", New: "b"}
		}, "exactly one of retract"},
		{"class not an identifier", func(s *Scenario) { s.Probes[0].Class = "Current Truth" }, "class"},
		{"as-of class not an identifier", func(s *Scenario) { s.AsOfProbes[0].Class = "9lives" }, "class"},
		{"unknown as-of phase", func(s *Scenario) { s.AsOfProbes[0].RecordedAsOf = "friday" }, "unknown phase"},
		{"both axes", func(s *Scenario) { s.AsOfProbes[0].ValidAsOf = "monday" }, "exactly one of recorded_as_of"},
		{"two ops in one change", func(s *Scenario) { s.Timeline[2].Changes[0].Archive = "x" }, "exactly one of retract"},
		{"duplicate probe id", func(s *Scenario) { s.AsOfProbes[1].ID = s.Probes[0].ID }, "duplicated"},
		{"as-of without timeline", func(s *Scenario) {
			s.Timeline = nil
			s.Corpus = []Doc{{ID: "x", Text: "t"}}
		}, "need a timeline"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sc := timelineScenario(t, Process{})
			tc.edit(&sc)
			err := sc.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}
