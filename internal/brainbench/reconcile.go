package brainbench

// Deterministic vs LLM reconciliation (ADR 0042).
//
// Reconciliation is deciding the relationship between two beliefs that may
// conflict: contradicts (one denies or supersedes the other) or compatible
// (they agree, add detail, concern different scopes, or are unrelated). Mnemos
// decides it with the rule-based detectors in internal/relate. This experiment
// asks whether a local LLM, alone or routed only to the pairs the rules are
// unsure of, decides it better, and at what cost.
//
// Three arms, scored on one hand-labelled pair set (data/eval/reconciliation):
//
//   - deterministic: relate.Engine.Detect on the pair, exactly as production
//     runs it. A contradicts edge is a "contradicts" prediction.
//   - llm: the model labels every pair with ReconcileSystemPrompt. It sees no
//     deterministic output.
//   - hybrid: the routing rule below, fixed before any result was seen.
//
// Nothing here is wired into production. The LLM is reached only through a
// PairJudge the caller supplies, so `make test` never needs a model.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/llm"
	"go.klarlabs.de/mnemos/internal/relate"
)

// Reconciliation labels.
const (
	LabelContradicts = "contradicts"
	LabelCompatible  = "compatible"
)

// Reconciliation arms.
const (
	ArmDeterministic = "deterministic"
	ArmLLM           = "llm"
	ArmHybrid        = "hybrid"
)

// Splits. The split is derived from the pair id, never stored, so it cannot be
// edited to suit a result.
const (
	SplitDev  = "dev"
	SplitTest = "test"
)

// ReconcilePromptVersion versions ReconcileSystemPrompt. Bump it whenever the
// prompt text changes; results are only comparable within one version.
const ReconcilePromptVersion = "reconcile-prompt/v1"

// ReconcileSystemPrompt is the fixed instruction every LLM call carries. It
// states the labelling rules of data/eval/reconciliation/README.md and
// nothing else: no examples from the pair set, and no deterministic output.
const ReconcileSystemPrompt = `You decide whether two statements from an engineering team's knowledge base can both be true.

Answer "contradicts" when they cannot both be true of the same subject at the same time: one denies the other; they give different values for the same thing (a number, a version, a name, an owner, a tool, a place, a time, a manner, or a policy); or one says the state the other describes was replaced or no longer holds.

Answer "compatible" when both can be true together: they agree or restate each other; one adds detail; they are about different subjects, scopes, environments, branches, versions, or time periods; or they are unrelated. A text that asserts nothing (a fragment, a header, narration) is compatible with anything.

Judge only from the two statements. Reply with JSON only, exactly one of:
{"verdict": "contradicts"}
{"verdict": "compatible"}`

// HybridStrengthThreshold is the routing threshold, fixed before any result
// was seen and not tuned on the test split. A deterministic contradiction
// weaker than this goes to the LLM. Strength is prior × (0.5 + 0.5 ×
// alignment) (relate/contradiction.go), so 0.65 routes every temporal edge
// (prior 0.6), frame and named-value edges that align poorly, and almost no
// polarity, antonym, numeric or entity edge.
const HybridStrengthThreshold = 0.65

// HybridRoutingRule is the routing rule in words, as the report prints it.
const HybridRoutingRule = "deterministic decides, except: (a) a contradicts edge with strength < 0.65 is re-decided by the LLM; " +
	"(b) a pair with no contradicts edge that passes relate.SharesTopic (>= 2 shared content tokens covering >= 30% of the shorter claim) is decided by the LLM. " +
	"Pairs that fail the topical gate and have no edge stay compatible without an LLM call."

// Routes a hybrid pair can take.
const (
	RouteDeterministicEdge = "det-edge" // strong deterministic contradiction, kept
	RouteLowStrength       = "llm-low-strength"
	RouteTopicalNoEdge     = "llm-topical-no-edge"
	RouteNoCandidate       = "det-no-candidate" // no edge and no topical overlap: compatible, no call
)

