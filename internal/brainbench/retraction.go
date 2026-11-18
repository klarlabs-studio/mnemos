package brainbench

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"go.klarlabs.de/mnemos"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
)

// Evidence-retraction track.
//
// The hypothesis under test (ADR 0037): dependency maintenance — withdrawing a
// source and propagating that to the beliefs it supported — reduces the
// unsupported beliefs a brain keeps holding. The pass condition is fewer
// surviving unsupported beliefs than a strong simple baseline, WITHOUT losing
// more supported ones.
//
// The baseline is raw chunk retrieval where retraction means deleting the
// retracted document's chunks and nothing else: every copy, restatement and
// conclusion drawn from it in another document stays. That is what a team with
// a document store and a search box does, and it is the honest bar.
//
// Arms, all byte copies of the warmed seed:
//
//   - reference: not retracted. What retraction is measured against.
//   - retracted: the episode is retracted through the public library facade
//     (Memory.RetractEvent, the path every surface shares).
//   - noise: a second retracted copy, measured independently. It must
//     reproduce the retracted arm exactly, or the track is invalid.
//   - one per raw-retrieval baseline, over the seed's event texts minus the
//     retracted document.
//
// How a dependent is recognised is the substance of the finding, so it is
// stated rather than hidden: Mnemos follows EVIDENCE LINKS. A belief whose only
// live evidence is the retracted episode is withdrawn; one with any other live
// evidence is kept and rescored. A later document that restates the retracted
// claim verbatim is folded onto the same belief at ingest (exact-text dedupe)
// and so counts as independent evidence; a paraphrase or a conclusion drawn in
// another document is a separate belief with its own evidence and is not
// reached. The track measures that as it is. The scenario's gold says which
// restatements are copies; nothing in the harness or the product reads it.

// Roles a gold belief can play. The first three are dependents: they rest on
// the retracted source and should stop being believed once it is withdrawn. The
// last two must survive.
const (
	// RoleOriginal is asserted by the retracted document. A later document may
	// repeat the same words; the gold declares that repetition a copy.
	RoleOriginal = "original"
	// RoleCopy is a later document restating the retracted claim in other
	// words. Copied misinformation is not independent evidence.
	RoleCopy = "copy"
	// RoleDerived is a conclusion another document drew from the retracted
	// claim.
	RoleDerived = "derived"
	// RoleCorroborated is asserted by the retracted document AND independently
	// by another one. It must survive.
	RoleCorroborated = "corroborated"
	// RoleIndependent never came from the retracted document. It must survive.
	RoleIndependent = "independent"
)

func isDependentRole(role string) bool {
	return role == RoleOriginal || role == RoleCopy || role == RoleDerived
}

// Retraction is a scenario's retraction experiment.
type Retraction struct {
	// Episode is the id of the scenario document to retract.
	Episode string `yaml:"episode"`
	Reason  string `yaml:"reason"`
	// Queries are asked of every arm after the retraction; Limit is their
	// top-k (0 uses ProbeDefaultLimit).
	Queries []string `yaml:"queries"`
	Limit   int      `yaml:"limit"`
	// Gold lists the beliefs the retraction is judged on.
	Gold []RetractionGold `yaml:"gold"`
}

// RetractionGold is one belief and the role it plays. Text is a
// case-insensitive substring matched against claim texts (Mnemos) and chunk
// texts (baselines), exactly like probe expectations.
type RetractionGold struct {
	Text string `yaml:"text"`
	Role string `yaml:"role"`
	Note string `yaml:"note"`
}

// docTexts maps every document id of the scenario to its text.
func (s Scenario) docTexts() map[string]string {
	out := make(map[string]string, len(s.Corpus))
	for _, d := range s.Corpus {
		out[d.ID] = d.Text
	}
	for _, ph := range s.Timeline {
		for _, d := range ph.Docs {
			out[d.ID] = d.Text
		}
	}
	return out
}

