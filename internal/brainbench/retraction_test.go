package brainbench

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
)

// retractionYAML is an inline adversarial retraction scenario. Inline, not the
// shipped fixture, so these tests pin harness behaviour and do not move when
// the fixture is tuned.
const retractionYAML = `
id: unit_retraction
corpus:
  - id: advisory
    text: |
      The libfoo 2.3 release fixed the heap overflow CVE-2026-1234.
      Services running libfoo 2.3 no longer need the WAF rule for CVE-2026-1234.
      libfoo 2.3 was released on 3 March 2026.
  - id: blog
    text: |
      The libfoo 2.3 release fixed the heap overflow CVE-2026-1234.
  - id: newsletter
    text: |
      According to the vendor, CVE-2026-1234 is patched in libfoo 2.3.
  - id: checkout-review
    text: |
      The checkout service is no longer exposed to CVE-2026-1234 after its upgrade to libfoo 2.3.
  - id: release-notes
    text: |
      libfoo 2.3 was released on 3 March 2026.
  - id: inventory
    text: |
      The payments service pins libfoo 2.2.
retraction:
  episode: advisory
  reason: advisory withdrawn
  limit: 3
  queries:
    - is CVE-2026-1234 fixed in libfoo 2.3
    - when was libfoo 2.3 released
    - which libfoo version does the payments service pin
  gold:
    - {text: release fixed the heap overflow, role: original}
    - {text: no longer need the WAF rule, role: original}
    - {text: is patched in libfoo 2.3, role: copy}
    - {text: no longer exposed to CVE-2026-1234, role: derived}
    - {text: released on 3 March 2026, role: corroborated}
    - {text: pins libfoo 2.2, role: independent}
probes:
  - id: release-date
    query: when was libfoo 2.3 released
    expect: released on 3 March 2026
`

func retractionScenario(t *testing.T) Scenario {
	t.Helper()
	sc, err := ParseScenario([]byte(retractionYAML))
	if err != nil {
		t.Fatalf("parse inline retraction scenario: %v", err)
	}
	return sc
}

func itemFor(t *testing.T, rt *RetractionResult, text string) GoldOutcome {
	t.Helper()
	for _, it := range rt.Items {
		if it.Text == text {
			return it
		}
	}
	t.Fatalf("no gold outcome for %q", text)
	return GoldOutcome{}
}

func metricValue(t *testing.T, ms MetricSet, name string) float64 {
	t.Helper()
	m, ok := ms.Get(name)
	if !ok || !m.Known {
		t.Fatalf("metric %s: known=%v present=%v (%s)", name, m.Known, ok, m.Unknown)
	}
	return m.Value
}

func effectNamed(rt *RetractionResult, name string) (Comparison, bool) {
	for _, c := range rt.Effect {
		if c.Name == name {
			return c, true
		}
	}
	return Comparison{}, false
}

