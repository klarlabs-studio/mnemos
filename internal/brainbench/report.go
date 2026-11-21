package brainbench

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// ReportVersion is the schema version of the machine-readable output. Bump it
// on any breaking change so a consumer diffing two reports can tell whether the
// numbers or the format moved.
//
// 2: timeline scenarios (seed.phases, as_of_* metrics), raw-retrieval baselines
// (scenarios[].baselines, baseline_summary) and per-scenario question lists.
// 3: the evidence-retraction track (scenarios[].retraction,
// retraction_summary, retraction_baseline_summary, retraction_hypotheses).
// 4: per-class accuracy metrics (accuracy_<class>) and the per-system cost
// report (scenarios[].cost).
// 5: the credibility-order track (scenarios[].credibility,
// credibility_summary).
const ReportVersion = 5

// Report is the harness's complete machine-readable output.
type Report struct {
	Harness     string           `json:"harness"`
	Version     int              `json:"version"`
	GeneratedAt time.Time        `json:"generated_at"`
	Config      Config           `json:"config"`
	Limitations []string         `json:"limitations"`
	Scenarios   []ScenarioResult `json:"scenarios"`
	Summary     Summary          `json:"summary"`
	// Verdicts counts scenario verdicts by kind.
	Verdicts map[RunVerdict]int `json:"verdicts"`
	// BaselineSummary totals, per baseline, how the untreated Mnemos arm
	// compared with it across scenarios. It never feeds Summary or
	// HasRegression: a baseline win is a finding about Mnemos's semantics,
	// not a regression caused by a process.
	BaselineSummary map[string]BaselineSummary `json:"baseline_summary"`

	// RetractionSummary totals the retraction tracks' effect comparisons
	// (retracted arm vs unretracted reference). A retraction that loses a
	// supported belief is a regression caused by the operation under test, so
	// it counts in HasRegression.
	RetractionSummary Summary `json:"retraction_summary"`
	// RetractionBaselineSummary totals, per baseline, how the retracted arm
	// compared with raw retrieval whose retraction removed chunks only.
	RetractionBaselineSummary map[string]BaselineSummary `json:"retraction_baseline_summary"`
	// RetractionHypotheses counts the retraction tracks' hypothesis verdicts.
	RetractionHypotheses map[HypothesisVerdict]int `json:"retraction_hypotheses"`

	// ShiftingSummary totals the shifting-world tracks' revision-vs-current-
	// loop comparisons. A stable belief lost or a correction slowed is a
	// regression caused by the mechanism under test, so it counts in
	// HasRegression.
	ShiftingSummary Summary `json:"shifting_summary"`
	// ShiftingHypotheses counts the shifting-world hypothesis verdicts.
	ShiftingHypotheses map[HypothesisVerdict]int `json:"shifting_hypotheses"`

	// CredibilitySummary totals the credibility-order tracks' pairs. A pair
	// whose gold-true belief does not out-trust the gold-false one means the
	// trust model rewarded something other than independent evidence and
	// consequences, so a violated pair counts in HasRegression.
	CredibilitySummary CredibilitySummary `json:"credibility_summary"`
}

// CredibilitySummary counts credibility-order pairs across scenarios.
type CredibilitySummary struct {
	Held     int `json:"held"`
	Violated int `json:"violated"`
}

// Config records the conditions the numbers were produced under. It travels
// inside the report because a metric without its conditions is not
// reproducible, and because these particular conditions (no LLM, a stub
// embedder) bound what the numbers can be used to argue.
type Config struct {
	Extraction string `json:"extraction"`
	Embedder   string `json:"embedder"`
	LLM        string `json:"llm"`
	Backend    string `json:"backend"`
	Design     string `json:"design"`
	Clock      string `json:"clock"`
}