// validateRetraction checks the retraction block against the documents, so a
// gold role that the corpus cannot support is an authoring error at load time
// rather than a misleading score.
func (s Scenario) validateRetraction() error {
	r := s.Retraction
	if r == nil {
		return nil
	}
	docs := s.docTexts()
	episode, ok := docs[r.Episode]
	if !ok {
		return fmt.Errorf("scenario %s: retraction.episode %q is not a document of this scenario", s.ID, r.Episode)
	}
	if len(r.Queries) == 0 {
		return fmt.Errorf("scenario %s: retraction needs at least one query", s.ID)
	}
	for i, q := range r.Queries {
		if strings.TrimSpace(q) == "" {
			return fmt.Errorf("scenario %s: retraction.queries[%d] is empty", s.ID, i)
		}
	}
	if r.Limit < 0 {
		return fmt.Errorf("scenario %s: retraction.limit must not be negative", s.ID)
	}
	var dependents, survivors int
	seen := make(map[string]struct{}, len(r.Gold))
	for i, g := range r.Gold {
		text := strings.TrimSpace(g.Text)
		if text == "" {
			return fmt.Errorf("scenario %s: retraction.gold[%d]: text is required", s.ID, i)
		}
		if _, dup := seen[strings.ToLower(text)]; dup {
			return fmt.Errorf("scenario %s: retraction gold %q is duplicated", s.ID, text)
		}
		seen[strings.ToLower(text)] = struct{}{}
		inEpisode := containsFold(episode, text)
		elsewhere := false
		for id, body := range docs {
			if id != r.Episode && containsFold(body, text) {
				elsewhere = true
				break
			}
		}
		var want string
		switch g.Role {
		case RoleOriginal:
			dependents++
			if !inEpisode {
				want = "appear in the retracted document"
			}
		case RoleCopy, RoleDerived:
			dependents++
			if inEpisode || !elsewhere {
				want = "appear in another document and not in the retracted one"
			}
		case RoleCorroborated:
			survivors++
			if !inEpisode || !elsewhere {
				want = "appear in the retracted document and in another one"
			}
		case RoleIndependent:
			survivors++
			if inEpisode || !elsewhere {
				want = "appear in another document and not in the retracted one"
			}
		default:
			return fmt.Errorf("scenario %s: retraction gold %q: role %q is not one of %s, %s, %s, %s, %s",
				s.ID, text, g.Role, RoleOriginal, RoleCopy, RoleDerived, RoleCorroborated, RoleIndependent)
		}
		if want != "" {
			return fmt.Errorf("scenario %s: retraction gold %q (role %s) must %s", s.ID, text, g.Role, want)
		}
	}
	// Both sides are required: without dependents nothing measures whether
	// retraction works, and without survivors nothing notices a retraction
	// that simply deletes everything near the source.
	if dependents == 0 || survivors == 0 {
		return fmt.Errorf("scenario %s: retraction gold needs at least one dependent (%s/%s/%s) "+
			"and one surviving (%s/%s) belief", s.ID, RoleOriginal, RoleCopy, RoleDerived, RoleCorroborated, RoleIndependent)
	}
	return nil
}

// Retraction-track metric names.
const (
	// MetricUnsupportedSurviving is the share of dependent gold beliefs still
	// held: a live claim (Mnemos) or a visible chunk (baseline) contains it.
	// The hypothesis' primary metric.
	MetricUnsupportedSurviving = "unsupported_surviving"
	// MetricUnsupportedSurfaced is the share of dependent gold beliefs
	// returned in the top-k of at least one retraction query.
	MetricUnsupportedSurfaced = "unsupported_surfaced"
	// MetricSupportedSurviving is the share of must-survive gold beliefs still
	// held. The anti-gaming metric: deleting everything near the source scores
	// a perfect MetricUnsupportedSurviving and a bad one here.
	MetricSupportedSurviving = "supported_surviving"
	// MetricSupportedSurfaced is the share of must-survive gold beliefs
	// returned by at least one retraction query.
	MetricSupportedSurfaced = "supported_surfaced"
	// MetricImpactPrecision is the share of what the retraction withdrew that
	// is a dependent gold belief.
	MetricImpactPrecision = "impact_precision"
	// MetricImpactRecall is the share of dependent gold beliefs the retraction
	// withdrew.
	MetricImpactRecall = "impact_recall"
	// MetricImpactWithdrawn counts what the retraction withdrew: beliefs
	// (Mnemos) or chunks (baseline). Descriptive.
	MetricImpactWithdrawn = "impact_withdrawn"
	// MetricImpactRescored counts beliefs kept on other evidence and rescored.
	// Descriptive; a baseline has no trust to rescore.
	MetricImpactRescored = "impact_rescored"
)

