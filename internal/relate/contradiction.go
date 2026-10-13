package relate

import (
	"math"
	"regexp"
	"strings"
	"unicode"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/extract"
)

// Contradiction precision (relate/v4).
//
// Measured on a real brain before this change: 103k open `contradicts` edges
// among 111k live claims. A hand-classified random sample of 20 held no genuine
// conflict: half were session-header fragments ("User messages in this
// session:") paired with some fact, a fifth were paraphrases that AGREE ("tests
// fail without the fix and pass with it" vs "red→green"), and the rest were
// unrelated. 1,102 hub claims with more than 100 edges each held 88% of all
// edge endpoints, and the top hubs were code lines and fragments.
//
// The lexical detectors (polarity, value, numeric, entity, temporal) are kept,
// but none of them decides on its own any more. Each only PROPOSES a
// contradiction; adjudicateContradiction accepts it only when:
//
//  1. both claims are contentful, standalone propositions — not junk, code,
//     transcript boilerplate or fragments (IsContradictionEndpoint);
//  2. the two claims are about the same subject — their remaining content is
//     nearly identical once the conflicting part is set aside;
//  3. they assert incompatible values or polarity about it — a real negation
//     operator on one side only, a known antonym swap, a named value swapped
//     for another, a different number, or an exclusive aspect;
//  4. they are not a paraphrase that agrees ("not enabled" vs "disabled").
//
// Every accepted edge records the detector that produced it in DerivedBy
// (ContradictionDerivedBy) and a strength in (0, 1] that reflects how well the
// two claims align. A pass keeps at most MaxContradictionsPerClaim edges per
// claim, so no single claim can become a hub.
//
// Shared vocabulary or a negation word alone never suffices. There is no LLM
// adjudication here: this path is deterministic, and the semantic (NLI) path
// proposed in #444 is a separate, opt-in change.

// Contradiction detectors, recorded in DerivedBy as "<ModelVersion>:<detector>".
const (
	DetectorPolarity     = "polarity"
	DetectorValue        = "value"
	DetectorNumeric      = "numeric"
	DetectorEntity       = "entity"
	DetectorTemporal     = "temporal"
	DetectorTestConflict = "test-conflict"
)

// ContradictionDerivedBy is the DerivedBy stamp of a contradiction edge the
// named detector produced.
func ContradictionDerivedBy(detector string) string {
	return ModelVersion + ":" + detector
}

// MaxContradictionsPerClaim is the most contradiction edges one claim keeps.
//
// A detection pass keeps a claim's strongest edges up to this cap, and the
// write path and `relate --prune-stale` apply it against the edges already
// stored, so a claim cannot accumulate hundreds of contradictions across
// writes either. A proposition genuinely contested by more than a handful of
// distinct beliefs is vanishingly rare; a claim "contradicting" hundreds is a
// fragment or a topic, and every edge it holds is noise. Before this cap, 1,102
// claims held more than 100 contradiction edges each.
const MaxContradictionsPerClaim = 5

// Minimum subject alignment (Jaccard over concept tokens) per detector.
const (
	minPolarityAlignment = 0.6
	minValueAlignment    = 0.5
	minAntonymAlignment  = 0.4
	minNumericAlignment  = 0.5
	minTemporalAlignment = 0.5
)

// Detector priors: the most a detector's verdict is worth when the two claims
// align perfectly. Strength is prior × (0.5 + 0.5 × alignment).
const (
	priorPolarity     = 0.9
	priorAntonym      = 0.9
	priorValueLiteral = 0.75
	priorNumeric      = 0.85
	priorEntity       = 0.8
	priorTemporal     = 0.6
	priorTestConflict = 1.0
)

// IsContradictionEndpoint reports whether text can be one side of a
// contradiction: a contentful, standalone proposition. Junk (narration,
// lead-ins, acknowledgements — the same filter extraction applies), code
// lines, transcript boilerplate and fragments cannot; they assert nothing
// another claim could disagree with.
func IsContradictionEndpoint(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	if extract.IsJunk(t) || isCodeLine(t) || isTranscriptBoilerplate(t) {
		return false
	}
	return isStandaloneProposition(t)
}

// fragmentEndings mark text that stops mid-thought: a lead-in, a list item cut
// at its comma, an open bracket, an ellipsis.
var fragmentEndings = []string{":", ",", ";", "(", "[", "{", "-", "–", "—", "…", "...", "=", "+", "&", "|"}