// TestRun_RetractionFollowsEvidenceLinks pins what ADR 0037 propagation does
// to each adversarial case, read from the stores the arms leave behind. The
// verbatim copy and the paraphrase surviving are the finding, not a bug in
// the harness: they are asserted so a change in how dependents are recognised
// shows up here as a deliberate edit.
func TestRun_RetractionFollowsEvidenceLinks(t *testing.T) {
	res, err := Run(context.Background(), retractionScenario(t), t.TempDir())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	rt := res.Retraction
	if rt == nil {
		t.Fatal("scenario declares a retraction but the result has no retraction track")
	}
	if !rt.Validity.Deterministic {
		t.Fatalf("retracted arms disagreed: %v", rt.Validity.NondeterministicMetrics)
	}

	cases := []struct {
		text string
		want ItemState
	}{
		// Folded with the blog's verbatim copy at ingest: two evidence links,
		// so the retraction keeps and rescores it.
		{"release fixed the heap overflow", ItemState{Surviving: true, Surfaced: true, Rescored: true}},
		// Only the advisory supported it: withdrawn.
		{"no longer need the WAF rule", ItemState{Withdrawn: true}},
		// Separate beliefs from separate episodes: not reached.
		{"is patched in libfoo 2.3", ItemState{Surviving: true, Surfaced: true}},
		{"no longer exposed to CVE-2026-1234", ItemState{Surviving: true, Surfaced: true}},
		// Real corroboration: kept and rescored.
		{"released on 3 March 2026", ItemState{Surviving: true, Surfaced: true, Rescored: true}},
		{"pins libfoo 2.2", ItemState{Surviving: true, Surfaced: true}},
	}
	for _, tc := range cases {
		it := itemFor(t, rt, tc.text)
		if it.Retracted != tc.want {
			t.Errorf("%q on the retracted arm = %+v, want %+v", tc.text, it.Retracted, tc.want)
		}
		if !it.Reference.Surviving {
			t.Errorf("%q must be held on the unretracted reference", tc.text)
		}
	}

	if got := metricValue(t, rt.Retracted, MetricUnsupportedSurviving); got != 0.75 {
		t.Errorf("unsupported_surviving = %v, want 0.75 (3 of 4 dependents still held)", got)
	}
	if got := metricValue(t, rt.Retracted, MetricSupportedSurviving); got != 1 {
		t.Errorf("supported_surviving = %v, want 1", got)
	}
	if got := metricValue(t, rt.Retracted, MetricImpactPrecision); got != 1 {
		t.Errorf("impact_precision = %v, want 1 (the one withdrawn belief is a dependent)", got)
	}
	if got := metricValue(t, rt.Retracted, MetricImpactRecall); got != 0.25 {
		t.Errorf("impact_recall = %v, want 0.25", got)
	}
	// The reference arm ran no retraction, so it has no report to score.
	if m, _ := rt.Reference.Get(MetricImpactPrecision); m.Known {
		t.Errorf("impact_precision on the reference arm must be unknown, got %v", m.Value)
	}
	if rt.Verdict != VerdictImproved {
		t.Errorf("verdict = %s, want %s", rt.Verdict, VerdictImproved)
	}
	// Ties with the chunk-removal baseline on survival: the hypothesis fails.
	if rt.Hypothesis.Verdict != HypothesisNotSupported {
		t.Errorf("hypothesis = %s (%v), want %s", rt.Hypothesis.Verdict, rt.Hypothesis.Reasons, HypothesisNotSupported)
	}
}

// destructiveRetract withdraws every belief linked to the event, including
// those with independent evidence elsewhere — the cascade ADR 0037's
// live-evidence rule exists to prevent.
func destructiveRetract(ctx context.Context, dsn, eventID, reason string) (retractionOutcome, error) {
	conn, err := store.Open(ctx, dsn)
	if err != nil {
		return retractionOutcome{}, err
	}
	defer func() { _ = conn.Close() }()
	claims, err := conn.Claims.ListByEventIDs(ctx, []string{eventID})
	if err != nil {
		return retractionOutcome{}, err
	}
	var out retractionOutcome
	for i := range claims {
		claims[i].Status = domain.ClaimStatusDeprecated
		out.Withdrawn = append(out.Withdrawn, claims[i].ID)
	}
	if err := conn.Claims.UpsertWithReasonAs(ctx, claims, reason, SeedActor); err != nil {
		return retractionOutcome{}, fmt.Errorf("deprecate: %w", err)
	}
	return out, nil
}