// retractionOutcome is what a retraction reported, as claim ids.
type retractionOutcome struct {
	Withdrawn []string
	Rescored  []string
}

// retractFunc retracts eventID in the store at dsn. Tests substitute a
// deliberately wrong one to prove the track reports the damage.
type retractFunc func(ctx context.Context, dsn, eventID, reason string) (retractionOutcome, error)

// libraryRetract retracts through the public facade, the same RetractEvent the
// CLI, MCP, REST and gRPC surfaces reach.
func libraryRetract(ctx context.Context, dsn, eventID, reason string) (retractionOutcome, error) {
	mem, err := mnemos.New(mnemos.WithStorage(dsn), mnemos.WithPassiveMode(), mnemos.WithActor(SeedActor))
	if err != nil {
		return retractionOutcome{}, fmt.Errorf("brainbench: open retraction handle: %w", err)
	}
	defer func() { _ = mem.Close() }()
	rep, err := mem.RetractEvent(ctx, eventID, reason)
	if err != nil {
		return retractionOutcome{}, fmt.Errorf("brainbench: retract %s: %w", eventID, err)
	}
	out := retractionOutcome{Withdrawn: rep.Impact.RetractedClaims}
	for _, r := range rep.Impact.Rescored {
		out.Rescored = append(out.Rescored, r.ID)
	}
	return out, nil
}

// ItemState is what happened to one gold belief on one arm.
type ItemState struct {
	// Surviving: still held (a live claim or a visible chunk contains it).
	Surviving bool `json:"surviving"`
	// Surfaced: returned by at least one retraction query.
	Surfaced bool `json:"surfaced"`
	// Withdrawn: the retraction withdrew a claim or chunk containing it.
	Withdrawn bool `json:"withdrawn"`
	// Rescored: a claim containing it was kept on other evidence (Mnemos only).
	Rescored bool `json:"rescored"`
}

// GoldOutcome is one gold belief across every arm of the track.
type GoldOutcome struct {
	Text      string               `json:"text"`
	Role      string               `json:"role"`
	Dependent bool                 `json:"dependent"`
	Note      string               `json:"note,omitempty"`
	Reference ItemState            `json:"reference"`
	Retracted ItemState            `json:"retracted"`
	Baselines map[string]ItemState `json:"baselines"`
}

// HypothesisVerdict is the outcome of the track's pass condition.
type HypothesisVerdict string

const (
	// HypothesisSupported — fewer unsupported beliefs survive than on every
	// baseline, and no fewer supported ones.
	HypothesisSupported HypothesisVerdict = "supported"
	// HypothesisNotSupported — the pass condition failed against at least one
	// baseline.
	HypothesisNotSupported HypothesisVerdict = "not-supported"
	// HypothesisUnknown — a metric the condition needs was unmeasurable, or
	// the measurement was not deterministic.
	HypothesisUnknown HypothesisVerdict = "unknown"
)

// Hypothesis is the track's pass condition and its outcome.
type Hypothesis struct {
	Statement string            `json:"statement"`
	Verdict   HypothesisVerdict `json:"verdict"`
	Reasons   []string          `json:"reasons"`
}

