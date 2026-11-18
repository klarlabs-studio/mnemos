// Package brainbench is a controlled-experiment harness that answers one
// question the rest of the codebase cannot: do the cognitive processes
// (consolidate / forget / reinforce / credit / salience / synthesize / decay)
// actually IMPROVE what the brain knows?
//
// Mnemos can already demonstrate that those processes RAN — `mnemos
// consolidate` returns counters, the cognitive journal records passes, ADR 0019
// reports vital signs. None of that is evidence of BENEFIT. A pass that
// deprecates 400 claims and decays 900 edges has produced a large number and no
// established improvement. This package exists to make that distinction
// measurable rather than rhetorical.
//
// # Method
//
// A paired A/B experiment on byte-identical brains:
//
//  1. Seed one pristine brain from a scenario corpus (deterministic: rule-based
//     extraction, a deterministic stub embedder, no network, no LLM).
//  2. Copy the seed file three times. The copies are byte-identical, so the arms
//     cannot differ for any reason except the treatment.
//  3. CONTROL: measure, run nothing.
//  4. TREATMENT: run the process set, then measure.
//  5. NOISE: a second untreated copy, measured independently. It must produce
//     exactly the control's numbers. When it does not, measurement is not
//     deterministic and no delta from this run can be attributed to the process
//     — the report says so and stops claiming an effect.
//
// Measurement itself is a mutation (recall applies Hebbian strengthening,
// reconsolidation and competitive inhibition write-backs), which is why every
// arm is a throwaway copy and each is measured exactly once.
//
// # Honesty constraints
//
// These are the point of the package, not decoration:
//
//   - Every metric declares a DIRECTION. Metrics with no defensible normative
//     direction (claim count, database size, mean trust) are Descriptive: the
//     harness reports that they changed and refuses to call the change good.
//     Forgetting mechanically shrinks the brain and mechanically raises mean
//     trust; scoring those as wins would make the harness a rubber stamp.
//   - A metric that cannot be computed reliably is reported UNKNOWN with a
//     reason. It never gets a fabricated number, and an unknown on either arm
//     makes the comparison unknown rather than optimistic.
//   - Regressions are first-class. GoldSurvival exists specifically to catch the
//     failure mode where a process improves retrieval precision by destroying
//     knowledge.
//   - Activity and benefit are reported in separate fields and reconciled in the
//     verdict: a run with high activity and no metric movement is labelled
//     ran-no-measurable-effect, not success.
//
// # Further tracks, outside the A/B verdict
//
//   - Temporal reconstruction (timeline.go): a scenario may build its brain
//     phase by phase on the real write clock and ask as-of questions on both
//     time axes. The as_of_* metrics are measured on every arm like any other
//     metric, so a process that corrupts history regresses them.
//   - Raw-retrieval baselines (baseline.go): BM25 over the seed's raw event
//     texts, asked the identical questions, compared with the untreated arm.
//     Reported beside the A/B result and never folded into its verdict.
//   - Evidence retraction (retraction.go): a scenario may retract one of its
//     documents (ADR 0037) on arms of its own and is scored on which
//     dependent beliefs stop surviving, which supported ones keep surviving,
//     and how well the impact report matches the gold, against raw retrieval
//     that deletes the document's chunks only.
package brainbench

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Scenario is one controlled experiment: a corpus to seed both arms from, the
// probes both arms are measured with, and the process set the treatment arm
// runs. It deliberately mirrors the seeds/query/gold idiom of
// data/eval/retrieval.yaml so the two suites read alike, but it is a different
// unit of work: retrieval.yaml compares RANKERS over a fixed store, this
// compares STORES that a process has or has not been applied to.
type Scenario struct {
	ID          string `yaml:"id"`
	Description string `yaml:"description"`

	// Corpus is ingested document-by-document, in order, each as its own
	// batch — the way a real brain accumulates across sessions, and the only
	// way near-duplicates across sessions arise at all. Ingesting the whole
	// corpus as one batch would let the ingest-time deduper collapse exactly
	// the redundancy consolidation is supposed to be tested on.
	Corpus []Doc `yaml:"corpus"`

	// Timeline replaces Corpus for a temporal-reconstruction scenario: the
	// brain is built phase by phase on the real write clock, and each phase
	// may apply curation changes (retract, archive, supersede, contradict)
	// after its documents are ingested. A scenario sets exactly one of Corpus
	// and Timeline. See timeline.go for why the phases use real time.
	Timeline []Phase `yaml:"timeline"`

	// Warmup queries are run against the SEED, before the arms are copied, so
	// both arms inherit their effects identically.
	//
	// They exist because several consolidation stages act on state that only
	// retrieval creates. DecayAssociations pulls over-base Hebbian edge
	// strengths back toward 1.0, and DecayInhibition pulls competitive-
	// inhibition weights back toward 0 — but on a freshly-ingested brain no
	// edge is over base and nothing is inhibited, so both stages report zero
	// and the scenario measures nothing. Without a warmup the harness would
	// report "inert" for a stage that is merely untested, which is a different
	// and much less useful claim.
	Warmup []string `yaml:"warmup"`

	// Probes are the retrieval questions both arms answer.
	Probes []Probe `yaml:"probes"`

	// AsOfProbes are time-travel questions, answered against a phase
	// boundary of the Timeline. Only a timeline scenario can declare them,
	// because only a timeline has instants to ask about.
	AsOfProbes []AsOfProbe `yaml:"as_of_probes"`

	// Process is the treatment: the exact consolidation option set under test.
	Process Process `yaml:"process"`

	// Retraction, when set, adds the evidence-retraction track
	// (retraction.go): one of the scenario's documents is withdrawn as a
	// source (ADR 0037) and the brain is scored on which beliefs it stops
	// holding. It is a separate experiment from Process, with its own arms.
	Retraction *Retraction `yaml:"retraction"`
}

