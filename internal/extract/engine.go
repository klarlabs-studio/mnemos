package extract

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// Engine extracts claims from domain events using heuristic NLP.
type Engine struct {
	now    func() time.Time
	nextID func() (string, error)
}

// sentenceSplitRE splits text on sentence boundaries without breaking decimal
// numbers, version strings, or percentages like "3.5%". It splits on:
//   - ! or ? or newline (always)
//   - . followed by whitespace (sentence boundary)
//   - . at end of string
var sentenceSplitRE = regexp.MustCompile(`[!?\n]+|\.\s+|\.\s*$`)

// NewEngine returns an Engine with default ID generation and time source.
func NewEngine() Engine {
	return Engine{
		now:    time.Now,
		nextID: newClaimID,
	}
}

// Extract derives deduplicated claims and their evidence links from the given
// events. Rule-based extraction is pure CPU work with no I/O, so the context is
// accepted for interface symmetry with LLMEngine and ports.ExtractionEngine but
// is not consulted.
func (e Engine) Extract(_ context.Context, events []domain.Event) ([]domain.Claim, []domain.ClaimEvidence, error) {
	claims := make([]domain.Claim, 0, len(events))
	evidence := make([]domain.ClaimEvidence, 0, len(events))
	seen := map[string]struct{}{}

	for _, event := range events {
		content := strings.TrimSpace(event.Content)
		if content == "" {
			continue
		}

		candidates := splitCandidates(content)
		for _, candidate := range candidates {
			dedupeKey := normalizeForDedupe(candidate)
			if dedupeKey == "" {
				continue
			}
			if _, ok := seen[dedupeKey]; ok {
				continue
			}
			seen[dedupeKey] = struct{}{}

			claimID, err := e.nextID()
			if err != nil {
				return nil, nil, fmt.Errorf("generate claim id: %w", err)
			}

			claimType := inferClaimType(candidate)
			claim := domain.Claim{
				ID:         claimID,
				Text:       candidate,
				Type:       claimType,
				Confidence: inferConfidence(candidate, claimType),
				Status:     domain.ClaimStatusActive,
				CreatedAt:  e.now().UTC(),
			}
			if err := claim.Validate(); err != nil {
				return nil, nil, fmt.Errorf("validate extracted claim: %w", err)
			}

			ce := domain.ClaimEvidence{ClaimID: claim.ID, EventID: event.ID}
			if err := ce.Validate(); err != nil {
				return nil, nil, fmt.Errorf("validate claim evidence: %w", err)
			}

			claims = append(claims, claim)
			evidence = append(evidence, ce)
		}
	}

	markContestedClaims(claims)

	return claims, evidence, nil
}

// ExtractClaims is a convenience wrapper around Extract that returns only the claims.
func (e Engine) ExtractClaims(ctx context.Context, events []domain.Event) ([]domain.Claim, error) {
	claims, _, err := e.Extract(ctx, events)
	if err != nil {
		return nil, err
	}
	return claims, nil
}

func inferClaimType(text string) domain.ClaimType {
	lower := strings.ToLower(text)
	decisionSignals := []string{"decide", "decision", "we will", "approved", "chosen", "plan is", "we should"}
	hypothesisSignals := []string{"might", "maybe", "hypothesis", "assume", "could", "possibly", "likely", "expected"}

	decisionScore := scoreSignals(lower, decisionSignals)
	hypothesisScore := scoreSignals(lower, hypothesisSignals)

	if decisionScore >= hypothesisScore && decisionScore > 0 {
		return domain.ClaimTypeDecision
	}
	if hypothesisScore > 0 {
		return domain.ClaimTypeHypothesis
	}
	return domain.ClaimTypeFact
}

func inferConfidence(text string, claimType domain.ClaimType) float64 {
	lower := strings.ToLower(text)
	var value float64

	switch claimType {
	case domain.ClaimTypeDecision:
		value = 0.88
	case domain.ClaimTypeHypothesis:
		value = 0.62
	default:
		value = 0.8
	}

	if hasAny(lower, []string{"approximately", "about", "around", "maybe", "might", "likely"}) {
		value -= 0.08
	}
	if hasDigits(lower) {
		value += 0.05
	}
	if hasAny(lower, []string{"confirmed", "observed", "measured", "reported"}) {
		value += 0.04
	}

	return clamp(value, 0.5, 0.95)
}

// markdownBoldRE matches **bold** and __bold__ markers.
var markdownBoldRE = regexp.MustCompile(`\*\*([^*]+)\*\*|__([^_]+)__`)

// markdownStrikethroughRE matches ~~strikethrough~~ markers.
var markdownStrikethroughRE = regexp.MustCompile(`~~([^~]+)~~`)