// TestRun_RetractionReportsRegressionWhenCorroboratedBeliefsAreDestroyed is the
// anti-gaming guarantee: a retraction that withdraws more scores BETTER on
// unsupported_surviving, so only supported_surviving can catch one that also
// destroys corroborated knowledge. It must be reported as a regression, gate
// -fail-on-regression, and fail the hypothesis.
func TestRun_RetractionReportsRegressionWhenCorroboratedBeliefsAreDestroyed(t *testing.T) {
	res, err := run(context.Background(), retractionScenario(t), t.TempDir(), destructiveRetract)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	rt := res.Retraction
	if c, ok := effectNamed(rt, MetricSupportedSurviving); !ok || c.Verdict != VerdictWorse {
		t.Fatalf("supported_surviving = %+v, want worse", c)
	}
	if c, _ := effectNamed(rt, MetricUnsupportedSurviving); c.Verdict != VerdictBetter {
		t.Errorf("unsupported_surviving = %s, want better (the cascade withdrew the false claim too)", c.Verdict)
	}
	if rt.Verdict != VerdictMixed && rt.Verdict != VerdictRegressed {
		t.Errorf("verdict = %s, want mixed or regressed", rt.Verdict)
	}
	if it := itemFor(t, rt, "released on 3 March 2026"); it.Retracted.Surviving {
		t.Error("the destructive retraction should have withdrawn the corroborated belief")
	}
	if rt.Hypothesis.Verdict != HypothesisNotSupported {
		t.Errorf("hypothesis = %s, want %s", rt.Hypothesis.Verdict, HypothesisNotSupported)
	}
	if !strings.Contains(strings.Join(rt.Hypothesis.Reasons, "\n"), "knowledge lost") {
		t.Errorf("hypothesis reasons do not name the lost knowledge: %v", rt.Hypothesis.Reasons)
	}

	report := BuildReport([]ScenarioResult{res})
	if !report.HasRegression() {
		t.Error("a retraction that destroyed corroborated beliefs must count as a regression")
	}
	var buf bytes.Buffer
	if err := report.WriteHuman(&buf); err != nil {
		t.Fatalf("write human: %v", err)
	}
	for _, want := range []string{"RETRACTION (ADR 0037)", "HYPOTHESIS: not-supported", "knowledge lost"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("human report lacks %q", want)
		}
	}
}

// TestRetractionBaseline_RemovesOnlyTheRetractedDocument pins the baseline's
// retraction: the document's chunks go, a verbatim copy elsewhere stays.
func TestRetractionBaseline_RemovesOnlyTheRetractedDocument(t *testing.T) {
	sc := retractionScenario(t)
	var events []domain.Event
	for _, d := range sc.Corpus {
		events = append(events, domain.Event{ID: deterministicEventID(sc.ID, d.ID), Content: d.Text})
	}
	ms, items, err := measureRetractionBaseline(context.Background(), sc.Retraction, events,
		deterministicEventID(sc.ID, "advisory"), false)
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}
	want := map[string]ItemState{
		"release fixed the heap overflow": {Surviving: true, Surfaced: true, Withdrawn: true},
		"no longer need the WAF rule":     {Withdrawn: true},
		"released on 3 March 2026":        {Surviving: true, Surfaced: true, Withdrawn: true},
		"pins libfoo 2.2":                 {Surviving: true, Surfaced: true},
	}
	for i, g := range sc.Retraction.Gold {
		if w, ok := want[g.Text]; ok && items[i] != w {
			t.Errorf("%q = %+v, want %+v", g.Text, items[i], w)
		}
	}
	// Three chunks withdrawn, two of them dependents.
	if got := metricValue(t, ms, MetricImpactPrecision); got != 2.0/3 {
		t.Errorf("baseline impact_precision = %v, want 2/3", got)
	}
	if m, _ := ms.Get(MetricImpactRescored); m.Known {
		t.Error("a baseline has no trust to rescore; impact_rescored must be unknown")
	}
	if _, _, err := measureRetractionBaseline(context.Background(), sc.Retraction, events, "ev_missing", false); err == nil {
		t.Error("a retracted event missing from the corpus must be an error, not an empty retraction")
	}
}