// Doc is one ingested document.
type Doc struct {
	ID     string `yaml:"id"`
	Source string `yaml:"source"`

	// AgeDays back-dates the document's event timestamp. This is not cosmetic:
	// trust freshness decays on a 90-day half-life, so with everything stamped
	// "now" the forgetting stage can never fire and a scenario would silently
	// measure nothing. Back-dating is the only way to reach decay-driven
	// behaviour offline and deterministically.
	AgeDays int `yaml:"age_days"`

	Text string `yaml:"text"`
}

// Probe is one retrieval question plus what a correct answer looks like.
//
// Expectations are matched on TEXT substrings, not claim ids: claim ids are
// minted per ingest and are not stable across runs, and a scenario author can
// reason about text but not about a hash.
type Probe struct {
	ID    string `yaml:"id"`
	Query string `yaml:"query"`

	// Expect is a case-insensitive substring the correct claim must contain.
	Expect string `yaml:"expect"`

	// MustNot lists substrings that should NOT appear in the returned set —
	// superseded or refuted statements the brain ought to stop surfacing.
	// Optional; when no probe in a scenario declares any, the corresponding
	// metric is reported unknown rather than a vacuous 0.
	MustNot []string `yaml:"must_not"`

	// Limit caps results for this probe. 0 uses ProbeDefaultLimit.
	Limit int `yaml:"limit"`

	// Class optionally groups probes into a task class ("current",
	// "change_why", ...). Every class present in a scenario gets an
	// accuracy_<class> metric, scored by the strict rule as-of probes use, so
	// a scenario can report accuracy per kind of question for Mnemos and for
	// every baseline. A lowercase identifier: [a-z][a-z0-9_]*.
	Class string `yaml:"class"`
}