// cleanMarkdown strips markdown formatting from text, returning plain content.
func cleanMarkdown(text string) string {
	s := text

	// Strip bold markers: **text** → text
	s = markdownBoldRE.ReplaceAllString(s, "$1$2")
	// Strip strikethrough: ~~text~~ → text
	s = markdownStrikethroughRE.ReplaceAllString(s, "$1")
	// Strip checkbox markers: - [x] or - [ ]
	s = strings.ReplaceAll(s, "- [x] ", "")
	s = strings.ReplaceAll(s, "- [ ] ", "")
	s = strings.ReplaceAll(s, "[x] ", "")
	s = strings.ReplaceAll(s, "[ ] ", "")
	// Strip leading bullet/list markers: - , * , > , 1.
	s = strings.TrimSpace(s)
	if len(s) > 2 && (s[0] == '-' || s[0] == '*' || s[0] == '>') && s[1] == ' ' {
		s = strings.TrimSpace(s[2:])
	}
	// Strip leading numbered list: "1. ", "2. ", etc.
	if len(s) > 3 && s[0] >= '0' && s[0] <= '9' {
		dotIdx := strings.Index(s, ". ")
		if dotIdx > 0 && dotIdx <= 3 {
			s = strings.TrimSpace(s[dotIdx+2:])
		}
	}
	// Strip link syntax: [text](url) → text
	for {
		start := strings.Index(s, "[")
		if start == -1 {
			break
		}
		mid := strings.Index(s[start:], "](")
		if mid == -1 {
			break
		}
		end := strings.Index(s[start+mid:], ")")
		if end == -1 {
			break
		}
		linkText := s[start+1 : start+mid]
		s = s[:start] + linkText + s[start+mid+end+1:]
	}

	return strings.TrimSpace(s)
}

func splitCandidates(content string) []string {
	// Pre-clean markdown formatting.
	cleaned := cleanMarkdown(content)

	raw := sentenceSplitRE.Split(cleaned, -1)
	out := make([]string, 0, len(raw))
	for _, piece := range raw {
		candidate := strings.TrimSpace(piece)
		candidate = strings.TrimRight(candidate, ".")
		// cleanMarkdown only strips the first leading list marker of the
		// full content; per-sentence trimming catches subsequent list
		// items ("- Authentication moved to Auth0" → "Authentication
		// moved to Auth0") so the isStructuralNoise check sees the
		// cleaned form.
		candidate = trimLeadingListMarker(candidate)
		if len(candidate) < 4 {
			continue
		}
		if isStructuralNoise(candidate) {
			continue
		}
		out = append(out, candidate)
	}
	return out
}

func trimLeadingListMarker(s string) string {
	trimmed := strings.TrimSpace(s)
	if len(trimmed) > 2 && (trimmed[0] == '-' || trimmed[0] == '*' || trimmed[0] == '>') && trimmed[1] == ' ' {
		return strings.TrimSpace(trimmed[2:])
	}
	return trimmed
}

// contentWordCount returns the number of meaningful words (excluding stop words
// and short tokens) for use as a minimum claim quality threshold.
func contentWordCount(text string) int {
	shortStops := map[string]struct{}{
		"the": {}, "a": {}, "an": {}, "is": {}, "are": {}, "was": {}, "were": {},
		"be": {}, "been": {}, "to": {}, "of": {}, "in": {}, "for": {}, "on": {},
		"and": {}, "or": {}, "but": {}, "it": {}, "we": {}, "by": {}, "as": {},
	}
	count := 0
	for _, w := range strings.Fields(strings.ToLower(text)) {
		w = strings.Trim(w, ",.;:!?()[]{}\"'-")
		if len(w) < 2 {
			continue
		}
		if _, ok := shortStops[w]; ok {
			continue
		}
		count++
	}
	return count
}

// salutationPrefixes match common email openings and sign-offs that leak
// into extraction on real-world documents.
var salutationPrefixes = []string{
	"hi ", "hello ", "hey ", "dear ",
	"regards", "thanks,", "thank you,", "cheers,", "best,", "sincerely,",
}