// RetractionResult is the evidence-retraction track of one scenario.
type RetractionResult struct {
	Episode   string `json:"episode"`
	EpisodeID string `json:"episode_id"`
	// Recognition states how this run identified dependents. It travels with
	// the numbers because it bounds what they mean.
	Recognition string `json:"recognition"`

	Validity Validity `json:"validity"`
	// Activity is what the retraction reported: withdrawn plus rescored.
	Activity int `json:"activity"`

	Reference MetricSet `json:"reference"`
	Retracted MetricSet `json:"retracted"`
	// Effect compares the retracted arm with the unretracted reference.
	Effect  []Comparison `json:"effect"`
	Summary Summary      `json:"summary"`
	Verdict RunVerdict   `json:"verdict"`

	// Baselines compares the retracted arm with raw retrieval whose
	// retraction removed the document's chunks only.
	Baselines []BaselineResult `json:"baselines"`
	Items     []GoldOutcome    `json:"items"`

	Hypothesis Hypothesis `json:"hypothesis"`
}

// recognitionNote is the statement of how dependents are recognised.
const recognitionNote = "Mnemos follows evidence links (ADR 0037): a belief whose only live evidence is the " +
	"retracted episode is withdrawn; a belief with any other live evidence is kept and rescored. " +
	"A verbatim restatement in another document is folded onto the same belief at ingest and counts " +
	"as independent evidence; a paraphrase or a conclusion in another document is a separate belief " +
	"and is not reached. The baseline removes the retracted document's chunks only. " +
	"The gold's copy/derived roles are author-declared and are read by neither side."

// runRetractionTrack runs the retraction experiment on copies of the warmed
// seed. events are the seed's raw events (the baselines' corpus).
func runRetractionTrack(ctx context.Context, sc Scenario, dir, seedPath string, events []domain.Event, retract retractFunc) (*RetractionResult, error) {
	r := sc.Retraction
	eventID := deterministicEventID(sc.ID, r.Episode)
	res := &RetractionResult{Episode: r.Episode, EpisodeID: eventID, Recognition: recognitionNote}

	refPath := filepath.Join(dir, "retraction_reference.db")
	retPath := filepath.Join(dir, "retraction_retracted.db")
	noisePath := filepath.Join(dir, "retraction_noise.db")
	for _, dst := range []string{refPath, retPath, noisePath} {
		if err := copyDB(seedPath, dst); err != nil {
			return nil, err
		}
	}

	reference, refItems, err := measureRetractionArm(ctx, "sqlite://"+refPath, r, nil)
	if err != nil {
		return nil, fmt.Errorf("brainbench: retraction reference: %w", err)
	}
	// Every gold belief must be held before anything is retracted: a gold text
	// that extraction never produced would score as "withdrawn" for free.
	for i, it := range refItems {
		if !it.Surviving {
			return nil, fmt.Errorf("brainbench: scenario %s: retraction gold %q matches no live claim "+
				"before the retraction; fix the gold text", sc.ID, r.Gold[i].Text)
		}
	}

	outcome, err := retract(ctx, "sqlite://"+retPath, eventID, r.Reason)
	if err != nil {
		return nil, err
	}
	retracted, retItems, err := measureRetractionArm(ctx, "sqlite://"+retPath, r, &outcome)
	if err != nil {
		return nil, fmt.Errorf("brainbench: retraction arm: %w", err)
	}
	noiseOutcome, err := retract(ctx, "sqlite://"+noisePath, eventID, r.Reason)
	if err != nil {
		return nil, err
	}
	noise, _, err := measureRetractionArm(ctx, "sqlite://"+noisePath, r, &noiseOutcome)
	if err != nil {
		return nil, fmt.Errorf("brainbench: retraction noise arm: %w", err)
	}

	res.Reference, res.Retracted = reference, retracted
	res.Activity = len(outcome.Withdrawn) + len(outcome.Rescored)
	dependents := 0
	for _, g := range r.Gold {
		dependents += b2i(isDependentRole(g.Role))
	}
	res.Validity = Validity{
		ProbeCount: len(r.Queries),
		// The rates are over the dependents or over the survivors; the larger
		// group sets the smallest change any of them can express.
		MinResolvableRateDelta:  1 / float64(max(dependents, len(r.Gold)-dependents, 1)),
		NondeterministicMetrics: driftingMetrics(retracted, noise),
	}
	res.Validity.Deterministic = len(res.Validity.NondeterministicMetrics) == 0
	res.Effect = CompareSets(reference, retracted)
	res.Summary = Summarize(res.Effect)
	res.Verdict = trackVerdict(res.Validity.Deterministic, res.Activity, res.Summary)

	res.Items = make([]GoldOutcome, len(r.Gold))
	for i, g := range r.Gold {
		res.Items[i] = GoldOutcome{
			Text: g.Text, Role: g.Role, Dependent: isDependentRole(g.Role), Note: g.Note,
			Reference: refItems[i], Retracted: retItems[i], Baselines: map[string]ItemState{},
		}
	}

	for _, b := range []struct {
		name    string
		recency bool
	}{{BaselineBM25, false}, {BaselineBM25Recency, true}} {
		ms, items, err := measureRetractionBaseline(ctx, r, events, eventID, b.recency)
		if err != nil {
			return nil, fmt.Errorf("brainbench: retraction baseline %s: %w", b.name, err)
		}
		br := BaselineResult{
			Name:        b.name,
			Description: baselineDescriptions[b.name] + "; retraction removes the retracted document's chunks only",
			Metrics:     ms,
		}
		br.Comparisons, br.Summary = compareWithBaseline(retracted, ms)
		res.Baselines = append(res.Baselines, br)
		for i := range res.Items {
			res.Items[i].Baselines[b.name] = items[i]
		}
	}
	res.Hypothesis = judgeHypothesis(res)
	return res, nil
}

