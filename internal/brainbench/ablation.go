package brainbench

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"go.klarlabs.de/mnemos/internal/clock"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/query"
	"go.klarlabs.de/mnemos/internal/store"
)

// Retrieval ablation.
//
// Hypothesis: what is useful to put in an agent's context differs from what is
// semantically similar to the question. Pass condition: Mnemos's ranking
// achieves higher task success than similarity alone at a fixed token budget.
//
// Task success, offline and without an LLM: the context assembled under the
// budget contains the gold supporting belief for the question and none of its
// gold-invalid ones (superseded, contradicted, retracted). The gold is the
// scenario's own: a probe's expect and must_not.
//
// Every Mnemos policy is a configuration of the shipped engine, reached through
// query.Engine.Recall with an AnswerOptions (Consumer, Prime, Hops, Ablate).
// Nothing here ranks claims itself. The raw baselines are the BM25 sentence
// retrievers in baseline.go. Every policy's ranked list is packed into each
// budget by the same function, packContext, with the token estimate the
// Context Block budgets with.
//
// Each policy answers on its own copy of the warmed seed, because recall writes
// back (Hebbian, reconsolidation, inhibition) and one policy's write-backs must
// not reach another's answers. The default policy is answered twice, on two
// copies; if the two disagree, measurement is not deterministic and the report
// says so.

// DefaultAblationBudgets are the token budgets the ablation packs into. The
// small ones are included on purpose: the scenario brains are small, and at
// the larger budgets every belief a policy retrieves fits, so ranking order
// stops mattering. The report counts, per cell, on how many questions the
// budget actually cut the ranked list.
var DefaultAblationBudgets = []int{32, 64, 128, 256, 512, 1024, 2048}

// Policy kinds.
const (
	PolicyReference  = "reference"
	PolicySimilarity = "similarity"
	PolicyLeaveOut   = "leave-one-out"
	PolicyOptIn      = "opt-in"
	PolicyBaseline   = "baseline"
)

// Policy names.
const (
	PolicyDefault        = "mnemos_default"
	PolicySimilarityOnly = "similarity_only"
)

// AblationPolicy is one ranking configuration under test.
type AblationPolicy struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Component   string `json:"component,omitempty"`
	Description string `json:"description"`

	// opts configures the engine for a Mnemos policy.
	opts query.AnswerOptions
	// raw selects a BM25 chunk baseline instead; recency is its variant.
	raw     bool
	recency bool
}

// similarityAblation removes every non-similarity component.
var similarityAblation = query.Ablation{Curation: true, Salience: true, Inhibition: true, IntentBoost: true, Trust: true, Freshness: true}

// AblationPolicies is the policy set, reference first.
func AblationPolicies() []AblationPolicy {
	agent := domain.ConsumerAgent
	user := domain.ConsumerUser
	sim := similarityAblation
	simDense, simLex := sim, sim
	simDense.Lexical = true
	simLex.Dense = true
	return []AblationPolicy{
		{Name: PolicyDefault, Kind: PolicyReference,
			Description: "shipped agent recall: Consumer=agent, cognitive defaults, hops 0",
			opts:        query.AnswerOptions{Consumer: agent}},
		{Name: PolicySimilarityOnly, Kind: PolicySimilarity,
			Description: "hybrid dense+BM25 similarity only: no curation, no contradiction resolution, no salience, inhibition, intent boost, trust or freshness",
			opts:        query.AnswerOptions{Consumer: user, Ablate: sim}},
		{Name: "similarity_dense_only", Kind: PolicySimilarity,
			Description: "similarity_only without the BM25 leg",
			opts:        query.AnswerOptions{Consumer: user, Ablate: simDense}},
		{Name: "similarity_lexical_only", Kind: PolicySimilarity,
			Description: "similarity_only without the dense leg",
			opts:        query.AnswerOptions{Consumer: user, Ablate: simLex}},
		leaveOut("dense", "cosine similarity over embeddings", query.Ablation{Dense: true}, agent),
		leaveOut("lexical", "BM25 over the event and claim full-text indexes", query.Ablation{Lexical: true}, agent),
		leaveOut("curation", "drop deprecated, session-local, superseded and archived claims", query.Ablation{Curation: true}, agent),
		leaveOut("contradiction_resolution", "agent-mode resolution of contradictions by confidence and credibility (Consumer=user)", query.Ablation{}, user),
		leaveOut("salience", "bounded stakes term", query.Ablation{Salience: true}, agent),
		leaveOut("inhibition", "competitive-inhibition penalty", query.Ablation{Inhibition: true}, agent),
		leaveOut("intent_boost", "question-intent claim-type reorder", query.Ablation{IntentBoost: true}, agent),
		leaveOut("trust", "bounded trust factor on relevance (ADR 0039)", query.Ablation{Trust: true}, agent),
		leaveOut("freshness", "bounded freshness factor on relevance, at the read's instant (ADR 0039)", query.Ablation{Freshness: true}, agent),
		{Name: "plus_prime", Kind: PolicyOptIn, Component: "prime",
			Description: "default plus spreading-activation priming (opt-in since ADR 0030)",
			opts:        query.AnswerOptions{Consumer: agent, Prime: true}},
		{Name: "plus_hops_1", Kind: PolicyOptIn, Component: "hops",
			Description: "default plus one hop of graph expansion",
			opts:        query.AnswerOptions{Consumer: agent, Hops: 1}},
		{Name: BaselineBM25, Kind: PolicyBaseline, raw: true, Description: baselineDescriptions[BaselineBM25]},
		{Name: BaselineBM25Recency, Kind: PolicyBaseline, raw: true, recency: true, Description: baselineDescriptions[BaselineBM25Recency]},
	}
}

