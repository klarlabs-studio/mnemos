package brainbench

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/store"
)

// changingWorldYAML is an inline ownership timeline with one change announced
// ahead of taking effect and one recorded after it did. Inline, like
// timelineYAML, so these tests pin harness behaviour rather than the fixture.
const changingWorldYAML = `
id: unit_changing_world
description: inline announced and late-recorded ownership changes
timeline:
  - phase: p1
    docs:
      - id: search-discovery
        text: The search service is owned by the Discovery team.
      - id: billing-revenue
        text: The billing service is owned by the Revenue team.
  - phase: p2
    docs:
      - id: search-platform
        valid_from: p3
        text: The search service is owned by the Platform team.
    changes:
      - supersede_by:
          old: search service is owned by the Discovery team
          new: search service is owned by the Platform team
  - phase: p3
    docs:
      - id: billing-ledger
        valid_from: p2
        text: The billing service is owned by the Ledger team.
    changes:
      - supersede_by:
          old: billing service is owned by the Revenue team
          new: billing service is owned by the Ledger team
probes:
  - id: now-search
    class: current
    query: who owns the search service
    expect: owned by the Platform team
    must_not: [owned by the Discovery team]
  - id: now-billing
    class: current
    query: who owns the billing service
    expect: owned by the Ledger team
    must_not: [owned by the Revenue team]
as_of_probes:
  - id: valid-p2-search
    class: valid_history
    valid_as_of: p2
    query: who owns the search service
    expect: owned by the Discovery team
    must_not: [owned by the Platform team]
  - id: known-p2-search
    class: known_history
    recorded_as_of: p2
    query: who owns the search service
    expect: owned by the Discovery team
    must_not: [owned by the Platform team]
  - id: valid-p2-billing
    class: valid_history
    valid_as_of: p2
    query: who owns the billing service
    expect: owned by the Ledger team
    must_not: [owned by the Revenue team]
  - id: known-p2-billing
    class: known_history
    recorded_as_of: p2
    query: who owns the billing service
    expect: owned by the Revenue team
    must_not: [owned by the Ledger team]
`