// ReconcilePair is one labelled pair.
type ReconcilePair struct {
	ID        string `yaml:"id" json:"id"`
	A         string `yaml:"a" json:"a"`
	B         string `yaml:"b" json:"b"`
	Label     string `yaml:"label" json:"label"`
	Category  string `yaml:"category" json:"category"`
	Source    string `yaml:"source" json:"source"`
	Rationale string `yaml:"rationale" json:"rationale"`
	// Split is derived from ID by ReconcileSplit.
	Split string `yaml:"-" json:"split"`
}

// Hand reports whether the pair was written for this set rather than taken
// from an existing corpus.
func (p ReconcilePair) Hand() bool { return p.Source == "hand" }

// ReconcileSet is the labelled pair set.
type ReconcileSet struct {
	Version int             `yaml:"version"`
	Pairs   []ReconcilePair `yaml:"pairs"`
}

// ReconcileSplit assigns a pair to dev or test from its id alone: FNV-1a of the
// id, mod 10; 0-2 dev, 3-9 test.
func ReconcileSplit(id string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	if h.Sum32()%10 < 3 {
		return SplitDev
	}
	return SplitTest
}

// LoadReconcileSet reads and validates a pair set. Any malformed pair is a
// hard error: a skipped pair would make the report cover less than it claims.
func LoadReconcileSet(path string) (ReconcileSet, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // operator-supplied fixture path
	if err != nil {
		return ReconcileSet{}, fmt.Errorf("reconcile: read %s: %w", path, err)
	}
	return ParseReconcileSet(raw)
}

// ParseReconcileSet parses and validates a pair set.
func ParseReconcileSet(raw []byte) (ReconcileSet, error) {
	var set ReconcileSet
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&set); err != nil {
		return ReconcileSet{}, fmt.Errorf("reconcile: parse pairs: %w", err)
	}
	if len(set.Pairs) == 0 {
		return ReconcileSet{}, errors.New("reconcile: pair set is empty")
	}
	seen := map[string]bool{}
	for i := range set.Pairs {
		p := &set.Pairs[i]
		switch {
		case p.ID == "":
			return ReconcileSet{}, fmt.Errorf("reconcile: pair %d has no id", i)
		case seen[p.ID]:
			return ReconcileSet{}, fmt.Errorf("reconcile: duplicate pair id %q", p.ID)
		case strings.TrimSpace(p.A) == "" || strings.TrimSpace(p.B) == "":
			return ReconcileSet{}, fmt.Errorf("reconcile: pair %q has an empty side", p.ID)
		case p.Label != LabelContradicts && p.Label != LabelCompatible:
			return ReconcileSet{}, fmt.Errorf("reconcile: pair %q has label %q (want %s or %s)", p.ID, p.Label, LabelContradicts, LabelCompatible)
		case p.Category == "" || p.Source == "" || strings.TrimSpace(p.Rationale) == "":
			return ReconcileSet{}, fmt.Errorf("reconcile: pair %q needs a category, a source and a rationale", p.ID)
		}
		seen[p.ID] = true
		p.Split = ReconcileSplit(p.ID)
	}
	return set, nil
}

// DetVerdict is what the deterministic arm decided about one pair.
type DetVerdict struct {
	Contradicts bool    `json:"contradicts"`
	Detector    string  `json:"detector,omitempty"`
	Strength    float64 `json:"strength,omitempty"`
	SharesTopic bool    `json:"shares_topic"`
}

// DeterministicVerdict runs the production batch pass on the pair alone.
func DeterministicVerdict(a, b string) (DetVerdict, error) {
	rels, err := relate.NewEngine().Detect([]domain.Claim{{ID: "cl_a", Text: a}, {ID: "cl_b", Text: b}})
	if err != nil {
		return DetVerdict{}, err
	}
	v := DetVerdict{SharesTopic: relate.SharesTopic(a, b)}
	for _, r := range rels {
		if r.Type != domain.RelationshipTypeContradicts {
			continue
		}
		v.Contradicts = true
		v.Strength = r.Strength
		v.Detector = strings.TrimPrefix(r.DerivedBy, relate.ModelVersion+":")
	}
	return v, nil
}

// HybridRoute applies the routing rule to a deterministic verdict.
func HybridRoute(v DetVerdict) string {
	switch {
	case v.Contradicts && v.Strength >= HybridStrengthThreshold:
		return RouteDeterministicEdge
	case v.Contradicts:
		return RouteLowStrength
	case v.SharesTopic:
		return RouteTopicalNoEdge
	default:
		return RouteNoCandidate
	}
}

