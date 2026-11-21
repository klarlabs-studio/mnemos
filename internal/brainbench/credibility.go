package brainbench

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"go.klarlabs.de/mnemos"
	"go.klarlabs.de/mnemos/internal/clock"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/store"
	"go.klarlabs.de/mnemos/internal/trust"
)

// Credibility-order track (ADR 0048).
//
// The hypothesis under test: credibility is earned by independent evidence and
// by consequences, not by repetition. A captured stream repeats what it already
// holds, session after session, and a later session that was shown a belief by
// recall says it again. Repetition makes a statement feel true whether or not
// it is (the illusory-truth effect, Hasher, Goldstein & Toppino 1977; Fazio et
// al. 2015), so a trust model that rewards it lets a stream that captured a
// falsehood out-trust the truth.
//
// The track runs on one arm of its own, a byte copy of the warmed seed:
//
//  1. Each declared outcome is recorded the way an agent records one through
//     the library facade: a decision that relied on the belief, an
//     expectation about it, and an observation that bears it out.
//  2. A consolidation pass with credit assignment only (ADR 0014) turns the
//     outcome into the belief's applied credit, the one way consequences reach
//     credibility.
//  3. Each declared pair is read back from the store: the belief the gold says
//     should be more credible must have the strictly higher stored trust.
//
// The gold is the author's declaration of which statement is true. Nothing in
// the product reads it. The pair is scored on the stored trust_score, which is
// what every reader of credibility reads (recall, the Context Block, forgetting).

// Credibility is a scenario's credibility-order experiment.
type Credibility struct {
	// Outcomes are consequences observed for beliefs: each is one validated
	// prediction (decision, expectation, matching observation).
	Outcomes []CredibilityOutcome `yaml:"outcomes"`
	// Order lists the pairs the trust model is judged on.
	Order []CredibilityOrder `yaml:"order"`
}

// CredibilityOutcome names the belief a validated outcome bears out. Belief is
// a case-insensitive substring matching exactly one current claim.
type CredibilityOutcome struct {
	Belief string `yaml:"belief"`
	Note   string `yaml:"note"`
}

// CredibilityOrder is one gold ordering: Higher must out-trust Lower. Both are
// case-insensitive substrings matching exactly one current claim.
type CredibilityOrder struct {
	Higher string `yaml:"higher"`
	Lower  string `yaml:"lower"`
	Note   string `yaml:"note"`
}

// CredibilityResult is the track's record.
type CredibilityResult struct {
	// TrustModel is the trust model version the stored scores were computed
	// under, so a report says which model it judged.
	TrustModel string `json:"trust_model"`
	// OutcomesApplied counts the validated outcomes recorded; Credited counts
	// the beliefs credit assignment then credited.
	OutcomesApplied int               `json:"outcomes_applied"`
	Credited        int               `json:"credited"`
	Pairs           []CredibilityPair `json:"pairs"`
	// Holds is true when every pair holds.
	Holds bool `json:"holds"`
}

// CredibilityPair is one judged ordering, with the inputs behind each score.
type CredibilityPair struct {
	Higher CredibilityBelief `json:"higher"`
	Lower  CredibilityBelief `json:"lower"`
	// Holds is true when Higher's stored trust is strictly greater.
	Holds bool   `json:"holds"`
	Note  string `json:"note,omitempty"`
}

// CredibilityBelief is one side of a pair as the store holds it.
type CredibilityBelief struct {
	Target string  `json:"target"`
	Text   string  `json:"text"`
	Trust  float64 `json:"trust"`
	// Episodes is the number of evidence episodes the belief has; Evidence
	// is the independence-graded count the trust model scored (TrustInput).
	Episodes int     `json:"episodes"`
	Evidence int     `json:"evidence_count"`
	Credit   float64 `json:"credit"`
}

// validateCredibility checks the credibility block's shape. Whether each
// target matches exactly one claim can only be known after seeding, and is
// checked then.
func (s Scenario) validateCredibility() error {
	c := s.Credibility
	if c == nil {
		return nil
	}
	if len(c.Order) == 0 {
		return fmt.Errorf("scenario %s: credibility needs at least one order pair", s.ID)
	}
	for i, o := range c.Outcomes {
		if strings.TrimSpace(o.Belief) == "" {
			return fmt.Errorf("scenario %s: credibility.outcomes[%d]: belief is required", s.ID, i)
		}
	}
	for i, o := range c.Order {
		if strings.TrimSpace(o.Higher) == "" || strings.TrimSpace(o.Lower) == "" {
			return fmt.Errorf("scenario %s: credibility.order[%d]: higher and lower are required", s.ID, i)
		}
		if strings.EqualFold(strings.TrimSpace(o.Higher), strings.TrimSpace(o.Lower)) {
			return fmt.Errorf("scenario %s: credibility.order[%d]: higher and lower name the same belief", s.ID, i)
		}
	}
	return nil
}

