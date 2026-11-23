package brainbench

// The shifting-world track (ADR 0049, issue #446).
//
// Hypothesis: after the world changes, beliefs that surprise-triggered revision
// acts on are corrected faster than with the current prediction-error loop,
// and without disturbing the beliefs that stayed true.
//
// The track replays the same agent episode on several copies of the warmed
// seed. Each round, a day apart on the simulated clock:
//
//  1. new documents arrive (the world's evidence, which may lag the change);
//  2. the agent recalls, and takes each scheduled decision only if every
//     belief it rests on came back from that recall. An agent that no longer
//     recalls the stale picture does not act on it, so a corrected arm stops
//     generating surprises on its own: the loop is closed by recall, not by
//     the schedule;
//  3. the observed outcome arrives and, in the arms that record predictions,
//     is reconciled;
//  4. the arm sleeps (one consolidation pass, the same options on every arm);
//  5. every probe is asked, through the same recall the A/B track uses.
//
// The arms differ only in what the agent can record about its prediction:
//
//   - evidence_only: decisions are recorded with their beliefs, no prediction.
//     The brain learns from documents alone.
//   - current_loop: the prediction is recorded the only way the API offered
//     before ADR 0049: one expectation on EACH belief the decision rested on,
//     observed and reconciled. The sleep pass's ForgetRefuted then archives
//     each refuted belief. (Attaching a failed outcome to the decision does
//     the same: autoedge refutes every belief.) This is the fastest use of the
//     old loop, so it is the honest control for a speed claim.
//   - revision: the prediction is recorded on the decision, resting on its
//     beliefs; reconciliation attributes the surprise and revises
//     (internal/revision).
//
// Two noise arms replay current_loop and revision a second time. Each must
// reproduce its twin exactly, or the track is reported non-deterministic and
// the hypothesis unknown.

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.klarlabs.de/mnemos"
	"go.klarlabs.de/mnemos/internal/clock"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/extract"
	"go.klarlabs.de/mnemos/internal/relate"
	"go.klarlabs.de/mnemos/internal/store"
)

// Shifting-world arms.
const (
	ShiftArmEvidenceOnly = "evidence_only"
	ShiftArmCurrentLoop  = "current_loop"
	ShiftArmRevision     = "revision"
)

// Shifting-world probe roles.
const (
	// ShiftRoleChanged marks a probe whose answer the world changed. Its
	// expect is the new state, its must_not the stale one.
	ShiftRoleChanged = "changed"
	// ShiftRoleStable marks a probe whose answer stayed true. Its expect must
	// survive every round: the anti-gaming half of the track.
	ShiftRoleStable = "stable"
)

// Shifting-world metric names.
const (
	MetricShiftChangedAccuracy  = "changed_accuracy"
	MetricShiftChangedCorrected = "changed_corrected"
	MetricShiftCorrectionLag    = "changed_correction_lag"
	MetricShiftStableAccuracy   = "stable_accuracy"
	MetricShiftStableDisturbed  = "stable_disturbed"
	MetricShiftDecisions        = "decisions_taken"
	MetricShiftSurprises        = "surprises"
	MetricShiftRevisions        = "revisions"
	MetricShiftArchived         = "archived_by_sleep"
)

// shiftRoundGap is the simulated time between rounds.
const shiftRoundGap = 24 * time.Hour

// ShiftingWorld declares the track.
type ShiftingWorld struct {
	Rounds []ShiftRound `yaml:"rounds"`
	Probes []ShiftProbe `yaml:"probes"`
	// Sleep is the consolidation pass every arm runs at the end of every
	// round. The same on every arm, so it is not the treatment.
	Sleep Process `yaml:"sleep"`
	// RecallLimit is the top-k of the agent's recall before a decision; 0
	// uses ProbeDefaultLimit.
	RecallLimit int `yaml:"recall_limit"`
}

// ShiftRound is one day of the episode.
type ShiftRound struct {
	ID        string          `yaml:"id"`
	Docs      []Doc           `yaml:"docs"`
	Decisions []ShiftDecision `yaml:"decisions"`
}