// measureRetractionArm scores one Mnemos arm. outcome is nil on the reference
// arm, where nothing was retracted and the impact metrics are unknown.
func measureRetractionArm(ctx context.Context, dsn string, r *Retraction, outcome *retractionOutcome) (MetricSet, []ItemState, error) {
	// Census first: recall writes back, so the store is read before it.
	conn, err := store.Open(ctx, dsn)
	if err != nil {
		return MetricSet{}, nil, fmt.Errorf("open census handle: %w", err)
	}
	claims, err := conn.Claims.ListAll(ctx)
	_ = conn.Close()
	if err != nil {
		return MetricSet{}, nil, fmt.Errorf("list claims: %w", err)
	}
	byID := make(map[string]domain.Claim, len(claims))
	var live []string
	for _, c := range claims {
		byID[c.ID] = c
		if c.IsCurrent() && c.Status != domain.ClaimStatusDeprecated {
			live = append(live, c.Text)
		}
	}

	mem, err := mnemos.New(mnemos.WithStorage(dsn), mnemos.WithPassiveMode(), mnemos.WithActor(SeedActor))
	if err != nil {
		return MetricSet{}, nil, fmt.Errorf("open recall handle: %w", err)
	}
	surfaced, err := askRetractionQueries(ctx, mnemosRetriever{mem: mem}, r)
	_ = mem.Close()
	if err != nil {
		return MetricSet{}, nil, err
	}

	var withdrawn, rescored []string
	if outcome != nil {
		if withdrawn, err = claimTexts(byID, outcome.Withdrawn); err != nil {
			return MetricSet{}, nil, err
		}
		if rescored, err = claimTexts(byID, outcome.Rescored); err != nil {
			return MetricSet{}, nil, err
		}
	}
	items := make([]ItemState, len(r.Gold))
	for i, g := range r.Gold {
		items[i] = ItemState{
			Surviving: anyContains(live, g.Text),
			Surfaced:  anyContains(surfaced, g.Text),
			Withdrawn: anyContains(withdrawn, g.Text),
			Rescored:  anyContains(rescored, g.Text),
		}
	}
	ms := retractionMetrics(r, items)
	if outcome == nil {
		ms.Metrics = append(ms.Metrics, unretractedImpact()...)
	} else {
		ms.Metrics = append(ms.Metrics, impactMetrics(r, withdrawn)...)
		ms.Metrics = append(ms.Metrics, KnownMetric(MetricImpactRescored, Descriptive, float64(len(rescored)), 0, "count"))
	}
	return ms, items, nil
}