// AsOfProbe is a question about the past, with its gold answer.
//
// Exactly one axis is set, so each probe scores exactly one metric:
//
//   - RecordedAsOf names a phase: "what did the brain believe at the end of
//     that phase?" (transaction time, mnemos.Query.RecordedAsOf).
//   - ValidAsOf names a phase: "what is known now about the end of that
//     phase?" (valid time, mnemos.Query.AsOf, read with today's knowledge).
//
// A probe is answered correctly only when Expect is in the top results AND no
// MustNot string is: a time-travel read that returns the right belief beside a
// belief the brain did not hold then is a wrong reconstruction.
type AsOfProbe struct {
	ID           string   `yaml:"id"`
	Query        string   `yaml:"query"`
	RecordedAsOf string   `yaml:"recorded_as_of"`
	ValidAsOf    string   `yaml:"valid_as_of"`
	Expect       string   `yaml:"expect"`
	MustNot      []string `yaml:"must_not"`
	Limit        int      `yaml:"limit"`
	// Class groups the probe into a task class; see Probe.Class.
	Class string `yaml:"class"`
}

// Phase is one step of a timeline: documents ingested, then changes applied.
type Phase struct {
	Name    string        `yaml:"phase"`
	Docs    []TimelineDoc `yaml:"docs"`
	Changes []Change      `yaml:"changes"`
}

// TimelineDoc is a document ingested during a phase.
type TimelineDoc struct {
	ID     string `yaml:"id"`
	Source string `yaml:"source"`
	// ValidFrom names the phase whose start the document's facts hold from.
	// Empty means the phase it is ingested in. It separates valid time from
	// transaction time in both directions: a root cause written down on
	// Thursday was true since Monday's deploy (an earlier phase), and an
	// ownership change announced in February takes effect in March (a later
	// phase). A later phase's start is reserved on the write clock when the
	// document is ingested; see timeline.go.
	ValidFrom string `yaml:"valid_from"`
	Text      string `yaml:"text"`
}

// Change is one curation operation applied after a phase's documents. Each
// target is a case-insensitive substring that must match exactly one current
// claim; anything else is an authoring error, not a silent no-op.
//
// These are structured operations a raw-retrieval baseline has no equivalent
// for: the baseline sees the documents only.
type Change struct {
	// Retract deprecates the matching claim (a status transition).
	Retract string `yaml:"retract"`
	// Archive stops the matching claim surfacing (ADR 0034 archive axis).
	Archive string `yaml:"archive"`
	// Supersede ends the matching claim's valid time now.
	Supersede string `yaml:"supersede"`
	// SupersedeBy ends the old claim's valid time at the new claim's valid
	// start — the semantics of `mnemos resolve --supersedes`. Unlike
	// Supersede, the cutoff is not "now": a change recorded late ends the old
	// claim in the past, and a change announced ahead ends it in the future.
	SupersedeBy *SupersedeEdge `yaml:"supersede_by"`
	// Contradict records a contradicts edge between two claims.
	Contradict *ContradictEdge `yaml:"contradict"`
	Reason     string          `yaml:"reason"`
}

// SupersedeEdge names the superseded claim and its successor.
type SupersedeEdge struct {
	Old string `yaml:"old"`
	New string `yaml:"new"`
}

// ContradictEdge names the two claims of a contradicts edge.
type ContradictEdge struct {
	From string `yaml:"from"`
	To   string `yaml:"to"`
}

func (c Change) ops() int {
	n := 0
	for _, set := range []bool{c.Retract != "", c.Archive != "", c.Supersede != "", c.SupersedeBy != nil, c.Contradict != nil} {
		if set {
			n++
		}
	}
	return n
}

// ProbeDefaultLimit is the top-k used when a probe does not set Limit.
const ProbeDefaultLimit = 10