// ShiftDecision is one decision the agent takes if it recalls its premises.
type ShiftDecision struct {
	ID string `yaml:"id"`
	// Recall is the query the agent asks before deciding.
	Recall string `yaml:"recall"`
	// RestsOn are claim-text substrings; each must match exactly one claim.
	RestsOn   []string `yaml:"rests_on"`
	Predicted float64  `yaml:"predicted"`
	Tolerance float64  `yaml:"tolerance"`
	// Observed is the outcome the world produces.
	Observed float64 `yaml:"observed"`
}

// ShiftProbe is one question asked after every round.
type ShiftProbe struct {
	ID      string   `yaml:"id"`
	Query   string   `yaml:"query"`
	Expect  string   `yaml:"expect"`
	MustNot []string `yaml:"must_not"`
	Limit   int      `yaml:"limit"`
	Role    string   `yaml:"role"`
	// EvidenceIn names the round whose documents first carry the new state,
	// for a changed probe. Correction lag is counted from it.
	EvidenceIn string `yaml:"evidence_in"`
}

// ShiftRoundOutcome is one arm's round.
type ShiftRoundOutcome struct {
	Round string `json:"round"`
	// Correct is the strict verdict per probe id: expect returned and no
	// must_not returned.
	Correct   map[string]bool `json:"correct"`
	Taken     []string        `json:"decisions_taken"`
	Skipped   []string        `json:"decisions_skipped"`
	Surprised int             `json:"surprised"`
	// Revisions are what surprise-triggered revision did, as kind:text.
	Revisions []string `json:"revisions"`
	// Archived counts beliefs the sleep pass archived (ForgetRefuted and
	// ForgetBelowTrust): how the current loop acts on a refutation.
	Archived int `json:"archived"`
}

// ShiftBeliefState is where a stable probe's belief ended.
type ShiftBeliefState struct {
	Probe   string `json:"probe"`
	Text    string `json:"text"`
	Current bool   `json:"current"`
	Status  string `json:"status"`
}

// ShiftArm is one arm's record.
type ShiftArm struct {
	Name    string              `json:"name"`
	Rounds  []ShiftRoundOutcome `json:"rounds"`
	Stable  []ShiftBeliefState  `json:"stable_beliefs"`
	Metrics MetricSet           `json:"metrics"`
}

// ShiftingResult is the track's report.
type ShiftingResult struct {
	Validity Validity   `json:"validity"`
	Arms     []ShiftArm `json:"arms"`
	// VsCurrentLoop compares revision (treatment) with current_loop
	// (control); it is the comparison the hypothesis is judged on and the one
	// that counts toward -fail-on-regression.
	VsCurrentLoop []Comparison `json:"vs_current_loop"`
	Summary       Summary      `json:"summary"`
	// VsEvidenceOnly compares revision with evidence_only. Reported, never
	// gated: it answers whether recording predictions helps at all.
	VsEvidenceOnly []Comparison `json:"vs_evidence_only"`
	Hypothesis     Hypothesis   `json:"hypothesis"`
}