// RoutesToLLM reports whether a route asks the LLM.
func RoutesToLLM(route string) bool {
	return route == RouteLowStrength || route == RouteTopicalNoEdge
}

// Judgement is one LLM decision about a pair.
type Judgement struct {
	// Verdict is LabelContradicts or LabelCompatible, or "" when the call
	// failed or the reply could not be read. An empty verdict counts as
	// compatible (no edge), the fail-safe a production path would take, and is
	// reported separately so a model that answers nothing cannot pass for one
	// that answers "compatible".
	Verdict      string        `json:"verdict"`
	Raw          string        `json:"raw,omitempty"`
	Err          string        `json:"error,omitempty"`
	Latency      time.Duration `json:"-"`
	LatencyMS    float64       `json:"latency_ms"`
	InputTokens  int           `json:"input_tokens,omitempty"`
	OutputTokens int           `json:"output_tokens,omitempty"`
}

// PairJudge decides one pair. Implementations must be safe to call
// sequentially; the harness never calls one concurrently.
type PairJudge interface {
	Judge(ctx context.Context, a, b string) Judgement
}

// LLMJudge is a PairJudge backed by an llm.Client with ReconcileSystemPrompt.
type LLMJudge struct {
	client llm.Client
}

// NewLLMJudge wraps client. The caller fixes the model and sampling (the
// experiment uses temperature 0 and a fixed seed).
func NewLLMJudge(client llm.Client) *LLMJudge { return &LLMJudge{client: client} }

// Judge asks the model about one pair.
func (j *LLMJudge) Judge(ctx context.Context, a, b string) Judgement {
	start := time.Now()
	resp, err := j.client.Complete(ctx, ReconcileMessages(a, b))
	lat := time.Since(start)
	out := Judgement{Latency: lat, LatencyMS: ms(lat)}
	if err != nil {
		out.Err = err.Error()
		return out
	}
	out.Raw = resp.Content
	out.InputTokens = resp.InputTokens
	out.OutputTokens = resp.OutputTokens
	if v, ok := ParseReconcileVerdict(resp.Content); ok {
		out.Verdict = v
	}
	return out
}

// ReconcileMessages is the exact conversation sent for a pair.
func ReconcileMessages(a, b string) []llm.Message {
	return []llm.Message{
		{Role: llm.RoleSystem, Content: ReconcileSystemPrompt},
		{Role: llm.RoleUser, Content: "Statement A: " + strings.TrimSpace(a) + "\nStatement B: " + strings.TrimSpace(b)},
	}
}

// Whole words only: "incompatible" must not read as "compatible".
var (
	contradictsWordRE = regexp.MustCompile(`\bcontradicts\b`)
	compatibleWordRE  = regexp.MustCompile(`\bcompatible\b`)
)

// ParseReconcileVerdict reads a model reply. Strict JSON first, then the first
// JSON object inside surrounding prose, then — only when exactly one of the two
// labels appears — the bare label. Anything else is unreadable.
func ParseReconcileVerdict(content string) (string, bool) {
	c := strings.TrimSpace(content)
	if c == "" {
		return "", false
	}
	var payload struct {
		Verdict string `json:"verdict"`
	}
	tryJSON := func(s string) (string, bool) {
		if err := json.Unmarshal([]byte(s), &payload); err != nil {
			return "", false
		}
		v := strings.ToLower(strings.TrimSpace(payload.Verdict))
		if v == LabelContradicts || v == LabelCompatible {
			return v, true
		}
		return "", false
	}
	if v, ok := tryJSON(c); ok {
		return v, true
	}
	if start := strings.Index(c, "{"); start >= 0 {
		if end := strings.Index(c[start:], "}"); end > 0 {
			if v, ok := tryJSON(c[start : start+end+1]); ok {
				return v, true
			}
		}
	}
	lc := strings.ToLower(c)
	hasC, hasK := contradictsWordRE.MatchString(lc), compatibleWordRE.MatchString(lc)
	switch {
	case hasC && !hasK:
		return LabelContradicts, true
	case hasK && !hasC:
		return LabelCompatible, true
	}
	return "", false
}