// Process is the consolidation option set the treatment arm runs, expressed in
// scenario YAML so each scenario declares precisely which process set it is
// making a claim about. Field-for-field this is mnemos.ConsolidateOptions; it is
// restated here rather than embedded so the YAML key names are stable even if
// the library struct is reorganised, and so a scenario cannot smuggle in
// non-deterministic options.
type Process struct {
	DedupeThreshold    float64 `yaml:"dedupe_threshold"`
	ForgetBelowTrust   float64 `yaml:"forget_below_trust"`
	ForgetRefuted      bool    `yaml:"forget_refuted"`
	ReinforceValidated bool    `yaml:"reinforce_validated"`
	ReinforcePlaybooks bool    `yaml:"reinforce_playbooks"`
	AssignCredit       bool    `yaml:"credit"`
	AssignSalience     bool    `yaml:"salience"`
	Plastic            bool    `yaml:"plastic"`
	DecayAssociations  bool    `yaml:"decay_associations"`
	DecayInhibition    bool    `yaml:"decay_inhibition"`
	Synthesize         bool    `yaml:"synthesize"`
	ReplayTopK         int     `yaml:"replay_top_k"`
}

// Enabled reports the process stages this scenario turns on, for the report.
// Named stages, not a bool blob, so a reader can tell what was under test.
func (p Process) Enabled() []string {
	var out []string
	// Dedupe + trust-refresh always run in a consolidation pass; they are not
	// optional stages, so they are listed unconditionally.
	out = append(out, "dedupe", "trust_refresh")
	if p.ForgetBelowTrust > 0 {
		out = append(out, fmt.Sprintf("forget_below_trust=%.2f", p.ForgetBelowTrust))
	}
	for _, f := range []struct {
		on   bool
		name string
	}{
		{p.ForgetRefuted, "forget_refuted"},
		{p.ReinforceValidated, "reinforce_validated"},
		{p.ReinforcePlaybooks, "reinforce_playbooks"},
		{p.AssignCredit, "credit"},
		{p.AssignSalience, "salience"},
		{p.Plastic, "plastic"},
		{p.DecayAssociations, "decay_associations"},
		{p.DecayInhibition, "decay_inhibition"},
		{p.Synthesize, "synthesize"},
	} {
		if f.on {
			out = append(out, f.name)
		}
	}
	if p.ReplayTopK > 0 {
		out = append(out, fmt.Sprintf("replay_top_k=%d", p.ReplayTopK))
	}
	return out
}