// validateShifting checks the track's declaration.
func (s Scenario) validateShifting() error {
	w := s.ShiftingWorld
	if w == nil {
		return nil
	}
	if len(w.Rounds) == 0 {
		return fmt.Errorf("scenario %s: shifting_world needs at least one round", s.ID)
	}
	if w.RecallLimit < 0 {
		return fmt.Errorf("scenario %s: shifting_world.recall_limit must not be negative", s.ID)
	}
	docs := map[string]bool{}
	for _, d := range s.Corpus {
		docs[d.ID] = true
	}
	rounds := map[string]bool{}
	decisions := map[string]bool{}
	for i, r := range w.Rounds {
		if strings.TrimSpace(r.ID) == "" || rounds[r.ID] {
			return fmt.Errorf("scenario %s: shifting_world.rounds[%d]: id missing or duplicated", s.ID, i)
		}
		rounds[r.ID] = true
		for _, d := range r.Docs {
			if strings.TrimSpace(d.ID) == "" || docs[d.ID] || strings.TrimSpace(d.Text) == "" {
				return fmt.Errorf("scenario %s: round %s: doc %q needs a unique id and text", s.ID, r.ID, d.ID)
			}
			docs[d.ID] = true
		}
		for _, d := range r.Decisions {
			if strings.TrimSpace(d.ID) == "" || decisions[d.ID+"@"+r.ID] {
				return fmt.Errorf("scenario %s: round %s: decision %q needs an id unique in its round", s.ID, r.ID, d.ID)
			}
			decisions[d.ID+"@"+r.ID] = true
			if strings.TrimSpace(d.Recall) == "" || len(d.RestsOn) == 0 {
				return fmt.Errorf("scenario %s: round %s: decision %s needs recall and rests_on", s.ID, r.ID, d.ID)
			}
		}
	}
	if len(w.Probes) == 0 {
		return fmt.Errorf("scenario %s: shifting_world needs probes", s.ID)
	}
	probes := map[string]bool{}
	changed := 0
	for _, p := range w.Probes {
		if strings.TrimSpace(p.ID) == "" || probes[p.ID] || strings.TrimSpace(p.Query) == "" || strings.TrimSpace(p.Expect) == "" {
			return fmt.Errorf("scenario %s: shifting probe %q needs a unique id, query and expect", s.ID, p.ID)
		}
		probes[p.ID] = true
		switch p.Role {
		case ShiftRoleChanged:
			changed++
			if !rounds[p.EvidenceIn] {
				return fmt.Errorf("scenario %s: changed probe %s: evidence_in %q is not a round", s.ID, p.ID, p.EvidenceIn)
			}
			if len(p.MustNot) == 0 {
				return fmt.Errorf("scenario %s: changed probe %s: must_not names the stale state", s.ID, p.ID)
			}
		case ShiftRoleStable:
			if p.EvidenceIn != "" {
				return fmt.Errorf("scenario %s: stable probe %s: evidence_in is for changed probes", s.ID, p.ID)
			}
		default:
			return fmt.Errorf("scenario %s: shifting probe %s: role must be changed or stable", s.ID, p.ID)
		}
	}
	if changed == 0 {
		return fmt.Errorf("scenario %s: shifting_world needs at least one changed probe", s.ID)
	}
	return nil
}

// runShiftingTrack runs every arm from the warmed seed on one restarted
// simulated clock each, so no arm runs later than another.
func runShiftingTrack(ctx context.Context, sc Scenario, dir, seedPath string, sim *clock.Simulated) (*ShiftingResult, error) {
	start := sim.Now()
	arms := []struct{ name, kind string }{
		{ShiftArmEvidenceOnly, ShiftArmEvidenceOnly},
		{ShiftArmCurrentLoop, ShiftArmCurrentLoop},
		{ShiftArmRevision, ShiftArmRevision},
		{"noise_" + ShiftArmCurrentLoop, ShiftArmCurrentLoop},
		{"noise_" + ShiftArmRevision, ShiftArmRevision},
	}
	byName := map[string]ShiftArm{}
	res := &ShiftingResult{}
	for _, a := range arms {
		path := filepath.Join(dir, "shifting_"+a.name+".db")
		if err := copyDB(seedPath, path); err != nil {
			return nil, err
		}
		sim.Restart(start)
		arm, err := runShiftArm(ctx, sc, "sqlite://"+path, a.kind, sim)
		if err != nil {
			return nil, fmt.Errorf("brainbench: shifting arm %s: %w", a.name, err)
		}
		arm.Name = a.name
		byName[a.name] = arm
		if !strings.HasPrefix(a.name, "noise_") {
			res.Arms = append(res.Arms, arm)
		}
	}
	w := sc.ShiftingWorld
	drift := append(
		prefixed("current_loop:", driftingMetrics(byName[ShiftArmCurrentLoop].Metrics, byName["noise_"+ShiftArmCurrentLoop].Metrics)),
		prefixed("revision:", driftingMetrics(byName[ShiftArmRevision].Metrics, byName["noise_"+ShiftArmRevision].Metrics))...)
	res.Validity = Validity{
		Deterministic:           len(drift) == 0,
		NondeterministicMetrics: drift,
		ProbeCount:              len(w.Probes) * len(w.Rounds),
		MinResolvableRateDelta:  1 / float64(max(len(w.Probes)*len(w.Rounds), 1)),
	}
	res.VsCurrentLoop = CompareSets(byName[ShiftArmCurrentLoop].Metrics, byName[ShiftArmRevision].Metrics)
	res.Summary = Summarize(res.VsCurrentLoop)
	res.VsEvidenceOnly = CompareSets(byName[ShiftArmEvidenceOnly].Metrics, byName[ShiftArmRevision].Metrics)
	res.Hypothesis = judgeShifting(res)
	return res, nil
}