func leaveOut(component, what string, a query.Ablation, consumer domain.Consumer) AblationPolicy {
	return AblationPolicy{
		Name: "minus_" + component, Kind: PolicyLeaveOut, Component: component,
		Description: "default without " + what,
		opts:        query.AnswerOptions{Consumer: consumer, Ablate: a},
	}
}

// Question kinds in the report.
const (
	QuestionProbe        = "probe"
	QuestionRecordedAsOf = "recorded_as_of"
	QuestionValidAsOf    = "valid_as_of"
)

// AblationQuestion identifies one question and its gold.
type AblationQuestion struct {
	Key      string   `json:"key"` // scenario/id
	Scenario string   `json:"scenario"`
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	Query    string   `json:"query"`
	Expect   string   `json:"expect"`
	MustNot  []string `json:"must_not,omitempty"`
	question question
}

// AblationOutcome is why a question succeeded or failed under one policy and
// budget.
type AblationOutcome string

const (
	// OutcomeSuccess — gold in the context, nothing gold-invalid in it.
	OutcomeSuccess AblationOutcome = "success"
	// OutcomeGoldNotRetrieved — the policy never ranked the gold belief.
	OutcomeGoldNotRetrieved AblationOutcome = "gold-not-retrieved"
	// OutcomeGoldOverBudget — the gold belief was ranked too low to fit.
	OutcomeGoldOverBudget AblationOutcome = "gold-over-budget"
	// OutcomeForbidden — gold in the context beside a gold-invalid belief.
	OutcomeForbidden AblationOutcome = "forbidden-in-context"
)

// Assessment scores one packed context against a question's gold.
type Assessment struct {
	Outcome AblationOutcome `json:"outcome"`
	// GoldRank is the 1-based rank of the first item containing the gold in
	// the full ranked list, 0 when absent.
	GoldRank int `json:"gold_rank"`
	// Packed is how many ranked items fit the budget, of Ranked.
	Packed int `json:"packed"`
	Ranked int `json:"ranked"`
	// Tokens is the packed context's estimated size.
	Tokens int `json:"tokens"`
	// Forbidden reports a gold-invalid item in the packed context, whether or
	// not the gold made it in.
	Forbidden bool `json:"forbidden"`
	// Binding reports that the budget cut the ranked list.
	Binding bool `json:"binding"`
}

// contextLine is how one ranked item is written into the context. Every
// policy uses the same line, so the raw baselines (which have no ids, types or
// trust to print) and the Mnemos policies pay the same tokens per item.
func contextLine(text string) string {
	return "- " + strings.TrimSpace(text) + "\n"
}

// packContext fills a context with ranked items in rank order until the next
// one would exceed budget, using the Context Block's token estimate and its
// stop rule: the first item that does not fit ends the block, as
// BuildContextBlock's MaxTokens does. A budget of 0 or less packs nothing.
func packContext(ranked []string, budget int) (packed []string, tokens int) {
	var b strings.Builder
	for _, t := range ranked {
		line := contextLine(t)
		if query.ApproxTokens(b.String()+line) > budget {
			break
		}
		b.WriteString(line)
		packed = append(packed, t)
	}
	return packed, query.ApproxTokens(b.String())
}