// Validate rejects a scenario that cannot yield an interpretable result.
//
// It is strict on purpose. A scenario with no probes still "runs" and still
// prints a table, but every retrieval metric would be an empty average — a
// number with no content behind it. Refusing to load it is the difference
// between a harness and a number generator.
func (s Scenario) Validate() error {
	if strings.TrimSpace(s.ID) == "" {
		return fmt.Errorf("scenario: id is required")
	}
	switch {
	case len(s.Corpus) > 0 && len(s.Timeline) > 0:
		return fmt.Errorf("scenario %s: set corpus or timeline, not both", s.ID)
	case len(s.Corpus) == 0 && len(s.Timeline) == 0:
		return fmt.Errorf("scenario %s: at least one corpus document is required", s.ID)
	}
	if err := s.validateTimeline(); err != nil {
		return err
	}
	seenDoc := make(map[string]struct{}, len(s.Corpus))
	for i, d := range s.Corpus {
		if strings.TrimSpace(d.ID) == "" {
			return fmt.Errorf("scenario %s: corpus[%d]: id is required", s.ID, i)
		}
		if _, dup := seenDoc[d.ID]; dup {
			return fmt.Errorf("scenario %s: corpus doc id %q is duplicated", s.ID, d.ID)
		}
		seenDoc[d.ID] = struct{}{}
		if strings.TrimSpace(d.Text) == "" {
			return fmt.Errorf("scenario %s: corpus doc %s: text is required", s.ID, d.ID)
		}
		if d.AgeDays < 0 {
			return fmt.Errorf("scenario %s: corpus doc %s: age_days must not be negative", s.ID, d.ID)
		}
	}
	if len(s.Probes) == 0 {
		return fmt.Errorf("scenario %s: at least one probe is required "+
			"(a scenario with no probes cannot measure answer quality)", s.ID)
	}
	seenProbe := make(map[string]struct{}, len(s.Probes))
	for i, p := range s.Probes {
		if strings.TrimSpace(p.ID) == "" {
			return fmt.Errorf("scenario %s: probes[%d]: id is required", s.ID, i)
		}
		if _, dup := seenProbe[p.ID]; dup {
			return fmt.Errorf("scenario %s: probe id %q is duplicated", s.ID, p.ID)
		}
		seenProbe[p.ID] = struct{}{}
		if strings.TrimSpace(p.Query) == "" {
			return fmt.Errorf("scenario %s: probe %s: query is required", s.ID, p.ID)
		}
		if strings.TrimSpace(p.Expect) == "" {
			return fmt.Errorf("scenario %s: probe %s: expect is required", s.ID, p.ID)
		}
		if p.Limit < 0 {
			return fmt.Errorf("scenario %s: probe %s: limit must not be negative", s.ID, p.ID)
		}
		if !validClass(p.Class) {
			return fmt.Errorf("scenario %s: probe %s: class %q must match [a-z][a-z0-9_]*", s.ID, p.ID, p.Class)
		}
	}
	if err := s.validateAsOfProbes(seenProbe); err != nil {
		return err
	}
	if err := s.validateRetraction(); err != nil {
		return err
	}
	if s.Process.DedupeThreshold < 0 || s.Process.DedupeThreshold > 1 {
		return fmt.Errorf("scenario %s: process.dedupe_threshold must be in [0, 1]", s.ID)
	}
	if s.Process.ForgetBelowTrust < 0 || s.Process.ForgetBelowTrust > 1 {
		return fmt.Errorf("scenario %s: process.forget_below_trust must be in [0, 1]", s.ID)
	}
	if s.Process.ReplayTopK < 0 {
		return fmt.Errorf("scenario %s: process.replay_top_k must not be negative", s.ID)
	}
	return nil
}

// validateTimeline checks phase names, doc ids and change shapes.
func (s Scenario) validateTimeline() error {
	all := make(map[string]struct{}, len(s.Timeline))
	for _, ph := range s.Timeline {
		all[ph.Name] = struct{}{}
	}
	phases := make(map[string]struct{}, len(s.Timeline))
	docs := make(map[string]struct{})
	for i, ph := range s.Timeline {
		if strings.TrimSpace(ph.Name) == "" {
			return fmt.Errorf("scenario %s: timeline[%d]: phase is required", s.ID, i)
		}
		if _, dup := phases[ph.Name]; dup {
			return fmt.Errorf("scenario %s: phase %q is duplicated", s.ID, ph.Name)
		}
		phases[ph.Name] = struct{}{}
		if len(ph.Docs) == 0 && len(ph.Changes) == 0 {
			return fmt.Errorf("scenario %s: phase %s has no docs and no changes", s.ID, ph.Name)
		}
		for j, d := range ph.Docs {
			if strings.TrimSpace(d.ID) == "" {
				return fmt.Errorf("scenario %s: phase %s docs[%d]: id is required", s.ID, ph.Name, j)
			}
			if _, dup := docs[d.ID]; dup {
				return fmt.Errorf("scenario %s: corpus doc id %q is duplicated", s.ID, d.ID)
			}
			docs[d.ID] = struct{}{}
			if strings.TrimSpace(d.Text) == "" {
				return fmt.Errorf("scenario %s: doc %s: text is required", s.ID, d.ID)
			}
			// valid_from may name any phase: an earlier one (learned late),
			// this one, or a later one (announced ahead of taking effect).
			if d.ValidFrom != "" {
				if _, ok := all[d.ValidFrom]; !ok {
					return fmt.Errorf("scenario %s: doc %s: valid_from %q is not a phase of this timeline",
						s.ID, d.ID, d.ValidFrom)
				}
			}
		}
		for j, c := range ph.Changes {
			if c.ops() != 1 {
				return fmt.Errorf("scenario %s: phase %s changes[%d]: set exactly one of "+
					"retract, archive, supersede, supersede_by, contradict", s.ID, ph.Name, j)
			}
			if c.SupersedeBy != nil &&
				(strings.TrimSpace(c.SupersedeBy.Old) == "" || strings.TrimSpace(c.SupersedeBy.New) == "") {
				return fmt.Errorf("scenario %s: phase %s changes[%d]: supersede_by needs old and new", s.ID, ph.Name, j)
			}
			if c.Contradict != nil &&
				(strings.TrimSpace(c.Contradict.From) == "" || strings.TrimSpace(c.Contradict.To) == "") {
				return fmt.Errorf("scenario %s: phase %s changes[%d]: contradict needs from and to", s.ID, ph.Name, j)
			}
		}
	}
	return nil
}