func prefixed(prefix string, names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = prefix + n
	}
	return out
}

// runShiftArm replays the episode on one arm.
func runShiftArm(ctx context.Context, sc Scenario, dsn, kind string, sim *clock.Simulated) (ShiftArm, error) {
	w := sc.ShiftingWorld
	arm := ShiftArm{}
	for _, round := range w.Rounds {
		sim.Advance(shiftRoundGap)
		claims, err := ingestRound(ctx, dsn, sc.ID, round)
		if err != nil {
			return arm, err
		}
		out, err := playRound(ctx, dsn, sc.ID, kind, w, round, claims)
		if err != nil {
			return arm, err
		}
		arm.Rounds = append(arm.Rounds, out)
	}
	stable, err := stableStates(ctx, dsn, w)
	if err != nil {
		return arm, err
	}
	arm.Stable = stable
	arm.Metrics = shiftMetrics(w, arm)
	return arm, nil
}

// ingestRound ingests the round's documents through the seed's ingest path
// and returns every claim afterwards, for resolving rests_on texts.
func ingestRound(ctx context.Context, dsn, scenarioID string, round ShiftRound) ([]domain.Claim, error) {
	conn, err := store.Open(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("open arm store: %w", err)
	}
	defer func() { _ = conn.Close() }()
	s := seeder{conn: conn, scenario: scenarioID, engine: extract.NewEngine(), relator: relate.NewEngine()}
	for _, d := range round.Docs {
		now := clock.Now().UTC()
		if err := s.ingest(ctx, d.ID, d.Source, d.Text, now.AddDate(0, 0, -d.AgeDays), now); err != nil {
			return nil, err
		}
	}
	claims, err := conn.Claims.ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("list claims: %w", err)
	}
	return claims, nil
}

// playRound takes the round's decisions, reconciles, sleeps and probes.
func playRound(ctx context.Context, dsn, scenarioID, kind string, w *ShiftingWorld, round ShiftRound, claims []domain.Claim) (ShiftRoundOutcome, error) {
	out := ShiftRoundOutcome{Round: round.ID, Correct: map[string]bool{}}
	mem, err := mnemos.New(mnemos.WithStorage(dsn), mnemos.WithPassiveMode(), mnemos.WithActor(SeedActor))
	if err != nil {
		return out, fmt.Errorf("open arm handle: %w", err)
	}
	defer func() { _ = mem.Close() }()

	for _, d := range round.Decisions {
		ids, err := resolveRestsOn(claims, d.RestsOn)
		if err != nil {
			return out, fmt.Errorf("round %s decision %s: %w", round.ID, d.ID, err)
		}
		recalled, err := mem.Recall(ctx, mnemos.Query{Text: d.Recall, Limit: limitOr(w.RecallLimit), AgentConsumer: true})
		if err != nil {
			return out, fmt.Errorf("round %s decision %s: recall: %w", round.ID, d.ID, err)
		}
		if !allRecalled(ids, recalled) {
			out.Skipped = append(out.Skipped, d.ID)
			continue
		}
		if err := recordDecision(ctx, mem, kind, scenarioID, round.ID, d, ids); err != nil {
			return out, fmt.Errorf("round %s decision %s: %w", round.ID, d.ID, err)
		}
		out.Taken = append(out.Taken, d.ID)
		// The world's surprise, the same on every arm that took the
		// decision, whether or not the arm can record it.
		if math.Abs(d.Predicted-d.Observed) > math.Abs(d.Tolerance) {
			out.Surprised++
		}
	}
	if kind != ShiftArmEvidenceOnly {
		recs, err := mem.ReconcileExpectations(ctx)
		if err != nil {
			return out, fmt.Errorf("round %s: reconcile: %w", round.ID, err)
		}
		// One step can be reported under several reconciliations (every
		// surprised decision that rested on the belief); count it once.
		seen := map[string]bool{}
		for _, r := range recs {
			for _, rv := range r.Revisions {
				key := rv.Kind + ":" + claimText(claims, rv.ClaimID)
				if !seen[key] {
					seen[key] = true
					out.Revisions = append(out.Revisions, key)
				}
			}
		}
		sort.Strings(out.Revisions)
	}
	slept, err := mem.Consolidate(ctx, consolidateOptions(w.Sleep))
	if err != nil {
		return out, fmt.Errorf("round %s: sleep: %w", round.ID, err)
	}
	out.Archived = slept.Refuted + slept.Forgotten
	for _, p := range w.Probes {
		res, err := mem.Recall(ctx, mnemos.Query{Text: p.Query, Limit: limitOr(p.Limit)})
		if err != nil {
			return out, fmt.Errorf("round %s probe %s: %w", round.ID, p.ID, err)
		}
		texts := make([]string, len(res))
		for i, r := range res {
			texts[i] = r.Text
		}
		out.Correct[p.ID] = correct(texts, question{Expect: p.Expect, MustNot: p.MustNot})
	}
	return out, nil
}