// isStandaloneProposition reports whether t reads as a complete assertion with
// enough content to be about something specific. Three word tokens suffice;
// two do only when one of them names something (a proper noun, a number or an
// identifier), as in "The CEO is Alice". "The gate passed" and "All user
// messages" have two generic words and no referent, so there is nothing
// definite for another claim to contradict.
func isStandaloneProposition(t string) bool {
	for _, end := range fragmentEndings {
		if strings.HasSuffix(t, end) {
			return false
		}
	}
	// Concept tokens, not content tokens: the tokenizer drops lexical
	// negatives ("failed"), which here are the predicate.
	words := len(wordTokens(conceptTokens(t)))
	switch {
	case words >= 3:
		return true
	case words == 2:
		return len(valueTokens(t)) > 0 || len(extractNumerics(t)) > 0
	default:
		return false
	}
}

// codeLineRE matches source-code shapes: declarations, imports, comments and
// operators prose does not use.
var codeLineRE = regexp.MustCompile(`^(?:pub(?:\([a-z]+\))?\s+\w|func\s+[\w(]|fn\s+\w|impl\b|import\s+["(\w]|package\s+\w+$|` +
	`type\s+\w+\s+(?:struct|interface|=)|def\s+\w+\(|class\s+\w+\s*[(:{]|#include|#define|//|/\*|\*/|@\w+\()` +
	`|:=|==|!=|&&|\|\||::|=>|\)\s*\{|\(\);|^\s*[}\])]`)

// isCodeLine reports whether t is a line of source code rather than prose:
// "type CompilationMachine interface {", "pub signing_key: String,".
func isCodeLine(t string) bool {
	if codeLineRE.MatchString(t) {
		return true
	}
	symbols, visible := 0, 0
	for _, r := range t {
		if unicode.IsSpace(r) {
			continue
		}
		visible++
		if strings.ContainsRune("{}[]();=<>|&$`", r) {
			symbols++
		}
	}
	return symbols >= 3 && float64(symbols) >= 0.1*float64(visible)
}

// transcriptBoilerplateRE matches capture scaffolding: the headers and labels
// of a session transcript, which say nothing about the world. Three such texts
// alone held 34.5% of all contradiction edges on a real brain.
var transcriptBoilerplateRE = regexp.MustCompile(`(?i)` +
	`^(?:all\s+|the\s+|my\s+|these\s+)?(?:user|assistant|human|system|agent)\s+(?:messages?|prompts?|turns?|requests?)\b` +
	`|\b(?:in|of|from|during|for)\s+(?:this|the\s+current|the\s+previous|the\s+last)\s+(?:session|conversation|chat|transcript)\b` +
	`|^(?:session|conversation|transcript)\s+(?:summary|header|transcript|log|start|end)\b`)

func isTranscriptBoilerplate(t string) bool {
	return transcriptBoilerplateRE.MatchString(t)
}

// strictNegators are the words that negate a predicate. The tokenizer's wider
// negationWords also holds lexical negatives ("without", "failed",
// "rejected", "unlikely"), which carry content rather than deny the claim:
// "tests fail without the fix" asserts something, it does not negate it.
// Reading those as polarity is the NLI negation-cue shortcut (#444), and it
// paired agreeing paraphrases as contradictions.
var strictNegators = map[string]struct{}{
	"not": {}, "no": {}, "never": {}, "cannot": {}, "nor": {}, "none": {},
	"cant": {}, "can't": {}, "dont": {}, "don't": {}, "doesnt": {}, "doesn't": {},
	"didnt": {}, "didn't": {}, "isnt": {}, "isn't": {}, "arent": {}, "aren't": {},
	"wasnt": {}, "wasn't": {}, "werent": {}, "weren't": {}, "wont": {}, "won't": {},
	"wouldnt": {}, "wouldn't": {}, "shouldnt": {}, "shouldn't": {},
	"hasnt": {}, "hasn't": {}, "havent": {}, "haven't": {}, "hadnt": {}, "hadn't": {},
	"couldnt": {}, "couldn't": {},
}

// strictlyNegated reports whether text negates its predicate with a real
// negation operator, ignoring contrastive negation ("not merely X").
func strictlyNegated(text string) bool {
	words := strings.Fields(strings.ToLower(text))
	for i, w := range words {
		w = strings.Trim(w, ",.;:!?()[]{}\"'")
		w = strings.ReplaceAll(w, "’", "'")
		if _, ok := strictNegators[w]; !ok {
			continue
		}
		if !isContrastiveNegation(words, i) {
			return true
		}
	}
	return false
}