// validateAsOfProbes checks the time-travel probes against the timeline.
func (s Scenario) validateAsOfProbes(seen map[string]struct{}) error {
	if len(s.AsOfProbes) > 0 && len(s.Timeline) == 0 {
		return fmt.Errorf("scenario %s: as_of_probes need a timeline to name instants in", s.ID)
	}
	phases := make(map[string]struct{}, len(s.Timeline))
	for _, ph := range s.Timeline {
		phases[ph.Name] = struct{}{}
	}
	for i, p := range s.AsOfProbes {
		if strings.TrimSpace(p.ID) == "" {
			return fmt.Errorf("scenario %s: as_of_probes[%d]: id is required", s.ID, i)
		}
		if _, dup := seen[p.ID]; dup {
			return fmt.Errorf("scenario %s: probe id %q is duplicated", s.ID, p.ID)
		}
		seen[p.ID] = struct{}{}
		if strings.TrimSpace(p.Query) == "" || strings.TrimSpace(p.Expect) == "" {
			return fmt.Errorf("scenario %s: as-of probe %s: query and expect are required", s.ID, p.ID)
		}
		if (p.RecordedAsOf == "") == (p.ValidAsOf == "") {
			return fmt.Errorf("scenario %s: as-of probe %s: set exactly one of recorded_as_of, valid_as_of", s.ID, p.ID)
		}
		phase := p.RecordedAsOf + p.ValidAsOf
		if _, ok := phases[phase]; !ok {
			return fmt.Errorf("scenario %s: as-of probe %s: unknown phase %q", s.ID, p.ID, phase)
		}
		if p.Limit < 0 {
			return fmt.Errorf("scenario %s: as-of probe %s: limit must not be negative", s.ID, p.ID)
		}
		if !validClass(p.Class) {
			return fmt.Errorf("scenario %s: as-of probe %s: class %q must match [a-z][a-z0-9_]*", s.ID, p.ID, p.Class)
		}
	}
	return nil
}

// validClass reports whether class is empty or a lowercase identifier, so
// accuracy_<class> is a well-formed metric name.
func validClass(class string) bool {
	for i, r := range class {
		switch {
		case r >= 'a' && r <= 'z':
		case i > 0 && (r == '_' || (r >= '0' && r <= '9')):
		default:
			return false
		}
	}
	return true
}

// ParseScenario decodes and validates one scenario YAML document.
//
// KnownFields is enabled: a typo'd key (`forget_bellow_trust`) would otherwise
// be silently ignored, and the scenario would quietly test a different process
// set than its author wrote down — the worst possible failure for an
// experimental harness, because the result still looks credible.
func ParseScenario(data []byte) (Scenario, error) {
	var s Scenario
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return Scenario{}, fmt.Errorf("parse scenario: %w", err)
	}
	if err := s.Validate(); err != nil {
		return Scenario{}, err
	}
	return s, nil
}