// recordDecision records one decision the way the arm's API allows.
func recordDecision(ctx context.Context, mem mnemos.Memory, kind, scenarioID, roundID string, d ShiftDecision, beliefs []string) error {
	dec := mnemos.Decision{
		ID:        fmt.Sprintf("dec_%s_%s_%s", scenarioID, roundID, d.ID),
		Statement: d.ID, Beliefs: beliefs, RiskLevel: "medium",
	}
	switch kind {
	case ShiftArmEvidenceOnly:
		_, err := mem.RecordDecision(ctx, dec)
		return err
	case ShiftArmCurrentLoop:
		if _, err := mem.RecordDecision(ctx, dec); err != nil {
			return err
		}
		for _, b := range beliefs {
			if err := mem.Expect(ctx, b, d.Predicted, d.Tolerance, time.Time{}); err != nil {
				return err
			}
			if err := mem.RecordObservation(ctx, b, d.Observed); err != nil {
				return err
			}
		}
		return nil
	case ShiftArmRevision:
		dec.Expectation = &mnemos.ExpectedOutcome{Predicted: d.Predicted, Tolerance: d.Tolerance}
		id, err := mem.RecordDecision(ctx, dec)
		if err != nil {
			return err
		}
		return mem.RecordDecisionObservation(ctx, id, d.Observed)
	default:
		return fmt.Errorf("unknown arm kind %q", kind)
	}
}

// resolveRestsOn maps each rests_on text to the one claim containing it.
func resolveRestsOn(claims []domain.Claim, texts []string) ([]string, error) {
	out := make([]string, 0, len(texts))
	for _, t := range texts {
		var hits []string
		for _, c := range claims {
			if containsFold(c.Text, t) {
				hits = append(hits, c.ID)
			}
		}
		if len(hits) != 1 {
			return nil, fmt.Errorf("rests_on %q matches %d claims, want exactly 1", t, len(hits))
		}
		out = append(out, hits[0])
	}
	return out, nil
}

func allRecalled(ids []string, recalled []mnemos.Result) bool {
	got := make(map[string]bool, len(recalled))
	for _, r := range recalled {
		got[r.ClaimID] = true
	}
	for _, id := range ids {
		if !got[id] {
			return false
		}
	}
	return true
}

func claimText(claims []domain.Claim, id string) string {
	for _, c := range claims {
		if c.ID == id {
			return c.Text
		}
	}
	return id
}

// consolidateOptions maps a scenario's Process onto the library options: the
// A/B treatment and the shifting-world sleep run the same mapping.
func consolidateOptions(p Process) mnemos.ConsolidateOptions {
	return mnemos.ConsolidateOptions{
		DedupeThreshold:    p.DedupeThreshold,
		ForgetBelowTrust:   p.ForgetBelowTrust,
		ForgetRefuted:      p.ForgetRefuted,
		Synthesize:         p.Synthesize,
		ReinforceValidated: p.ReinforceValidated,
		ReinforcePlaybooks: p.ReinforcePlaybooks,
		AssignCredit:       p.AssignCredit,
		AssignSalience:     p.AssignSalience,
		ReplayTopK:         p.ReplayTopK,
		Plastic:            p.Plastic,
		DecayAssociations:  p.DecayAssociations,
		DecayInhibition:    p.DecayInhibition,
	}
}