// conceptOf normalises one lowercased word for judging whether two claims say
// the same thing, so the forms of a word compare equal: "decrease",
// "decreased" and "decreasing" become "decreas"; "succeed" and "succeeded"
// both "succeed"; "running" and "run" both "run". The tokenizer's stemWord is
// too coarse for this ("succeed" loses its "ed"), and the tokenizer drops
// lexical negatives such as "failed" and "impossible", which here are content:
// they are exactly the values a contradiction turns on.
//
// It is used only to judge alignment, never for candidate indexing, so it
// cannot make the candidate index miss a pair.
func conceptOf(w string) string {
	w = strings.TrimSuffix(w, "'s")
	n := len(w)
	switch {
	case n > 4 && strings.HasSuffix(w, "ies"):
		w = w[:n-3] + "y"
	case n > 5 && strings.HasSuffix(w, "ing"):
		w = w[:n-3]
	case n > 4 && strings.HasSuffix(w, "ed") && !strings.HasSuffix(w, "eed"):
		w = w[:n-2]
	case n > 5 && strings.HasSuffix(w, "es"):
		w = w[:n-2]
	case n > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") && !strings.HasSuffix(w, "us"):
		w = w[:n-1]
	}
	if m := len(w); m > 4 && strings.HasSuffix(w, "e") && !strings.HasSuffix(w, "ee") {
		w = w[:m-1]
	}
	// Collapse a doubled final consonant: "runn" -> "run", "shipp" -> "ship".
	if m := len(w); m > 3 && w[m-1] == w[m-2] && !strings.ContainsRune("aeiousl", rune(w[m-1])) {
		w = w[:m-1]
	}
	return w
}

// conceptTokens is the alignment view of text: every word that is neither a
// stop word nor a negation operator, normalised by conceptOf.
func conceptTokens(text string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, w := range strings.Fields(strings.ToLower(text)) {
		w = strings.ReplaceAll(strings.Trim(w, ",.;:!?()[]{}\"'"), "’", "'")
		if w == "" {
			continue
		}
		if _, ok := stopWords[w]; ok {
			continue
		}
		if _, ok := strictNegators[w]; ok {
			continue
		}
		out[conceptOf(w)] = struct{}{}
	}
	return out
}

// antonymPairs are predicate values that exclude each other about one subject.
// Both sides are normalised by conceptOf at init, so inflections match.
var antonymPairs = [][2]string{
	{"enabled", "disabled"}, {"enable", "disable"}, {"on", "off"}, {"true", "false"},
	{"pass", "fail"}, {"succeed", "fail"}, {"success", "failure"}, {"successful", "failed"},
	{"successfully", "failed"}, {"completed", "failed"}, {"works", "broken"}, {"working", "broken"},
	{"achievable", "impossible"}, {"feasible", "impossible"}, {"open", "closed"}, {"opened", "closed"},
	{"allowed", "denied"}, {"allow", "deny"}, {"allowed", "blocked"}, {"permitted", "forbidden"},
	{"public", "private"}, {"sync", "async"}, {"synchronous", "asynchronous"},
	{"required", "optional"}, {"mandatory", "optional"}, {"present", "absent"},
	{"online", "offline"}, {"increase", "decrease"}, {"increased", "decreased"},
	{"higher", "lower"}, {"faster", "slower"}, {"larger", "smaller"}, {"more", "fewer"},
	{"include", "exclude"}, {"included", "excluded"}, {"accepted", "rejected"},
	{"active", "inactive"}, {"visible", "hidden"}, {"started", "stopped"},
	{"running", "stopped"}, {"internal", "external"}, {"local", "remote"},
	{"minimum", "maximum"}, {"always", "never"}, {"green", "red"}, {"up", "down"},
	{"before", "after"}, {"first", "last"}, {"read-only", "writable"},
}

// antonymPrefixes negate a word morphologically: "supported"/"unsupported".
var antonymPrefixes = []string{"un", "in", "im", "non", "non-", "dis"}

var antonymIndex = func() map[string]map[string]struct{} {
	idx := map[string]map[string]struct{}{}
	add := func(a, b string) {
		if idx[a] == nil {
			idx[a] = map[string]struct{}{}
		}
		idx[a][b] = struct{}{}
	}
	for _, p := range antonymPairs {
		a, b := conceptOf(p[0]), conceptOf(p[1])
		add(a, b)
		add(b, a)
	}
	return idx
}()

// areAntonyms reports whether two concept tokens exclude each other.
func areAntonyms(a, b string) bool {
	if _, ok := antonymIndex[a][b]; ok {
		return true
	}
	for _, p := range antonymPrefixes {
		if len(a) > 3 && (b == p+a) || len(b) > 3 && (a == p+b) {
			return true
		}
	}
	return false
}