// assess packs ranked into budget and scores it against q's gold. A question
// succeeds when the packed context contains q.Expect and no q.MustNot string.
func assess(ranked []string, budget int, q question) Assessment {
	packed, tokens := packContext(ranked, budget)
	a := Assessment{
		GoldRank:  rankOf(ranked, q.Expect),
		Packed:    len(packed),
		Ranked:    len(ranked),
		Tokens:    tokens,
		Forbidden: anyForbidden(packed, q.MustNot),
		Binding:   len(packed) < len(ranked),
	}
	switch {
	case rankOf(packed, q.Expect) == 0 && a.GoldRank == 0:
		a.Outcome = OutcomeGoldNotRetrieved
	case rankOf(packed, q.Expect) == 0:
		a.Outcome = OutcomeGoldOverBudget
	case a.Forbidden:
		a.Outcome = OutcomeForbidden
	default:
		a.Outcome = OutcomeSuccess
	}
	return a
}

// CellResult is one policy at one budget over every question.
type CellResult struct {
	Budget    int `json:"budget"`
	Successes int `json:"successes"`
	N         int `json:"n"`
	// Binding counts the questions whose ranked list the budget cut. Where it
	// is 0 the whole ranked list fit, and ranking order cannot have mattered.
	Binding int `json:"binding"`
	// Outcomes holds one assessment per question, keyed by question key.
	Outcomes map[string]Assessment `json:"outcomes"`
}

// PolicyResult is one policy's ranked lists and its cells.
type PolicyResult struct {
	Policy AblationPolicy `json:"policy"`
	Cells  []CellResult   `json:"cells"`
	// Ranked is each question's full ranked list, keyed by question key.
	Ranked map[string][]string `json:"ranked"`
}

// Flip is one question whose outcome changed between two policies.
type Flip struct {
	Question string          `json:"question"`
	From     AblationOutcome `json:"from"`
	To       AblationOutcome `json:"to"`
}

// ComponentEffect is a policy against the default at one budget. Lost are the
// questions the default answers and the policy does not, Gained the reverse:
// a component carries weight on the Lost questions of its leave-one-out row.
type ComponentEffect struct {
	Policy string `json:"policy"`
	Budget int    `json:"budget"`
	Delta  int    `json:"delta"` // policy successes minus default successes
	Lost   []Flip `json:"lost,omitempty"`
	Gained []Flip `json:"gained,omitempty"`
}

// AblationReport is the full experiment.
type AblationReport struct {
	Hypothesis    string             `json:"hypothesis"`
	PassCondition string             `json:"pass_condition"`
	Success       string             `json:"success_definition"`
	Budgets       []int              `json:"budgets"`
	Questions     []AblationQuestion `json:"questions"`
	Policies      []PolicyResult     `json:"policies"`
	Effects       []ComponentEffect  `json:"effects"`
	// Deterministic is false when two copies of the default policy ranked any
	// question differently; then no difference in this report is attributable.
	Deterministic bool     `json:"deterministic"`
	Drifting      []string `json:"drifting_questions,omitempty"`
	Limitations   []string `json:"limitations"`
}

// RunAblation runs every policy over every scenario's questions.
func RunAblation(ctx context.Context, scenarios []Scenario, workDir string, budgets []int) (AblationReport, error) {
	if len(budgets) == 0 {
		budgets = DefaultAblationBudgets
	}
	rep := AblationReport{
		Hypothesis:    "the beliefs useful in an agent's context differ from the beliefs most similar to the question",
		PassCondition: "mnemos_default achieves higher task success than similarity_only at a fixed token budget",
		Success: "the context packed under the budget contains the question's expected text and none of its " +
			"must_not texts (superseded, contradicted or retracted statements)",
		Budgets:       append([]int(nil), budgets...),
		Deterministic: true,
		Limitations:   AblationLimitations(),
	}
	policies := AblationPolicies()
	ranked := make([]map[string][]string, len(policies))
	for i := range ranked {
		ranked[i] = map[string][]string{}
	}
	for _, sc := range scenarios {
		qs, drift, err := ablateScenario(ctx, sc, workDir, policies, ranked)
		if err != nil {
			return AblationReport{}, err
		}
		rep.Questions = append(rep.Questions, qs...)
		rep.Drifting = append(rep.Drifting, drift...)
	}
	rep.Deterministic = len(rep.Drifting) == 0
	for i, p := range policies {
		rep.Policies = append(rep.Policies, PolicyResult{
			Policy: p, Ranked: ranked[i], Cells: scoreCells(rep.Questions, ranked[i], budgets),
		})
	}
	rep.Effects = componentEffects(rep)
	return rep, nil
}