// stableStates reads where each stable probe's belief ended.
func stableStates(ctx context.Context, dsn string, w *ShiftingWorld) ([]ShiftBeliefState, error) {
	conn, err := store.Open(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("open arm store: %w", err)
	}
	defer func() { _ = conn.Close() }()
	claims, err := conn.Claims.ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("list claims: %w", err)
	}
	var out []ShiftBeliefState
	for _, p := range w.Probes {
		if p.Role != ShiftRoleStable {
			continue
		}
		st := ShiftBeliefState{Probe: p.ID, Text: p.Expect, Status: "missing"}
		for _, c := range claims {
			if containsFold(c.Text, p.Expect) {
				st.Text, st.Current, st.Status = c.Text, c.IsCurrent(), string(c.Status)
				if st.Current {
					break
				}
			}
		}
		out = append(out, st)
	}
	return out, nil
}

// shiftMetrics scores one arm.
func shiftMetrics(w *ShiftingWorld, arm ShiftArm) MetricSet {
	roundIndex := map[string]int{}
	for i, r := range w.Rounds {
		roundIndex[r.ID] = i
	}
	var chN, chOK, stN, stOK, corrected, taken, surprised, revisions, archived int
	var lagSum float64
	var never []string
	for _, p := range w.Probes {
		settled := -1 // first round from which the probe stays correct
		for i, r := range arm.Rounds {
			ok := r.Correct[p.ID]
			if p.Role == ShiftRoleChanged {
				chN++
				chOK += b2i(ok)
			} else {
				stN++
				stOK += b2i(ok)
			}
			switch {
			case ok && settled < 0:
				settled = i
			case !ok:
				settled = -1
			}
		}
		if p.Role != ShiftRoleChanged {
			continue
		}
		if settled < 0 {
			never = append(never, p.ID)
			continue
		}
		corrected++
		lagSum += float64(settled - roundIndex[p.EvidenceIn])
	}
	for _, r := range arm.Rounds {
		taken += len(r.Taken)
		surprised += r.Surprised
		revisions += len(r.Revisions)
		archived += r.Archived
	}
	changedProbes := chN / max(len(w.Rounds), 1)
	disturbed := 0
	for _, s := range arm.Stable {
		if !s.Current || s.Status != string(domain.ClaimStatusActive) {
			disturbed++
		}
	}
	lag := UnknownMetric(MetricShiftCorrectionLag, LowerBetter,
		"changed probe(s) never settled on the new state within the episode: "+strings.Join(never, ", "))
	if len(never) == 0 {
		lag = KnownMetric(MetricShiftCorrectionLag, LowerBetter, lagSum/float64(max(corrected, 1)), corrected, "rounds")
	}
	ms := MetricSet{Metrics: []Metric{
		KnownMetric(MetricShiftChangedAccuracy, HigherBetter, ratio(chOK, chN), chN, "ratio"),
		KnownMetric(MetricShiftChangedCorrected, HigherBetter, ratio(corrected, changedProbes), changedProbes, "ratio"),
		lag,
		KnownMetric(MetricShiftStableAccuracy, HigherBetter, ratio(stOK, stN), stN, "ratio"),
		KnownMetric(MetricShiftStableDisturbed, LowerBetter, ratio(disturbed, len(arm.Stable)), len(arm.Stable), "ratio"),
		KnownMetric(MetricShiftDecisions, Descriptive, float64(taken), 0, "count"),
		KnownMetric(MetricShiftSurprises, Descriptive, float64(surprised), 0, "count"),
		KnownMetric(MetricShiftRevisions, Descriptive, float64(revisions), 0, "count"),
		KnownMetric(MetricShiftArchived, Descriptive, float64(archived), 0, "count"),
	}}
	if stN == 0 {
		ms.Metrics[3] = UnknownMetric(MetricShiftStableAccuracy, HigherBetter, "no stable probe declared")
		ms.Metrics[4] = UnknownMetric(MetricShiftStableDisturbed, LowerBetter, "no stable probe declared")
	}
	return ms
}