// runCredibilityTrack runs the track on its own copy of the seed.
func runCredibilityTrack(ctx context.Context, sc Scenario, dir, seedPath string) (*CredibilityResult, error) {
	armPath := filepath.Join(dir, "credibility.db")
	if err := copyDB(seedPath, armPath); err != nil {
		return nil, err
	}
	dsn := "sqlite://" + armPath
	res := &CredibilityResult{TrustModel: trust.ModelVersion}
	if err := applyOutcomes(ctx, dsn, sc.Credibility.Outcomes, res); err != nil {
		return nil, err
	}

	conn, err := store.Open(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("brainbench: open credibility arm: %w", err)
	}
	defer func() { _ = conn.Close() }()
	finder := seeder{conn: conn}
	res.Holds = true
	for _, o := range sc.Credibility.Order {
		hi, err := readCredibility(ctx, &finder, o.Higher)
		if err != nil {
			return nil, err
		}
		lo, err := readCredibility(ctx, &finder, o.Lower)
		if err != nil {
			return nil, err
		}
		pair := CredibilityPair{Higher: hi, Lower: lo, Holds: hi.Trust > lo.Trust, Note: o.Note}
		res.Holds = res.Holds && pair.Holds
		res.Pairs = append(res.Pairs, pair)
	}
	return res, nil
}

// applyOutcomes records each outcome through the library facade and runs
// credit assignment, the path an agent's recorded consequences take.
func applyOutcomes(ctx context.Context, dsn string, outcomes []CredibilityOutcome, res *CredibilityResult) error {
	if len(outcomes) == 0 {
		return nil
	}
	conn, err := store.Open(ctx, dsn)
	if err != nil {
		return fmt.Errorf("brainbench: open credibility arm: %w", err)
	}
	finder := seeder{conn: conn}
	ids := make([]string, 0, len(outcomes))
	for _, o := range outcomes {
		c, err := finder.findCurrent(ctx, o.Belief)
		if err != nil {
			_ = conn.Close()
			return fmt.Errorf("brainbench: credibility outcome: %w", err)
		}
		ids = append(ids, c.ID)
	}
	_ = conn.Close()

	mem, err := mnemos.New(mnemos.WithStorage(dsn), mnemos.WithPassiveMode(), mnemos.WithActor(SeedActor))
	if err != nil {
		return fmt.Errorf("brainbench: open credibility handle: %w", err)
	}
	defer func() { _ = mem.Close() }()
	horizon := clock.Now().UTC().Add(24 * time.Hour)
	for i, id := range ids {
		if _, err := mem.RecordDecision(ctx, mnemos.Decision{
			Statement: "acted on: " + outcomes[i].Belief,
			Beliefs:   []string{id},
		}); err != nil {
			return fmt.Errorf("brainbench: record decision on %s: %w", id, err)
		}
		if err := mem.Expect(ctx, id, 1, 0.5, horizon); err != nil {
			return fmt.Errorf("brainbench: expect %s: %w", id, err)
		}
		if err := mem.RecordObservation(ctx, id, 1); err != nil {
			return fmt.Errorf("brainbench: observe %s: %w", id, err)
		}
		res.OutcomesApplied++
	}
	out, err := mem.Consolidate(ctx, mnemos.ConsolidateOptions{AssignCredit: true})
	if err != nil {
		return fmt.Errorf("brainbench: credit assignment: %w", err)
	}
	res.Credited = out.Credited
	return nil
}

// writeCredibility renders one scenario's credibility-order track.
func writeCredibility(p func(string, ...any), cr *CredibilityResult) {
	p("  CREDIBILITY ORDER (%s; %d outcome(s) recorded, %d belief(s) credited)\n",
		cr.TrustModel, cr.OutcomesApplied, cr.Credited)
	for _, pair := range cr.Pairs {
		mark := "HOLDS"
		if !pair.Holds {
			mark = "VIOLATED"
		}
		p("    %-8s %q %.4f (episodes %d, evidence %d, credit %+.2f) > %q %.4f (episodes %d, evidence %d, credit %+.2f)\n",
			mark, pair.Higher.Target, pair.Higher.Trust, pair.Higher.Episodes, pair.Higher.Evidence, pair.Higher.Credit,
			pair.Lower.Target, pair.Lower.Trust, pair.Lower.Episodes, pair.Lower.Evidence, pair.Lower.Credit)
	}
	p("\n")
}

// readCredibility reads one belief's stored trust and the inputs behind it.
func readCredibility(ctx context.Context, finder *seeder, target string) (CredibilityBelief, error) {
	c, err := finder.findCurrent(ctx, target)
	if err != nil {
		return CredibilityBelief{}, fmt.Errorf("brainbench: credibility pair: %w", err)
	}
	b := CredibilityBelief{Target: target, Text: c.Text, Trust: c.TrustScore, Credit: domain.AppliedCredit(c.ConfidenceComponents)}
	links, err := finder.conn.Claims.ListEvidenceByClaimIDs(ctx, []string{c.ID})
	if err != nil {
		return CredibilityBelief{}, fmt.Errorf("brainbench: evidence of %s: %w", c.ID, err)
	}
	b.Episodes = len(links)
	if lister, ok := finder.conn.Claims.(ports.TrustInputLister); ok {
		inputs, err := lister.ListTrustInputs(ctx, []string{c.ID})
		if err != nil {
			return CredibilityBelief{}, fmt.Errorf("brainbench: trust inputs of %s: %w", c.ID, err)
		}
		b.Evidence = inputs[c.ID].EvidenceCount
	}
	return b, nil
}