// valueTokens returns the concept tokens of the words in text that name a
// specific value rather than describe one: proper nouns ("React", "Alice"),
// identifiers with internal structure ("v1.2", "go-redis", "signing_key") and
// mixed-case names ("MySQL"). A sentence that swaps one of these for another
// assigns its subject a different value; a sentence that swaps a common word
// ("pass" for "green") is more often a paraphrase.
func valueTokens(text string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, w := range properNounWords(text) {
		out[conceptOf(w)] = struct{}{}
	}
	for _, w := range strings.Fields(text) {
		trimmed := strings.Trim(w, ",.;:!?()[]{}\"'")
		if trimmed == "" || !isIdentifierLike(trimmed) {
			continue
		}
		out[conceptOf(strings.ToLower(trimmed))] = struct{}{}
	}
	return out
}

// isIdentifierLike reports whether a word is a code-ish or versioned name:
// letters mixed with digits or inner punctuation, or an inner capital.
func isIdentifierLike(w string) bool {
	hasLetter, hasDigit, hasInner, innerUpper := false, false, false, false
	runes := []rune(w)
	for i, r := range runes {
		switch {
		case unicode.IsLetter(r):
			hasLetter = true
			if i > 0 && unicode.IsUpper(r) && unicode.IsLower(runes[i-1]) {
				innerUpper = true
			}
		case unicode.IsDigit(r):
			hasDigit = true
		case i > 0 && i < len(runes)-1 && strings.ContainsRune("._-/", r):
			hasInner = true
		}
	}
	return hasLetter && (hasDigit || hasInner || innerUpper)
}

// contradictionView is what adjudication needs about one claim, derived once.
type contradictionView struct {
	endpoint bool
	strict   bool
	concepts map[string]struct{}
	values   map[string]struct{}
}

func newContradictionView(text string) *contradictionView {
	return &contradictionView{
		endpoint: IsContradictionEndpoint(text),
		strict:   strictlyNegated(text),
		concepts: conceptTokens(text),
		values:   valueTokens(text),
	}
}

// pairVerdict is the classification of one claim pair.
type pairVerdict struct {
	relType  domain.RelationshipType
	detector string  // contradiction detector; empty for other edge types
	strength float64 // contradiction strength in (0, 1]; overlap rank for supports
}

// setDiff splits two sets into the tokens only a has, only b has, and their
// Jaccard similarity.
func setDiff(a, b map[string]struct{}) (onlyA, onlyB []string, jaccard float64) {
	shared := 0
	for tok := range a {
		if _, ok := b[tok]; ok {
			shared++
		} else {
			onlyA = append(onlyA, tok)
		}
	}
	for tok := range b {
		if _, ok := a[tok]; !ok {
			onlyB = append(onlyB, tok)
		}
	}
	union := len(a) + len(b) - shared
	if union > 0 {
		jaccard = float64(shared) / float64(union)
	}
	return onlyA, onlyB, jaccard
}

func anyAntonymPair(onlyA, onlyB []string) bool {
	for _, x := range onlyA {
		for _, y := range onlyB {
			if areAntonyms(x, y) {
				return true
			}
		}
	}
	return false
}

func strengthOf(prior, alignment float64) float64 {
	return math.Round(prior*(0.5+0.5*alignment)*100) / 100
}

// polarityConflict is the precise polarity detector: the same proposition,
// asserted on one side and denied with a real negation operator on the other.
// "Revenue decreased after launch" vs "Revenue did not decrease after launch".
//
// The rest of the two claims must be nearly identical, so a negation word
// somewhere in an otherwise different sentence never qualifies. A swap that is
// itself an antonym pair cancels the negation — "Feature X is not enabled" and
// "Feature X is disabled" agree — and is rejected.
func polarityConflict(a, b *contradictionView, overlap int) (float64, bool) {
	if a.strict == b.strict || overlap < minContentTokenOverlap {
		return 0, false
	}
	onlyA, onlyB, j := setDiff(a.concepts, b.concepts)
	if j < minPolarityAlignment || len(onlyA) > 2 || len(onlyB) > 2 {
		return 0, false
	}
	if anyAntonymPair(onlyA, onlyB) {
		return 0, false
	}
	return strengthOf(priorPolarity, j), true
}