// DefaultConfig describes the conditions Run operates under.
func DefaultConfig() Config {
	return Config{
		Extraction: "rule-based (internal/extract), no LLM",
		Embedder:   StubEmbedderModel + " (deterministic hashed bag-of-words, lexical only)",
		LLM:        "none",
		Backend:    "sqlite",
		Design: "paired A/B on byte-identical file copies of one seed; " +
			"control untreated, treatment consolidated, third untreated arm measures the noise floor; " +
			"raw-retrieval baselines over the seed's event texts are scored on the same questions " +
			"and compared with the control arm, outside the A/B verdict",
		Clock: "simulated (internal/clock): every scenario starts at " + simEpoch.Format(time.RFC3339) +
			" and each clock read advances it " + simTick.String() + ", so stamps and trust freshness are " +
			"identical across runs; corpus scenarios back-date by age_days from that anchor; " +
			"timeline phases are clock advances milliseconds apart",
	}
}

// Limitations is what this harness does NOT measure.
//
// It ships inside every report rather than living only in a README, because a
// JSON file gets pasted into an issue, a slide, or a model prompt without its
// documentation, and an unqualified "consolidation improved MRR by 0.17" is a
// materially misleading claim. The caveats have to travel with the numbers.
func Limitations() []string {
	return []string{
		"LLM path untested: extraction is rule-based and no LLM is configured. " +
			"Nothing here says anything about LLM extraction, LLM causal relation detection, " +
			"grounded generation, or the narration-clearing stage of `consolidate --clear-session-noise`.",
		"Embeddings are a deterministic hashed bag-of-words stub, not a semantic model. " +
			"It scores exact and near-lexical restatements high and true paraphrases near zero, " +
			"so reported dedupe/merge behaviour is a LOWER BOUND on production and retrieval " +
			"quality is not comparable to a real embedding provider.",
		"Scale untested: scenarios hold tens of claims. Consolidation behaviour on a " +
			"10k-claim brain (clustering cost, merge cascades, forgetting mass) is not exercised, " +
			"and the incremental-trust-scoring failure mode documented in CLAUDE.md needs volume to appear.",
		"Single cycle only: one consolidation pass is measured. Repeated nightly passes may " +
			"compound, oscillate, or converge; that is a different experiment and this is not it.",
		"Cross-tenant promotion (ADR 0011 hippocampus->neocortex, `consolidate --promote`) is not " +
			"measured at all. This harness covers in-store maintenance only.",
		"Downstream utility is not measured. Metrics are properties of the store and of retrieval; " +
			"whether an agent using this brain made a better decision is unmeasured and would need " +
			"a task benchmark, not a store diff.",
		"Gold labels are author-written, not independently adjudicated, and there are few of them. " +
			"A scenario can be unintentionally written toward the behaviour it wants to show; " +
			"treat any single scenario as an anecdote and read the noise floor before the delta.",
		"Statistical significance is not computed and would be meaningless here: the run is " +
			"deterministic, so a delta is exact for this scenario and carries no confidence interval " +
			"across scenarios. The reported noise floor is the empirical substitute.",
		"Calibration, credit and salience stages need decisions, expectations and outcomes to act on. " +
			"A scenario whose corpus contains none leaves those stages inert, and the report will " +
			"show that as zero activity rather than as a neutral result.",
		"The raw-retrieval baselines see the documents only. Curation a timeline applies as " +
			"structured operations (retract, archive, supersede, contradict) has no textual trace in " +
			"their corpus; a real log would carry a retraction note they could retrieve. Mnemos's " +
			"advantage on forbidden-hit and valid-as-of questions is therefore partly an advantage of " +
			"having that curation at all, not only of using it well.",
		"The baselines are lexical (BM25, sentence chunks, no stemming or stop words) and Mnemos " +
			"recall here runs on the lexical stub embedder, so 'Mnemos vs raw retrieval' compares " +
			"two lexical retrievers plus Mnemos's structure. It says nothing about dense-retrieval RAG.",
		"Baselines are compared with the untreated arm only. They are not consolidated, so comparing " +
			"them with the treatment arm would mix the process effect into the baseline effect.",
		"Evidence retraction is judged on author-declared gold roles (original, copy, derived, " +
			"corroborated, independent). Mnemos recognises a dependent only through evidence links: a " +
			"verbatim restatement in another document is folded onto the original belief at ingest and " +
			"counts as independent evidence, and a paraphrase or a conclusion drawn in another document is " +
			"a separate belief the retraction does not reach. The track measures that gap; it does not " +
			"model provenance between documents, and no scenario is special-cased to close it.",
		"Retraction impact precision and recall count beliefs for Mnemos and sentence chunks for the " +
			"baselines. A baseline 'withdraws' the retracted document's copy of a sentence even when " +
			"another document still holds it; Mnemos keeps one belief and rescores it. Survival, not the " +
			"impact report, is what says whether an unsupported statement is still held.",
		"Cost figures are single-run wall times on tens of documents. Mnemos's stored bytes are a " +
			"SQLite file dominated by schema and history tables, the baselines' an in-memory index; " +
			"embedding is the free stub, so a real provider's ingest and query cost is absent; and " +
			"nothing here says how either side's cost grows with corpus size.",
		"Timeline phases are milliseconds apart on the simulated clock, not days. Valid-time freshness " +
			"and decay see no age difference between phases " +
			"(TestRun_TimelinePhasesAreOrderedOnTheWriteClock guards the order). " +
			"Trust is current on a recorded-as-of read (ADR 0036 §3) and merges are not rewound.",
		"The shifting-world track (ADR 0049) scripts the agent: its decisions, predictions and the " +
			"world's outcomes are fixed in the scenario, and only whether a decision is taken depends on " +
			"the arm (its premises must come back from recall). It measures what each prediction loop " +
			"does to the brain, not whether a real agent would predict or decide well. The current-loop " +
			"control records one expectation on every rested-on belief, the fastest use of the old API; " +
			"no arm can beat it on speed, only on what it destroys getting there.",
	}
}