// scoreCells assesses one policy's ranked lists at every budget.
func scoreCells(qs []AblationQuestion, ranked map[string][]string, budgets []int) []CellResult {
	cells := make([]CellResult, 0, len(budgets))
	for _, b := range budgets {
		c := CellResult{Budget: b, N: len(qs), Outcomes: make(map[string]Assessment, len(qs))}
		for _, q := range qs {
			a := assess(ranked[q.Key], b, q.question)
			c.Outcomes[q.Key] = a
			if a.Outcome == OutcomeSuccess {
				c.Successes++
			}
			if a.Binding {
				c.Binding++
			}
		}
		cells = append(cells, c)
	}
	return cells
}

// componentEffects diffs every policy against the default, per budget.
func componentEffects(rep AblationReport) []ComponentEffect {
	var def PolicyResult
	for _, p := range rep.Policies {
		if p.Policy.Name == PolicyDefault {
			def = p
		}
	}
	var out []ComponentEffect
	for _, p := range rep.Policies {
		if p.Policy.Name == PolicyDefault {
			continue
		}
		for i, cell := range p.Cells {
			base := def.Cells[i]
			e := ComponentEffect{Policy: p.Policy.Name, Budget: cell.Budget, Delta: cell.Successes - base.Successes}
			for _, q := range rep.Questions {
				from, to := base.Outcomes[q.Key].Outcome, cell.Outcomes[q.Key].Outcome
				switch {
				case from == OutcomeSuccess && to != OutcomeSuccess:
					e.Lost = append(e.Lost, Flip{Question: q.Key, From: from, To: to})
				case from != OutcomeSuccess && to == OutcomeSuccess:
					e.Gained = append(e.Gained, Flip{Question: q.Key, From: from, To: to})
				}
			}
			out = append(out, e)
		}
	}
	return out
}

// ablateScenario seeds and warms one brain, then answers every question under
// every policy on its own copy. It fills ranked[policy][questionKey]. Like a
// paired run, the whole scenario runs on one simulated clock (clock.go).
func ablateScenario(ctx context.Context, sc Scenario, workDir string, policies []AblationPolicy, ranked []map[string][]string) ([]AblationQuestion, []string, error) {
	var (
		qs    []AblationQuestion
		drift []string
	)
	err := withSimulatedClock(func(sim *clock.Simulated) error {
		var err error
		qs, drift, err = ablateScenarioOn(ctx, sc, workDir, policies, ranked, sim)
		return err
	})
	return qs, drift, err
}

// ablateScenarioOn is ablateScenario's body, on the simulated clock sim.
func ablateScenarioOn(ctx context.Context, sc Scenario, workDir string, policies []AblationPolicy, ranked []map[string][]string, sim *clock.Simulated) ([]AblationQuestion, []string, error) {
	dir := filepath.Join(workDir, "ablation", sc.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("brainbench: ablation work dir: %w", err)
	}
	seedPath := filepath.Join(dir, "seed.db")
	stats, err := Seed(ctx, "sqlite://"+seedPath, sc, sim)
	if err != nil {
		return nil, nil, err
	}
	events, err := readEvents(ctx, "sqlite://"+seedPath)
	if err != nil {
		return nil, nil, err
	}
	if err := warmup(ctx, "sqlite://"+seedPath, sc); err != nil {
		return nil, nil, err
	}
	set, err := buildQuestions(sc, stats.Phases)
	if err != nil {
		return nil, nil, err
	}
	qs := ablationQuestions(sc.ID, set)

	for i, p := range policies {
		if p.raw {
			r := newRawRetriever(events, p.recency)
			for _, q := range qs {
				q.question.Limit = len(r.chunks)
				texts, err := r.retrieve(ctx, q.question)
				if err != nil {
					return nil, nil, err
				}
				ranked[i][q.Key] = texts
			}
			continue
		}
		got, err := answerOnCopy(ctx, seedPath, filepath.Join(dir, p.Name+".db"), p.opts, qs)
		if err != nil {
			return nil, nil, fmt.Errorf("brainbench: ablation %s/%s: %w", sc.ID, p.Name, err)
		}
		for k, v := range got {
			ranked[i][k] = v
		}
	}

	// Noise arm: the default policy again, on a fresh copy.
	again, err := answerOnCopy(ctx, seedPath, filepath.Join(dir, "noise.db"), policies[0].opts, qs)
	if err != nil {
		return nil, nil, fmt.Errorf("brainbench: ablation %s noise arm: %w", sc.ID, err)
	}
	var drift []string
	for _, q := range qs {
		if !reflect.DeepEqual(again[q.Key], ranked[0][q.Key]) {
			drift = append(drift, q.Key)
		}
	}
	return qs, drift, nil
}