// measureRetractionBaseline scores raw retrieval after removing the retracted
// document's chunks. Copies and conclusions in other documents remain.
func measureRetractionBaseline(ctx context.Context, r *Retraction, events []domain.Event, eventID string, recency bool) (MetricSet, []ItemState, error) {
	var kept []domain.Event
	var removed []string
	for _, ev := range events {
		if ev.ID == eventID {
			removed = splitSentences(ev.Content)
			continue
		}
		kept = append(kept, ev)
	}
	if removed == nil {
		return MetricSet{}, nil, fmt.Errorf("retracted event %s is not in the baseline corpus", eventID)
	}
	rr := newRawRetriever(kept, recency)
	held := make([]string, len(rr.chunks))
	for i, c := range rr.chunks {
		held[i] = c.text
	}
	surfaced, err := askRetractionQueries(ctx, rr, r)
	if err != nil {
		return MetricSet{}, nil, err
	}
	items := make([]ItemState, len(r.Gold))
	for i, g := range r.Gold {
		items[i] = ItemState{
			Surviving: anyContains(held, g.Text),
			Surfaced:  anyContains(surfaced, g.Text),
			Withdrawn: anyContains(removed, g.Text),
		}
	}
	ms := retractionMetrics(r, items)
	ms.Metrics = append(ms.Metrics, impactMetrics(r, removed)...)
	ms.Metrics = append(ms.Metrics, UnknownMetric(MetricImpactRescored, Descriptive,
		"raw retrieval keeps no trust, so nothing can be rescored"))
	return ms, items, nil
}

// askRetractionQueries returns every text any retraction query surfaced.
func askRetractionQueries(ctx context.Context, rt retriever, r *Retraction) ([]string, error) {
	var out []string
	for i, q := range r.Queries {
		texts, err := rt.retrieve(ctx, question{ID: fmt.Sprintf("retraction-%d", i), Text: q, Limit: limitOr(r.Limit)})
		if err != nil {
			return nil, fmt.Errorf("retraction query %q: %w", q, err)
		}
		out = append(out, texts...)
	}
	return out, nil
}

// retractionMetrics computes the survival and surfacing rates, which every arm
// and every baseline has.
func retractionMetrics(r *Retraction, items []ItemState) MetricSet {
	var dep, depSurv, depSurf, sup, supSurv, supSurf int
	for i, g := range r.Gold {
		if isDependentRole(g.Role) {
			dep++
			depSurv += b2i(items[i].Surviving)
			depSurf += b2i(items[i].Surfaced)
			continue
		}
		sup++
		supSurv += b2i(items[i].Surviving)
		supSurf += b2i(items[i].Surfaced)
	}
	return MetricSet{Metrics: []Metric{
		KnownMetric(MetricUnsupportedSurviving, LowerBetter, ratio(depSurv, dep), dep, "ratio"),
		KnownMetric(MetricUnsupportedSurfaced, LowerBetter, ratio(depSurf, dep), dep, "ratio"),
		KnownMetric(MetricSupportedSurviving, HigherBetter, ratio(supSurv, sup), sup, "ratio"),
		KnownMetric(MetricSupportedSurfaced, HigherBetter, ratio(supSurf, sup), sup, "ratio"),
	}}
}

// impactMetrics scores what a retraction withdrew against the dependent gold.
// Precision is per withdrawn item (is it a dependent?), recall per dependent
// (was it withdrawn?). Nothing withdrawn leaves precision unknown, not 1.
func impactMetrics(r *Retraction, withdrawn []string) []Metric {
	var dependents []string
	for _, g := range r.Gold {
		if isDependentRole(g.Role) {
			dependents = append(dependents, g.Text)
		}
	}
	correct := 0
	for _, w := range withdrawn {
		for _, d := range dependents {
			if containsFold(w, d) {
				correct++
				break
			}
		}
	}
	found := 0
	for _, d := range dependents {
		if anyContains(withdrawn, d) {
			found++
		}
	}
	precision := UnknownMetric(MetricImpactPrecision, HigherBetter, "the retraction withdrew nothing, so precision is undefined")
	if len(withdrawn) > 0 {
		precision = KnownMetric(MetricImpactPrecision, HigherBetter, ratio(correct, len(withdrawn)), len(withdrawn), "ratio")
	}
	return []Metric{
		precision,
		KnownMetric(MetricImpactRecall, HigherBetter, ratio(found, len(dependents)), len(dependents), "ratio"),
		KnownMetric(MetricImpactWithdrawn, Descriptive, float64(len(withdrawn)), 0, "count"),
	}
}