// BuildReport assembles the full report from scenario results.
func BuildReport(results []ScenarioResult) Report {
	r := Report{
		Harness:     "brainbench",
		Version:     ReportVersion,
		GeneratedAt: time.Now().UTC(),
		Config:      DefaultConfig(),
		Limitations: Limitations(),
		Scenarios:   results,
		Verdicts:    map[RunVerdict]int{},

		BaselineSummary: map[string]BaselineSummary{},

		RetractionBaselineSummary: map[string]BaselineSummary{},
		RetractionHypotheses:      map[HypothesisVerdict]int{},
		ShiftingHypotheses:        map[HypothesisVerdict]int{},
	}
	for _, s := range results {
		if sw := s.ShiftingWorld; sw != nil {
			r.ShiftingSummary.add(sw.Summary)
			r.ShiftingHypotheses[sw.Hypothesis.Verdict]++
		}
		if cr := s.Credibility; cr != nil {
			for _, p := range cr.Pairs {
				if p.Holds {
					r.CredibilitySummary.Held++
				} else {
					r.CredibilitySummary.Violated++
				}
			}
		}
		if rt := s.Retraction; rt != nil {
			r.RetractionSummary.add(rt.Summary)
			r.RetractionHypotheses[rt.Hypothesis.Verdict]++
			for _, b := range rt.Baselines {
				sum := r.RetractionBaselineSummary[b.Name]
				sum.add(b.Summary)
				r.RetractionBaselineSummary[b.Name] = sum
			}
		}
		for _, b := range s.Baselines {
			sum := r.BaselineSummary[b.Name]
			sum.add(b.Summary)
			r.BaselineSummary[b.Name] = sum
		}
		r.Summary.add(s.Summary)
		r.Verdicts[s.Verdict]++
	}
	return r
}

// add accumulates another summary into s.
func (s *Summary) add(o Summary) {
	s.Better += o.Better
	s.Worse += o.Worse
	s.Unchanged += o.Unchanged
	s.Changed += o.Changed
	s.Unknown += o.Unknown
}

// HasRegression reports whether any scenario degraded a scored metric, in the
// process track or the retraction track, or violated a credibility order. Callers
// that want the harness to gate CI use this; it is not the default, because the
// harness's job is to report honestly, not to block on a number nobody has
// baselined yet.
func (r Report) HasRegression() bool {
	return r.Summary.Worse > 0 || r.RetractionSummary.Worse > 0 || r.ShiftingSummary.Worse > 0 || r.CredibilitySummary.Violated > 0
}