// labelPrefixRE matches "Label: value" patterns where the label is a short
// noun-like phrase — typical of metadata rows like "Status: Accepted",
// "Severity: High", "Date: March 15", "Author: X", "Feature: User Auth",
// "Bug: Slow Responses", "ADR-001: Choose Queue", "Objective 1: Improve X".
// The label side (before `:`) is 1–3 words of letters/digits/hyphens.
var labelPrefixRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9 -]{0,40}:\s+\S`)

// isShortTitleCaseHeader reports whether text is a 2–4-word title-case
// phrase with no finite verb structure — i.e., a section header like
// "Database Schema Design" or "Q2 OKRs" rather than a declarative claim.
// Words that are purely numeric/digit tokens are allowed (e.g., "Sprint 24
// Retrospective"), as are all-caps tokens (e.g., "API Endpoints").
func isShortTitleCaseHeader(text string) bool {
	fields := strings.Fields(text)
	if len(fields) < 2 || len(fields) > 4 {
		return false
	}
	for _, w := range fields {
		// Allow numeric tokens anywhere.
		isNum := true
		for _, r := range w {
			if r < '0' || r > '9' {
				isNum = false
				break
			}
		}
		if isNum {
			continue
		}
		// First character must be an uppercase letter. Tokens that start
		// with a non-letter (punctuation, symbol) disqualify this as a
		// title-case phrase.
		first := w[0]
		if first < 'A' || first > 'Z' {
			return false
		}
	}
	return true
}

// isStructuralNoise returns true for markdown artifacts, formatting, and
// header/label phrases that should not be extracted as claims.
func isStructuralNoise(text string) bool {
	trimmed := strings.TrimSpace(text)
	lower := strings.ToLower(trimmed)

	// Markdown headers: # Title, ## Section, etc.
	if strings.HasPrefix(trimmed, "#") {
		return true
	}
	// Empty after stripping list markers.
	stripped := strings.TrimLeft(trimmed, "-*> ")
	if stripped == "" {
		return true
	}
	// Horizontal rules (--- or more).
	noSpaces := strings.ReplaceAll(trimmed, " ", "")
	if len(noSpaces) >= 3 && strings.Trim(noSpaces, "-") == "" {
		return true
	}
	// Code fences.
	if strings.HasPrefix(trimmed, "```") {
		return true
	}
	// Label-only lines: "Deliverables:", "Status:", "Goal:" etc.
	if strings.HasSuffix(trimmed, ":") && len(strings.Fields(trimmed)) <= 2 {
		return true
	}
	// Short "Label: value" metadata lines (≤ 5 words). Catches section
	// headers and KV metadata without biting legitimate claims that
	// contain colons — those are almost always longer.
	words := strings.Fields(trimmed)
	if len(words) <= 5 && labelPrefixRE.MatchString(trimmed) {
		return true
	}
	// Pipe-separated rows — tables ("DAU | 10K | 15K") and email signature
	// lines ("Alice | VP Engineering" / "email | phone").
	if strings.Count(trimmed, "|") >= 2 {
		return true
	}
	// Single-pipe signature lines: "Name | Title" or "email | phone".
	// Requires a pipe AND either an email-looking token or a phone-looking
	// token to avoid clobbering legitimate content.
	if strings.Count(trimmed, "|") == 1 && (strings.Contains(trimmed, "@") || strings.Contains(trimmed, "+1-")) {
		return true
	}
	// JSON fragments — the splitter sometimes picks up inline JSON objects.
	if strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}") {
		return true
	}
	// Title-case short headers: 2–4 words where each word starts with an
	// uppercase letter (or is a digit token). Catches "Q2 OKRs", "Sprint
	// 24 Retrospective", "Database Schema Design", "Performance
	// Requirements", "API Endpoints", "Project Phoenix Status Update".
	if isShortTitleCaseHeader(trimmed) {
		return true
	}
	// Salutations and sign-offs — only drop if the whole candidate is the
	// greeting itself. A long message that happens to start with "hey" still
	// contains claims after the greeting, and we want those.
	if len(words) <= 4 {
		for _, p := range salutationPrefixes {
			if strings.HasPrefix(lower, p) {
				return true
			}
		}
	}
	// Note: repeat-marker filtering (As mentioned, To reiterate, In summary)
	// was tried but conflicts with the robustness eval ground truth, which
	// treats paraphrased restatements as three separate claims. We keep
	// those extractions and let downstream dedup or ranking decide.
	// Too few content words (after stop-word removal) to be a meaningful claim.
	if contentWordCount(trimmed) < 2 {
		return true
	}
	return false
}

func normalizeForDedupe(text string) string {
	normalized := strings.ToLower(strings.TrimSpace(text))
	replacer := strings.NewReplacer(",", "", ".", "", ";", "", ":", "", "!", "", "?", "", "\t", " ")
	normalized = replacer.Replace(normalized)
	normalized = strings.Join(strings.Fields(normalized), " ")
	return normalized
}

// contestedAnchorRatio is the share of the shorter claim's tokens that two
// opposite-polarity claims must have in common before the negation counts as a
// disagreement rather than a coincidence. Half is deliberately permissive —
// opposite polarity is genuine evidence — while still excluding the
// two-stray-tokens pairings that dominated bulk document ingestion.
const contestedAnchorRatio = 0.5