// writeShifting renders the track for the human report.
func writeShifting(p func(string, ...any), sw *ShiftingResult) {
	p("  SHIFTING WORLD (ADR 0049) — %d arms, %d probe answers per arm\n", len(sw.Arms), sw.Validity.ProbeCount)
	if sw.Validity.Deterministic {
		p("    measurement deterministic: yes (both noise arms reproduced their twins)\n")
	} else {
		p("    measurement deterministic: NO — %s\n", strings.Join(sw.Validity.NondeterministicMetrics, ", "))
	}
	p("\n    %-24s", "metric")
	for _, a := range sw.Arms {
		p(" %14s", a.Name)
	}
	p("\n")
	if len(sw.Arms) > 0 {
		for _, m := range sw.Arms[0].Metrics.Metrics {
			p("    %-24s", m.Name)
			for _, a := range sw.Arms {
				got, _ := a.Metrics.Get(m.Name)
				p(" %14s", fmtMetric(got))
			}
			p("\n")
		}
	}
	p("\n    PER ROUND (probes answered correctly; decisions taken / skipped; revisions)\n")
	for _, a := range sw.Arms {
		for _, r := range a.Rounds {
			var ok []string
			for id, c := range r.Correct {
				if c {
					ok = append(ok, id)
				}
			}
			sort.Strings(ok)
			p("    %-14s %-4s correct=[%s] taken=%d skipped=%d archived=%d\n",
				a.Name, r.Round, strings.Join(ok, " "), len(r.Taken), len(r.Skipped), r.Archived)
			for _, rv := range r.Revisions {
				p("    %-14s %-4s   %s\n", "", "", rv)
			}
		}
	}
	p("\n    REVISION vs CURRENT LOOP\n")
	for _, c := range sw.VsCurrentLoop {
		delta := "—"
		if c.Verdict != VerdictUnknown {
			delta = fmt.Sprintf("%+.4f", c.Delta)
		}
		p("    %-24s %10s %10s %10s  %s%s\n", c.Name, fmtMetric(c.Control), fmtMetric(c.Treatment),
			delta, verdictMark(c.Verdict), verdictLabel(c))
	}
	p("\n    HYPOTHESIS: %s — %s\n", sw.Hypothesis.Verdict, sw.Hypothesis.Statement)
	for _, reason := range sw.Hypothesis.Reasons {
		p("      - %s\n", reason)
	}
	p("\n")
}

// judgeShifting applies the pass condition, fixed before any result was seen:
// revision corrects changed beliefs with a strictly smaller lag than
// current_loop, with changed accuracy and both stable metrics no worse.
func judgeShifting(res *ShiftingResult) Hypothesis {
	h := Hypothesis{Statement: "after the world changes, beliefs surprise revises are corrected faster than " +
		"with the current loop (lower changed_correction_lag), with changed_accuracy, stable_accuracy and " +
		"stable_disturbed no worse"}
	if !res.Validity.Deterministic {
		h.Verdict = HypothesisUnknown
		h.Reasons = []string{"noise arms disagreed: " + strings.Join(res.Validity.NondeterministicMetrics, ", ")}
		return h
	}
	by := map[string]Comparison{}
	for _, c := range res.VsCurrentLoop {
		by[c.Name] = c
	}
	lag := by[MetricShiftCorrectionLag]
	if lag.Verdict == VerdictUnknown {
		h.Verdict = HypothesisUnknown
		h.Reasons = []string{"correction lag unmeasurable: " + strings.TrimSpace(lag.Control.Unknown+" "+lag.Treatment.Unknown)}
		return h
	}
	h.Verdict = HypothesisSupported
	if lag.Verdict != VerdictBetter {
		h.Verdict = HypothesisNotSupported
		h.Reasons = append(h.Reasons, fmt.Sprintf("correction lag %s: current_loop %s, revision %s",
			lag.Verdict, fmtMetric(lag.Control), fmtMetric(lag.Treatment)))
	}
	for _, name := range []string{MetricShiftChangedAccuracy, MetricShiftStableAccuracy, MetricShiftStableDisturbed} {
		c := by[name]
		if c.Verdict == VerdictWorse || c.Verdict == VerdictUnknown {
			h.Verdict = HypothesisNotSupported
			h.Reasons = append(h.Reasons, fmt.Sprintf("%s %s: current_loop %s, revision %s",
				name, c.Verdict, fmtMetric(c.Control), fmtMetric(c.Treatment)))
		}
	}
	if h.Verdict == HypothesisSupported {
		h.Reasons = append(h.Reasons, fmt.Sprintf("correction lag %s → %s rounds", fmtMetric(lag.Control), fmtMetric(lag.Treatment)))
	}
	return h
}