// ablationQuestions lists a scenario's questions in asking order: as-of first,
// then probes, as runBattery asks them.
func ablationQuestions(scenario string, set questionSet) []AblationQuestion {
	all := append(append([]question(nil), set.asOf...), set.probes...)
	out := make([]AblationQuestion, 0, len(all))
	for _, q := range all {
		kind := QuestionProbe
		switch {
		case !q.RecordedAsOf.IsZero():
			kind = QuestionRecordedAsOf
		case !q.AsOf.IsZero():
			kind = QuestionValidAsOf
		}
		out = append(out, AblationQuestion{
			Key: scenario + "/" + q.ID, Scenario: scenario, ID: q.ID, Kind: kind,
			Query: q.Text, Expect: q.Expect, MustNot: q.MustNot, question: q,
		})
	}
	return out
}

// answerOnCopy copies the seed and asks every question through
// query.Engine.Recall with opts, returning each question's ranked texts.
func answerOnCopy(ctx context.Context, seedPath, dst string, opts query.AnswerOptions, qs []AblationQuestion) (map[string][]string, error) {
	if err := copyDB(seedPath, dst); err != nil {
		return nil, err
	}
	conn, err := store.Open(ctx, "sqlite://"+dst)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dst, err)
	}
	defer func() { _ = conn.Close() }()
	engine := ablationEngine(conn)

	out := make(map[string][]string, len(qs))
	for _, q := range qs {
		o := opts
		o.AsOf, o.RecordedAsOf = q.question.AsOf, q.question.RecordedAsOf
		o.SkipAnswerText = true
		ans, err := engine.Recall(ctx, query.RecallRequest{Question: q.question.Text, Options: o})
		if err != nil {
			return nil, fmt.Errorf("question %s: %w", q.Key, err)
		}
		texts := make([]string, len(ans.Claims))
		for i, c := range ans.Claims {
			texts[i] = c.Text
		}
		out[q.Key] = texts
	}
	return out, nil
}

// ablationEngine builds the engine the shipped surfaces build, with the
// deterministic stub embedder as its embedding client so the dense leg is
// live: the seed stores stub vectors for every event and claim, and without a
// client to embed the question the dense leg would never run, leaving nothing
// for the dense ablation to remove.
func ablationEngine(conn *store.Conn) query.Engine {
	return query.NewEngineWith(conn.Events, conn.Claims, conn.Relationships, query.EngineDeps{
		Embeddings:  conn.Embeddings,
		EmbedClient: stubEmbedder{},
		Decisions:   conn.Decisions,
		Incidents:   conn.Incidents,
		Journal:     conn.Journal,
	})
}

// stubEmbedder embeds the question with the seed's stub, in the seed's model
// space.
type stubEmbedder struct{}

// Embed implements embedding.Client.
func (stubEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = EmbedText(t)
	}
	return out, nil
}

// ModelID confines cosine to the vectors the seed stored.
func (stubEmbedder) ModelID() string { return StubEmbedderModel }

// AblationLimitations is what the ablation does not establish. It ships inside
// every report.
func AblationLimitations() []string {
	return []string{
		"The question set is small (the brainbench scenarios' probes and as-of probes); " +
			"one question flipping moves a policy's success count by one. Read the per-question outcomes, not only the totals.",
		"Success is a substring test against the scenario gold. It credits a context that contains the gold text; " +
			"it does not check that an agent would use it correctly, and a gold string that is a substring of a " +
			"gold-invalid one (payments-oncall inside payments-oncall-legacy) is credited either way.",
		"The dense leg is a hashed bag-of-words stub, so it is lexical too. A real embedding model would rank " +
			"paraphrases this stub cannot see; dense-leg results here say nothing about a production embedder.",
		"Extraction is rule-based. Beliefs are the sentences rule-based extraction keeps; LLM extraction would " +
			"produce different beliefs and a different ranking problem.",
		"Tokens are estimated as characters / 4, the Context Block's estimate, and every policy writes one " +
			"'- <text>' line per item; the metadata a real context block or recall hook adds (ids, types, trust) is not charged.",
		"The scenario brains are small. At the larger budgets every retrieved belief fits; the binding count in each " +
			"cell says on how many questions the budget cut the ranked list at all.",
		"Ranking orders a context; it cannot remove a statement from it. Once a budget holds both a superseded " +
			"statement and its replacement, the question fails whatever the order, unless curation or contradiction " +
			"resolution dropped the older one. Trust and freshness (ADR 0039) therefore show only at budgets that cut the list.",
		"Hebbian strengthening and reconsolidation are write-backs; Hebbian strength is read only by priming, which " +
			"is off by default, so they have no leave-one-out row either. plus_prime measures priming.",
	}
}

// sortedFlipKeys renders flips as their question keys, sorted.
func sortedFlipKeys(fs []Flip) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.Question
	}
	sort.Strings(out)
	return out
}