func markContestedClaims(claims []domain.Claim) {
	cores := make([]string, len(claims))
	negs := make([]bool, len(claims))
	for i := range claims {
		core, neg := polarityCore(normalizeForDedupe(claims[i].Text))
		cores[i] = core
		negs[i] = neg
	}

	for i := 0; i < len(claims); i++ {
		if cores[i] == "" {
			continue
		}
		for j := i + 1; j < len(claims); j++ {
			if cores[j] == "" {
				continue
			}
			if negs[i] == negs[j] {
				// Same polarity: detect value-divergence contradictions.
				// E.g. "We will use React" vs "We will use Vue" share most
				// tokens but differ on the key value word.
				overlap := tokenOverlap(cores[i], cores[j])
				if overlap < 2 {
					continue
				}
				tokensI := len(strings.Fields(cores[i]))
				tokensJ := len(strings.Fields(cores[j]))
				minLen := tokensI
				if tokensJ < minLen {
					minLen = tokensJ
				}
				if minLen >= 4 && overlap >= minLen-1 {
					claims[i].Status = domain.ClaimStatusContested
					claims[j].Status = domain.ClaimStatusContested
				}
				continue
			}
			// Opposite polarity. A negation is a real disagreement signal, but
			// it only means anything when the two claims are about the SAME
			// thing — otherwise any negated sentence contests every other
			// sentence that happens to share a couple of words.
			//
			// The bar used to be a flat "share >= 2 tokens", with no
			// same-subject check at all. Bulk-ingesting structured documents
			// (a decisions.md of terse technical bullets, all drawing on the
			// same vocabulary) marked 53% of claims contested — 70% in the
			// worst project — pairing unrelated bullets like "Frame: nonce LE,
			// supp-size includes size-byte+marker" with "Next Session Should:
			// M1 remainder". Same failure shape as the relate detectors: an
			// overlap heuristic with no subject anchor.
			//
			// Require the shared tokens to be a real share of the shorter
			// claim, mirroring the same-polarity branch above. The canonical
			// case still fires: "revenue decreased after launch" vs "revenue
			// (not) decrease after launch" overlap on most of their tokens.
			overlap := tokenOverlap(cores[i], cores[j])
			if overlap < 2 {
				continue
			}
			minLen := min(len(strings.Fields(cores[i])), len(strings.Fields(cores[j])))
			if minLen == 0 || float64(overlap)/float64(minLen) < contestedAnchorRatio {
				continue
			}
			claims[i].Status = domain.ClaimStatusContested
			claims[j].Status = domain.ClaimStatusContested
		}
	}
}

func polarityCore(text string) (string, bool) {
	negWords := map[string]struct{}{"not": {}, "no": {}, "never": {}, "without": {}, "cannot": {}, "cant": {}}
	stopWords := map[string]struct{}{"did": {}, "does": {}, "do": {}, "is": {}, "are": {}, "was": {}, "were": {}, "be": {}, "been": {}, "being": {}, "the": {}, "a": {}, "an": {}}
	words := strings.Fields(text)
	core := make([]string, 0, len(words))
	neg := false
	for _, w := range words {
		if _, ok := negWords[w]; ok {
			neg = true
			continue
		}
		if w == "didnt" || w == "didn't" || w == "isnt" || w == "isn't" {
			neg = true
			continue
		}
		if _, ok := stopWords[w]; ok {
			continue
		}
		core = append(core, stemWord(w))
	}
	return strings.Join(core, " "), neg
}

func stemWord(word string) string {
	if len(word) > 5 && strings.HasSuffix(word, "ed") {
		return strings.TrimSuffix(word, "ed")
	}
	if len(word) > 5 && strings.HasSuffix(word, "es") {
		return strings.TrimSuffix(word, "es")
	}
	if len(word) > 4 && strings.HasSuffix(word, "s") {
		return strings.TrimSuffix(word, "s")
	}
	return word
}

func scoreSignals(text string, signals []string) int {
	score := 0
	for _, signal := range signals {
		if strings.Contains(text, signal) {
			score++
		}
	}
	return score
}

func hasAny(text string, words []string) bool {
	for _, w := range words {
		if strings.Contains(text, w) {
			return true
		}
	}
	return false
}

func hasDigits(text string) bool {
	for _, r := range text {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return false
}

func clamp(value, minValue, maxValue float64) float64 {
	return math.Max(minValue, math.Min(maxValue, value))
}

func tokenOverlap(a, b string) int {
	aSet := map[string]struct{}{}
	for _, token := range strings.Fields(a) {
		aSet[token] = struct{}{}
	}
	overlap := 0
	for _, token := range strings.Fields(b) {
		if _, ok := aSet[token]; ok {
			overlap++
		}
	}
	return overlap
}

func newClaimID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "cl_" + hex.EncodeToString(buf), nil
}