// valueConflict is the precise value detector: the same sentence about the
// same subject, with one value swapped for an incompatible one — an antonym
// ("enabled"/"disabled") or a different named value ("React"/"Vue").
// Both claims must have the same polarity; a different polarity is the
// polarity detector's case (and an antonym under negation agrees).
func valueConflict(a, b *contradictionView, overlap int) (string, float64, bool) {
	if a.strict != b.strict || overlap < minContentTokenOverlap {
		return "", 0, false
	}
	onlyA, onlyB, j := setDiff(a.concepts, b.concepts)
	if len(onlyA) == 0 || len(onlyB) == 0 || len(onlyA) > 2 || len(onlyB) > 2 {
		return "", 0, false
	}
	// An antonym swap is explicit evidence of incompatibility, so it tolerates
	// one qualifier more ("completed successfully" vs "failed").
	if j >= minAntonymAlignment && anyAntonymPair(onlyA, onlyB) {
		return DetectorValue, strengthOf(priorAntonym, j), true
	}
	// A named value swapped for another: exactly one token differs on each
	// side and both name something.
	if j >= minValueAlignment && len(onlyA) == 1 && len(onlyB) == 1 {
		_, va := a.values[onlyA[0]]
		_, vb := b.values[onlyB[0]]
		if va && vb {
			return DetectorValue, strengthOf(priorValueLiteral, j), true
		}
	}
	return "", 0, false
}

// classifyPair is the single pair classifier the batch and incremental passes
// share. The lexical paths propose; contradictions are then adjudicated, so a
// proposal that fails the precision rules is dropped (or, when a numeric
// proposal had overridden a supports verdict, reverts to supports).
func classifyPair(a, b *claimDerived, overlap int, bothEnumerated bool) (pairVerdict, bool) {
	base, baseOK := inferRelationshipFromOverlapWithContext(overlap, a.tokens, a.neg, b.tokens, b.neg, bothEnumerated)
	if baseOK && base == domain.RelationshipTypeContradicts {
		// The precise polarity and value detectors below replace the lexical
		// verdict entirely.
		baseOK = false
	}
	fallback := func() (pairVerdict, bool) {
		if baseOK {
			return pairVerdict{relType: base, strength: supportsStrength(overlap, len(a.tokens), len(b.tokens))}, true
		}
		return pairVerdict{}, false
	}

	av, bv := a.view(), b.view()
	if !av.endpoint || !bv.endpoint {
		return fallback()
	}
	contra := func(detector string, strength float64) (pairVerdict, bool) {
		return pairVerdict{relType: domain.RelationshipTypeContradicts, detector: detector, strength: strength}, true
	}

	if s, ok := polarityConflict(av, bv, overlap); ok {
		return contra(DetectorPolarity, s)
	}
	if !bothEnumerated {
		if det, s, ok := valueConflict(av, bv, overlap); ok {
			return contra(det, s)
		}
		if av.strict == bv.strict && numericDivergesPre(a.nums, a.words, b.nums, b.words) {
			if _, _, j := setDiff(a.words, b.words); j >= minNumericAlignment {
				return contra(DetectorNumeric, strengthOf(priorNumeric, j))
			}
		}
		if !baseOK && av.strict == bv.strict && entityShapeAllows(len(a.tokens), len(b.tokens), overlap) &&
			entityRoleDivergesPre(a.properNouns(), b.properNouns()) {
			return contra(DetectorEntity, priorEntity)
		}
	}
	if !baseOK && temporalDivergesPre(a.asp, a.anchor, b.asp, b.anchor) {
		if _, _, j := setDiff(a.anchor, b.anchor); j >= minTemporalAlignment {
			return contra(DetectorTemporal, strengthOf(priorTemporal, j))
		}
	}
	return fallback()
}

// keepContradictionsWithinCap reports, for each candidate, whether the
// per-claim contradiction cap keeps it. Strongest edges are kept first; an
// edge is kept only while both of its endpoints are under the cap. Every other
// edge type is passed through. key maps a candidate endpoint to a claim
// identity, so batch and incremental passes (whose indices address different
// slices) count correctly.
func keepContradictionsWithinCap(edges []candidateEdge, keep []bool, limit int, fromKey, toKey func(int) string) {
	if limit < 0 {
		return
	}
	order := make([]int, 0, len(edges))
	for k, e := range edges {
		if e.relType == domain.RelationshipTypeContradicts {
			order = append(order, k)
		}
	}
	sortByStrength(order, edges)
	degree := map[string]int{}
	for _, k := range order {
		f, t := fromKey(edges[k].from), toKey(edges[k].to)
		if degree[f] >= limit || degree[t] >= limit {
			keep[k] = false
			continue
		}
		degree[f]++
		degree[t]++
	}
}