func TestImpactMetrics_NothingWithdrawnIsUnknownPrecision(t *testing.T) {
	sc := retractionScenario(t)
	ms := impactMetrics(sc.Retraction, nil)
	if ms[0].Name != MetricImpactPrecision || ms[0].Known {
		t.Errorf("precision over an empty withdrawal must be unknown, got %+v", ms[0])
	}
	if ms[1].Name != MetricImpactRecall || !ms[1].Known || ms[1].Value != 0 {
		t.Errorf("recall over an empty withdrawal must be a known 0, got %+v", ms[1])
	}
}

func TestJudgeHypothesis(t *testing.T) {
	arm := func(unsupported, supported float64) MetricSet {
		return MetricSet{Metrics: []Metric{
			KnownMetric(MetricUnsupportedSurviving, LowerBetter, unsupported, 4, "ratio"),
			KnownMetric(MetricSupportedSurviving, HigherBetter, supported, 2, "ratio"),
		}}
	}
	result := func(deterministic bool, mnemos, base MetricSet) *RetractionResult {
		return &RetractionResult{
			Validity:  Validity{Deterministic: deterministic},
			Retracted: mnemos,
			Baselines: []BaselineResult{{Name: BaselineBM25, Metrics: base}},
		}
	}
	cases := []struct {
		name string
		res  *RetractionResult
		want HypothesisVerdict
	}{
		{"fewer unsupported, same supported", result(true, arm(0.25, 1), arm(0.75, 1)), HypothesisSupported},
		{"tie on unsupported", result(true, arm(0.75, 1), arm(0.75, 1)), HypothesisNotSupported},
		{"fewer unsupported bought by deleting knowledge", result(true, arm(0, 0.5), arm(0.75, 1)), HypothesisNotSupported},
		{"nondeterministic", result(false, arm(0, 1), arm(0.75, 1)), HypothesisUnknown},
		{"unmeasured", result(true, MetricSet{}, arm(0.75, 1)), HypothesisUnknown},
	}
	for _, tc := range cases {
		if got := judgeHypothesis(tc.res).Verdict; got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestScenario_ValidateRetraction(t *testing.T) {
	cases := []struct {
		name    string
		edit    func(*Retraction)
		wantErr string
	}{
		{"valid", func(*Retraction) {}, ""},
		{"unknown episode", func(r *Retraction) { r.Episode = "nope" }, "not a document"},
		{"no queries", func(r *Retraction) { r.Queries = nil }, "at least one query"},
		{"unknown role", func(r *Retraction) { r.Gold[0].Role = "rumour" }, "not one of"},
		{"original not in the episode", func(r *Retraction) {
			r.Gold[0] = RetractionGold{Text: "pins libfoo 2.2", Role: RoleOriginal}
		}, "appear in the retracted document"},
		{"duplicate gold text", func(r *Retraction) { r.Gold[2].Text = "no longer need the WAF rule" }, "duplicated"},
		{"copy only in the episode", func(r *Retraction) {
			r.Gold[2] = RetractionGold{Text: "Services running libfoo", Role: RoleCopy}
		}, "not in the retracted one"},
		{"corroborated without a second source", func(r *Retraction) {
			r.Gold[4] = RetractionGold{Text: "Services running libfoo", Role: RoleCorroborated}
		}, "and in another one"},
		{"no survivors", func(r *Retraction) { r.Gold = r.Gold[:4] }, "at least one dependent"},
		{"no dependents", func(r *Retraction) { r.Gold = r.Gold[4:] }, "at least one dependent"},
		{"negative limit", func(r *Retraction) { r.Limit = -1 }, "limit"},
	}
	for _, tc := range cases {
		sc := retractionScenario(t)
		r := *sc.Retraction
		r.Gold = append([]RetractionGold(nil), r.Gold...)
		tc.edit(&r)
		sc.Retraction = &r
		err := sc.Validate()
		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("%s: unexpected error %v", tc.name, err)
		case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
			t.Errorf("%s: error %v, want one containing %q", tc.name, err, tc.wantErr)
		}
	}
}