// TestRun_AnnouncedAndLateChangesSeparateTheTimeAxes pins the two shapes of
// valid time ≠ record time the ownership scenario rests on, end to end:
//
//   - search: recorded in p2, in force from p3. Valid in p2 and believed in p2
//     are both Discovery.
//   - billing: in force from p2, recorded in p3. Valid in p2 is Ledger
//     (today's knowledge); believed in p2 is Revenue.
//
// It also checks the seeding mechanics behind them: the announced document is
// stamped with p3's start, reserved before p3 began.
func TestRun_AnnouncedAndLateChangesSeparateTheTimeAxes(t *testing.T) {
	sc, err := ParseScenario([]byte(changingWorldYAML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	work := t.TempDir()
	res, err := Run(context.Background(), sc, work)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	p1, p2, p3 := res.Seed.Phases["p1"], res.Seed.Phases["p2"], res.Seed.Phases["p3"]
	if !p2.Start.After(p1.End) || !p3.Start.After(p2.End) {
		t.Fatalf("phases overlap: p1 %+v p2 %+v p3 %+v", p1, p2, p3)
	}

	conn, err := store.Open(context.Background(), "sqlite://"+filepath.Join(work, sc.ID, "seed.db"))
	if err != nil {
		t.Fatalf("open seed: %v", err)
	}
	defer func() { _ = conn.Close() }()
	events, err := conn.Events.ListAll(context.Background())
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	announced := deterministicEventID(sc.ID, "search-platform")
	for _, ev := range events {
		if ev.ID != announced {
			continue
		}
		if !ev.Timestamp.Equal(p3.Start) {
			t.Errorf("announced doc observed at %v, want p3's start %v", ev.Timestamp, p3.Start)
		}
		if ev.IngestedAt.After(p2.End) || ev.IngestedAt.Before(p2.Start) {
			t.Errorf("announced doc ingested at %v, outside p2 %+v", ev.IngestedAt, p2)
		}
	}

	for _, o := range res.Outcomes {
		if !o.Correct {
			t.Errorf("Mnemos answered %s wrongly: %+v", o.ID, o)
		}
	}
	for _, class := range []string{"current", "valid_history", "known_history"} {
		c, ok := comparisonNamed(res, MetricClassAccuracyPrefix+class)
		if !ok || !c.Control.Known || c.Control.N != 2 {
			t.Errorf("accuracy_%s: want a known metric over 2 probes, got %+v", class, c)
		}
	}

	if len(res.Cost.Systems) != 3 {
		t.Fatalf("cost systems = %d, want mnemos and both baselines", len(res.Cost.Systems))
	}
	for _, s := range res.Cost.Systems {
		if s.Queries != len(res.Questions) {
			t.Errorf("%s: latency over %d queries, want %d", s.System, s.Queries, len(res.Questions))
		}
		if s.StoredBytes <= 0 || len(s.Rows) == 0 {
			t.Errorf("%s: stored %d bytes, rows %v", s.System, s.StoredBytes, s.Rows)
		}
	}
	if m := res.Cost.Systems[0]; m.System != SystemMnemos || m.BuildMS <= 0 || m.Rows["events"] != 4 {
		t.Errorf("mnemos cost = %+v, want ingest time and 4 events", m)
	}
	if res.Cost.CorpusBytes <= 0 {
		t.Errorf("corpus bytes = %d", res.Cost.CorpusBytes)
	}
}

// TestPhaseStart_ReservationOverrunIsAnError: when the phases before an
// announced phase outrun its reservation, seeding must fail rather than start
// the phase before the previous one ended.
func TestPhaseStart_ReservationOverrunIsAnError(t *testing.T) {
	reserved := map[string]time.Time{"mar": time.Now().UTC().Add(-time.Second)}
	if _, err := phaseStart("mar", reserved); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("err = %v, want a reservation overrun", err)
	}
	at := time.Now().UTC().Add(20 * time.Millisecond)
	start, err := phaseStart("apr", map[string]time.Time{"apr": at})
	if err != nil || !start.Equal(at) {
		t.Fatalf("start = %v, %v; want the reserved %v", start, err, at)
	}
	if time.Now().Before(at) {
		t.Fatal("phaseStart returned before its reserved instant")
	}
}

// TestScoreClasses_StrictRulePerClass: each class is scored by the strict rule
// over its own probes, unclassified probes are not tallied, and a battery that
// declares no class emits no class metric at all.
func TestScoreClasses_StrictRulePerClass(t *testing.T) {
	qs := questionSet{
		probes: []question{
			{ID: "cur-ok", Class: "current", Limit: 10, Expect: "Commerce", MustNot: []string{"Payments owns"}},
			{ID: "cur-stale", Class: "current", Limit: 10, Expect: "Commerce", MustNot: []string{"Payments owns"}},
			{ID: "why", Class: "change_why", Limit: 10, Expect: "reorg"},
			{ID: "plain", Limit: 10, Expect: "anything"},
		},
		asOf: []question{
			{ID: "then", Class: "valid_history", Limit: 10, Expect: "Payments owns", AsOf: phaseInstant},
		},
	}
	r := fakeRetriever{
		"cur-ok":    {"Commerce owns checkout"},
		"cur-stale": {"Commerce owns checkout", "Payments owns checkout"},
		"why":       {"moved after the reorg"},
		"then":      {"Payments owns checkout"},
	}
	b, err := runBattery(context.Background(), r, qs)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]float64{
		MetricClassAccuracyPrefix + "current":       0.5,
		MetricClassAccuracyPrefix + "change_why":    1,
		MetricClassAccuracyPrefix + "valid_history": 1,
	}
	got := map[string]Metric{}
	for _, m := range b.metrics {
		if strings.HasPrefix(m.Name, MetricClassAccuracyPrefix) {
			got[m.Name] = m
		}
	}
	if len(got) != len(want) {
		t.Fatalf("class metrics = %v, want %v", got, want)
	}
	for name, v := range want {
		if m := got[name]; !m.Known || m.Value != v || m.Direction != HigherBetter {
			t.Errorf("%s = %+v, want %.2f higher-better", name, m, v)
		}
	}
	if len(b.outcomes) != 5 || len(b.latencies) != 5 {
		t.Fatalf("outcomes %d, latencies %d; want one per question", len(b.outcomes), len(b.latencies))
	}

	none, err := runBattery(context.Background(), r, questionSet{probes: []question{{ID: "plain", Limit: 10, Expect: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range none.metrics {
		if strings.HasPrefix(m.Name, MetricClassAccuracyPrefix) {
			t.Errorf("a battery with no classes emitted %s", m.Name)
		}
	}
}

func TestSystemCost_Latency(t *testing.T) {
	c := SystemCost{}.withLatency([]time.Duration{3 * time.Millisecond, time.Millisecond, 2 * time.Millisecond})
	if c.Queries != 3 || c.QueryMeanMS != 2 || c.QueryP50MS != 2 || c.QueryMaxMS != 3 {
		t.Fatalf("latency = %+v, want mean 2, p50 2, max 3 over 3 queries", c)
	}
	if empty := (SystemCost{}).withLatency(nil); empty.Queries != 0 || empty.QueryMaxMS != 0 {
		t.Fatalf("empty latency = %+v", empty)
	}
}

// TestLoadScenarios_OwnershipTimelineCoversTheExperiment guards the shipped
// changing-world fixture's design: every probe class, both time axes, both
// directions of valid time ≠ record time, a correction and a stale
// restatement. A fixture edit that dropped one would leave the report
// silently measuring less than it claims.
func TestLoadScenarios_OwnershipTimelineCoversTheExperiment(t *testing.T) {
	scenarios, err := LoadScenarios("../../data/eval/brainbench")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	var sc Scenario
	for _, s := range scenarios {
		if s.ID == "ownership_timeline" {
			sc = s
		}
	}
	if sc.ID == "" {
		t.Fatal("ownership_timeline is not shipped")
	}
	classes := map[string]int{}
	for _, p := range sc.Probes {
		classes[p.Class]++
	}
	var recorded, valid int
	for _, p := range sc.AsOfProbes {
		classes[p.Class]++
		if p.RecordedAsOf != "" {
			recorded++
		} else {
			valid++
		}
	}
	for _, c := range []string{"current", "valid_history", "known_history", "change_why"} {
		if classes[c] == 0 {
			t.Errorf("no %s probe", c)
		}
	}
	if recorded == 0 || valid == 0 {
		t.Errorf("as-of axes: %d recorded, %d valid; want both", recorded, valid)
	}

	order := map[string]int{}
	for i, ph := range sc.Timeline {
		order[ph.Name] = i
	}
	var ahead, late, retract, supersedeBy int
	texts := map[string]int{}
	for i, ph := range sc.Timeline {
		for _, d := range ph.Docs {
			texts[strings.TrimSpace(d.Text)]++
			if d.ValidFrom == "" {
				continue
			}
			if order[d.ValidFrom] > i {
				ahead++
			} else if order[d.ValidFrom] < i {
				late++
			}
		}
		for _, c := range ph.Changes {
			if c.Retract != "" {
				retract++
			}
			if c.SupersedeBy != nil {
				supersedeBy++
			}
		}
	}
	var restated int
	for _, n := range texts {
		if n > 1 {
			restated++
		}
	}
	if ahead == 0 || late == 0 || retract == 0 || supersedeBy == 0 || restated == 0 {
		t.Errorf("announced=%d late=%d retract=%d supersede_by=%d verbatim-restated=%d; want each > 0",
			ahead, late, retract, supersedeBy, restated)
	}
}