// unretractedImpact is the impact block of the reference arm: no retraction
// ran, so there is no report to score.
func unretractedImpact() []Metric {
	const why = "no retraction on the reference arm, so there is no impact report"
	return []Metric{
		UnknownMetric(MetricImpactPrecision, HigherBetter, why),
		UnknownMetric(MetricImpactRecall, HigherBetter, why),
		UnknownMetric(MetricImpactWithdrawn, Descriptive, why),
		UnknownMetric(MetricImpactRescored, Descriptive, why),
	}
}

// judgeHypothesis applies the pass condition: against every baseline, fewer
// unsupported beliefs survive AND no fewer supported ones do. The second half
// is what stops a retraction that deletes everything from passing.
func judgeHypothesis(res *RetractionResult) Hypothesis {
	h := Hypothesis{
		Statement: "retracting the source leaves fewer unsupported beliefs surviving than raw retrieval " +
			"with the document's chunks removed, without losing more supported beliefs",
		Verdict: HypothesisSupported,
	}
	if !res.Validity.Deterministic {
		h.Verdict = HypothesisUnknown
		h.Reasons = append(h.Reasons, "the two retracted arms disagreed; nothing is attributable")
		return h
	}
	mU, okU := res.Retracted.Get(MetricUnsupportedSurviving)
	mS, okS := res.Retracted.Get(MetricSupportedSurviving)
	for _, b := range res.Baselines {
		bU, okBU := b.Metrics.Get(MetricUnsupportedSurviving)
		bS, okBS := b.Metrics.Get(MetricSupportedSurviving)
		if !okU || !okS || !okBU || !okBS || !mU.Known || !mS.Known || !bU.Known || !bS.Known {
			h.Verdict = HypothesisUnknown
			h.Reasons = append(h.Reasons, b.Name+": a survival metric was not measured")
			continue
		}
		if mU.Value >= bU.Value-deltaEpsilon {
			if h.Verdict == HypothesisSupported {
				h.Verdict = HypothesisNotSupported
			}
			h.Reasons = append(h.Reasons, fmt.Sprintf("%s: unsupported beliefs surviving %.4f, baseline %.4f (not fewer)",
				b.Name, mU.Value, bU.Value))
		} else {
			h.Reasons = append(h.Reasons, fmt.Sprintf("%s: unsupported beliefs surviving %.4f, baseline %.4f (fewer)",
				b.Name, mU.Value, bU.Value))
		}
		if mS.Value < bS.Value-deltaEpsilon {
			if h.Verdict == HypothesisSupported {
				h.Verdict = HypothesisNotSupported
			}
			h.Reasons = append(h.Reasons, fmt.Sprintf("%s: supported beliefs surviving %.4f, baseline %.4f (knowledge lost)",
				b.Name, mS.Value, bS.Value))
		}
	}
	return h
}

// claimTexts resolves reported claim ids to their texts. A reported id that is
// not in the store is an error: dropping it would shrink the withdrawn set and
// flatter precision.
func claimTexts(byID map[string]domain.Claim, ids []string) ([]string, error) {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		c, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("the retraction reported claim %s, which is not in the store", id)
		}
		out = append(out, c.Text)
	}
	return out, nil
}

func anyContains(texts []string, needle string) bool {
	for _, t := range texts {
		if containsFold(t, needle) {
			return true
		}
	}
	return false
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