// WriteJSON emits the machine-readable report.
func (r Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WriteHuman renders a readable report.
//
// It leads with activity and the noise floor, and only then shows deltas —
// the same order the reader needs to judge them in. Unchanged metrics are
// listed rather than hidden: "we measured this and it did not move" is a
// finding, and suppressing it would leave a reader assuming the harness simply
// did not look.
func (r Report) WriteHuman(w io.Writer) error {
	// Write errors are latched rather than checked per call: the report is ~40
	// Fprintf calls and threading an error through each would drown the layout.
	// A broken stdout surfaces once, at the end.
	var werr error
	p := func(format string, args ...any) {
		if werr != nil {
			return
		}
		_, werr = fmt.Fprintf(w, format, args...)
	}
	p("brainbench — do the cognitive processes improve what the brain knows?\n")
	p("generated %s\n\n", r.GeneratedAt.Format(time.RFC3339))
	p("conditions: %s | embedder %s | backend %s\n", r.Config.Extraction, r.Config.Embedder, r.Config.Backend)
	p("design:     %s\n\n", r.Config.Design)

	for _, s := range r.Scenarios {
		p("──────────────────────────────────────────────────────────────────────\n")
		p("SCENARIO %s — %s\n", s.ID, s.Description)
		p("  stages under test: %s\n", strings.Join(s.Stages, ", "))
		p("  seed: %d events, %d claims (%d collapsed by ingest dedupe), %d edges, %d vectors\n",
			s.Seed.Events, s.Seed.Claims, s.Seed.DedupedAtIngest, s.Seed.Relationships, s.Seed.Embeddings)
		if len(s.Seed.Phases) > 0 {
			p("  timeline: %d phases on the real write clock, %d curation changes, %d as-of probes\n",
				len(s.Seed.Phases), s.Seed.Changes, s.Validity.AsOfProbeCount)
		}

		p("\n  ACTIVITY (what the process reported doing — NOT evidence of benefit)\n")
		p("    scanned %d | duplicate groups %d | merged %d | forgotten %d | refuted %d\n",
			s.Activity.ClaimsScanned, s.Activity.DuplicateGroups, s.Activity.Merged,
			s.Activity.Forgotten, s.Activity.Refuted)
		p("    validated %d | credited %d | salience %d | replayed %d (protected %d)\n",
			s.Activity.Validated, s.Activity.Credited, s.Activity.SalienceTagged,
			s.Activity.Replayed, s.Activity.ReplayProtected)
		p("    associations decayed %d | inhibition decayed %d | lessons %d | playbooks %d\n",
			s.Activity.AssociationsDecayed, s.Activity.InhibitionDecayed,
			s.Activity.LessonsSynthesized, s.Activity.PlaybooksSynthesized)
		p("    total mutations: %d\n", s.Activity.Total)

		p("\n  VALIDITY\n")
		if s.Validity.Deterministic {
			p("    measurement deterministic: yes (two untreated arms agreed on every metric)\n")
		} else {
			p("    measurement deterministic: NO — %s\n", strings.Join(s.Validity.NondeterministicMetrics, ", "))
			p("    deltas below this noise floor cannot be attributed to the process\n")
		}
		p("    probes: %d — smallest expressible rate change is %.4f\n",
			s.Validity.ProbeCount, s.Validity.MinResolvableRateDelta)

		p("\n  EFFECT (treatment minus control)\n")
		p("    %-34s %10s %10s %10s  %s\n", "metric", "control", "treatment", "delta", "verdict")
		for _, c := range s.Comparisons {
			ctrl := fmtMetric(c.Control)
			trt := fmtMetric(c.Treatment)
			delta := "—"
			if c.Verdict != VerdictUnknown {
				delta = fmt.Sprintf("%+.4f", c.Delta)
			}
			p("    %-34s %10s %10s %10s  %s%s\n",
				c.Name, ctrl, trt, delta, verdictMark(c.Verdict), verdictLabel(c))
			if c.Note != "" {
				p("    %-34s %s\n", "", "^ "+c.Note)
			}
		}
		p("\n  SUMMARY: %d better, %d worse, %d unchanged, %d changed-but-unscored, %d unknown\n",
			s.Summary.Better, s.Summary.Worse, s.Summary.Unchanged, s.Summary.Changed, s.Summary.Unknown)
		p("  VERDICT: %s — %s\n\n", s.Verdict, verdictExplanation(s.Verdict))
		writeBaselines(p, s)
		if s.Retraction != nil {
			writeRetraction(p, s.Retraction)
		}
		if s.ShiftingWorld != nil {
			writeShifting(p, s.ShiftingWorld)
		}
		if s.Credibility != nil {
			writeCredibility(p, s.Credibility)
		}
	}

	p("──────────────────────────────────────────────────────────────────────\n")
	p("TOTAL: %d better, %d worse, %d unchanged, %d changed-but-unscored, %d unknown\n",
		r.Summary.Better, r.Summary.Worse, r.Summary.Unchanged, r.Summary.Changed, r.Summary.Unknown)
	verdicts := make([]string, 0, len(r.Verdicts))
	for v, n := range r.Verdicts {
		verdicts = append(verdicts, fmt.Sprintf("%s=%d", v, n))
	}
	sort.Strings(verdicts)
	p("VERDICTS: %s\n", strings.Join(verdicts, " "))
	names := make([]string, 0, len(r.BaselineSummary))
	for n := range r.BaselineSummary {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		b := r.BaselineSummary[n]
		p("MNEMOS vs %s: %d mnemos-better, %d baseline-better, %d tie, %d unknown\n",
			n, b.MnemosBetter, b.BaselineBetter, b.Tie, b.Unknown)
	}
	if len(r.RetractionHypotheses) > 0 {
		s := r.RetractionSummary
		p("RETRACTION: %d better, %d worse, %d unchanged, %d changed-but-unscored, %d unknown\n",
			s.Better, s.Worse, s.Unchanged, s.Changed, s.Unknown)
		hyp := make([]string, 0, len(r.RetractionHypotheses))
		for v, n := range r.RetractionHypotheses {
			hyp = append(hyp, fmt.Sprintf("%s=%d", v, n))
		}
		sort.Strings(hyp)
		p("RETRACTION HYPOTHESIS: %s\n", strings.Join(hyp, " "))
		rnames := make([]string, 0, len(r.RetractionBaselineSummary))
		for n := range r.RetractionBaselineSummary {
			rnames = append(rnames, n)
		}
		sort.Strings(rnames)
		for _, n := range rnames {
			b := r.RetractionBaselineSummary[n]
			p("RETRACTED MNEMOS vs %s: %d mnemos-better, %d baseline-better, %d tie, %d unknown\n",
				n, b.MnemosBetter, b.BaselineBetter, b.Tie, b.Unknown)
		}
	}
	if len(r.ShiftingHypotheses) > 0 {
		s := r.ShiftingSummary
		p("SHIFTING WORLD (revision vs current loop): %d better, %d worse, %d unchanged, %d changed-but-unscored, %d unknown\n",
			s.Better, s.Worse, s.Unchanged, s.Changed, s.Unknown)
		hyp := make([]string, 0, len(r.ShiftingHypotheses))
		for v, n := range r.ShiftingHypotheses {
			hyp = append(hyp, fmt.Sprintf("%s=%d", v, n))
		}
		sort.Strings(hyp)
		p("SHIFTING WORLD HYPOTHESIS: %s\n", strings.Join(hyp, " "))
	}
	if c := r.CredibilitySummary; c.Held+c.Violated > 0 {
		p("CREDIBILITY ORDER: %d held, %d violated\n", c.Held, c.Violated)
	}
	p("\n")

	p("WHAT THIS HARNESS DOES NOT MEASURE\n")
	for _, l := range r.Limitations {
		p("  - %s\n", wrapIndent(l, 76, "    "))
	}
	return werr
}

// writeBaselines renders the Mnemos-vs-raw-retrieval table for one scenario:
// one row per metric, one column per answerer, so a baseline win is as visible
// as a Mnemos win.
func writeBaselines(p func(string, ...any), s ScenarioResult) {
	if len(s.Baselines) == 0 {
		return
	}
	p("  BASELINE (untreated Mnemos arm vs raw retrieval over the same documents, same %d questions)\n",
		len(s.Questions))
	for _, b := range s.Baselines {
		p("    %s: %s\n", b.Name, b.Description)
	}
	p("    %-26s %10s", "metric", "mnemos")
	for _, b := range s.Baselines {
		p(" %17s", b.Name)
	}
	p("  verdicts\n")
	for i, c := range s.Baselines[0].Comparisons {
		p("    %-26s %10s", c.Name, fmtMetric(c.Mnemos))
		verdicts := make([]string, 0, len(s.Baselines))
		for _, b := range s.Baselines {
			// Every baseline is scored by the same battery, so rows align;
			// a misaligned row is printed as unknown rather than guessed.
			if i >= len(b.Comparisons) || b.Comparisons[i].Name != c.Name {
				p(" %17s", "unknown")
				verdicts = append(verdicts, string(BaselineUnknown))
				continue
			}
			bc := b.Comparisons[i]
			p(" %17s", fmtMetric(bc.Baseline))
			verdicts = append(verdicts, string(bc.Verdict))
		}
		p("  %s\n", strings.Join(verdicts, " / "))
	}
	for _, b := range s.Baselines {
		p("    vs %s: %d mnemos-better, %d baseline-better, %d tie, %d unknown\n",
			b.Name, b.Summary.MnemosBetter, b.Summary.BaselineBetter, b.Summary.Tie, b.Summary.Unknown)
	}
	p("\n")
	writeCost(p, s.Cost)
}

// writeCost renders the cost side: what each system spent to give the answers
// in the baseline table. Wall-clock figures; no verdict is drawn from them.
func writeCost(p func(string, ...any), c CostReport) {
	if len(c.Systems) == 0 {
		return
	}
	p("  COST (wall-clock, varies run to run; corpus %d bytes of raw text)\n", c.CorpusBytes)
	p("    %-17s %10s %11s %12s %8s %10s %10s  %s\n",
		"system", "build_ms", "curation_ms", "stored_bytes", "queries", "q_mean_ms", "q_max_ms", "rows")
	for _, s := range c.Systems {
		kinds := make([]string, 0, len(s.Rows))
		for k := range s.Rows {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		rows := make([]string, len(kinds))
		for i, k := range kinds {
			rows[i] = fmt.Sprintf("%s=%d", k, s.Rows[k])
		}
		p("    %-17s %10.2f %11.2f %12d %8d %10.3f %10.3f  %s\n",
			s.System, s.BuildMS, s.CurationMS, s.StoredBytes, s.Queries, s.QueryMeanMS, s.QueryMaxMS,
			strings.Join(rows, " "))
	}
	p("\n")
}

func fmtMetric(m Metric) string {
	if !m.Known {
		return "unknown"
	}
	if m.Unit == "count" || m.Unit == "bytes" {
		return fmt.Sprintf("%.0f", m.Value)
	}
	return fmt.Sprintf("%.4f", m.Value)
}

func verdictMark(v Verdict) string {
	switch v {
	case VerdictBetter:
		return "+ "
	case VerdictWorse:
		return "- "
	case VerdictChanged:
		return "~ "
	case VerdictUnknown:
		return "? "
	default:
		return "  "
	}
}

func verdictLabel(c Comparison) string {
	if c.Verdict == VerdictUnknown {
		reason := c.Control.Unknown
		if reason == "" {
			reason = c.Treatment.Unknown
		}
		if reason != "" {
			return string(c.Verdict) + " (" + reason + ")"
		}
	}
	if c.Verdict == VerdictChanged {
		return "changed (descriptive — no direction claimed)"
	}
	return string(c.Verdict)
}

func verdictExplanation(v RunVerdict) string {
	switch v {
	case VerdictInert:
		return "the process reported zero mutations; it did not act on this brain"
	case VerdictNoEffect:
		return "the process mutated the brain but moved no scored metric — activity without demonstrated benefit"
	case VerdictImproved:
		return "scored metrics moved only in the improving direction"
	case VerdictRegressed:
		return "scored metrics moved only in the degrading direction"
	case VerdictMixed:
		return "some scored metrics improved and others degraded; read the rows, not the headline"
	case VerdictInvalid:
		return "two identically-untreated arms disagreed, so no delta in this run is attributable"
	default:
		return ""
	}
}

// wrapIndent soft-wraps long limitation text so the human report stays readable
// in a terminal.
func wrapIndent(s string, width int, indent string) string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return ""
	}
	var b strings.Builder
	lineLen := 0
	for i, w := range words {
		if i > 0 {
			if lineLen+1+len(w) > width {
				b.WriteString("\n" + indent)
				lineLen = 0
			} else {
				b.WriteString(" ")
				lineLen++
			}
		}
		b.WriteString(w)
		lineLen += len(w)
	}
	return b.String()
}

// writeRetraction renders one scenario's evidence-retraction track.
func writeRetraction(p func(string, ...any), rt *RetractionResult) {
	p("  RETRACTION (ADR 0037) — episode %s (%s)\n", rt.Episode, rt.EpisodeID)
	p("    recognition: %s\n", wrapIndent(rt.Recognition, 72, "                 "))
	if rt.Validity.Deterministic {
		p("    measurement deterministic: yes (two retracted arms agreed on every metric)\n")
	} else {
		p("    measurement deterministic: NO — %s\n", strings.Join(rt.Validity.NondeterministicMetrics, ", "))
	}
	p("    activity: %d beliefs withdrawn or rescored\n", rt.Activity)
	p("\n    EFFECT (retracted minus unretracted reference)\n")
	p("    %-34s %10s %10s %10s  %s\n", "metric", "reference", "retracted", "delta", "verdict")
	for _, c := range rt.Effect {
		delta := "—"
		if c.Verdict != VerdictUnknown {
			delta = fmt.Sprintf("%+.4f", c.Delta)
		}
		p("    %-34s %10s %10s %10s  %s%s\n", c.Name, fmtMetric(c.Control), fmtMetric(c.Treatment),
			delta, verdictMark(c.Verdict), verdictLabel(c))
		if c.Note != "" {
			p("    %-34s %s\n", "", "^ "+c.Note)
		}
	}
	p("    VERDICT: %s — %s\n\n", rt.Verdict, verdictExplanation(rt.Verdict))

	if len(rt.Baselines) > 0 {
		p("    BASELINE (retracted Mnemos arm vs raw retrieval with the document's chunks removed)\n")
		p("    %-26s %10s", "metric", "mnemos")
		for _, b := range rt.Baselines {
			p(" %17s", b.Name)
		}
		p("  verdicts\n")
		for i, c := range rt.Baselines[0].Comparisons {
			p("    %-26s %10s", c.Name, fmtMetric(c.Mnemos))
			verdicts := make([]string, 0, len(rt.Baselines))
			for _, b := range rt.Baselines {
				if i >= len(b.Comparisons) || b.Comparisons[i].Name != c.Name {
					p(" %17s", "unknown")
					verdicts = append(verdicts, string(BaselineUnknown))
					continue
				}
				p(" %17s", fmtMetric(b.Comparisons[i].Baseline))
				verdicts = append(verdicts, string(b.Comparisons[i].Verdict))
			}
			p("  %s\n", strings.Join(verdicts, " / "))
		}
		p("\n")
	}

	p("    PER BELIEF (S surviving, Q surfaced by a query, W withdrawn, R rescored; - = no)\n")
	p("    %-13s %-40s %9s %9s", "role", "gold", "reference", "retracted")
	for _, b := range rt.Baselines {
		p(" %17s", b.Name)
	}
	p("\n")
	for _, it := range rt.Items {
		p("    %-13s %-40s %9s %9s", it.Role, truncate(it.Text, 40), stateCode(it.Reference), stateCode(it.Retracted))
		for _, b := range rt.Baselines {
			p(" %17s", stateCode(it.Baselines[b.Name]))
		}
		p("\n")
	}
	p("\n    HYPOTHESIS: %s — %s\n", rt.Hypothesis.Verdict, rt.Hypothesis.Statement)
	for _, reason := range rt.Hypothesis.Reasons {
		p("      - %s\n", reason)
	}
	p("\n")
}

// stateCode renders an ItemState as four fixed columns.
func stateCode(s ItemState) string {
	code := []byte("----")
	for i, on := range []bool{s.Surviving, s.Surfaced, s.Withdrawn, s.Rescored} {
		if on {
			code[i] = "SQWR"[i]
		}
	}
	return string(code)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
