package relate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"go.klarlabs.de/mnemos/internal/domain"
)

// SortedContentTokens is ContentTokens as a sorted slice. They are exactly the
// tokens the candidate index posts a claim under, so a store that persists
// them can answer "which claims share a token with these?" without loading
// the corpus.
func SortedContentTokens(text string) []string {
	set := make(map[string]struct{}, 16)
	tokenizeContentInto(set, text)
	return slices.Sorted(maps.Keys(set))
}

// CandidateQuery is what DetectIncremental needs from the corpus for a batch
// of new claims. A pass run over exactly the claims a store returns for it
// produces the same relationships as a pass over the whole corpus:
//
//   - Tokens: every existing claim sharing one of these can pair with a new
//     claim; a claim sharing none cannot produce an edge (see candidateIndex
//     for the per-detector argument).
//   - CitedIDs: claim ids the new claims cite, whose existence decides the
//     citation edges.
//   - and, independently of the query, every existing test_result claim with
//     a requirement reference, which test-conflict detection compares across
//     the whole corpus.
type CandidateQuery struct {
	Tokens   []string
	CitedIDs []string
	// ClaimTokens holds each new claim's own tokens, which the candidate
	// budget plans per claim.
	ClaimTokens [][]string
	// Budget caps the token-matched candidates per new claim (see
	// DefaultCandidateBudget). 0 means the default; < 0 means unlimited, the
	// exact candidate set.
	Budget int
}

// DefaultCandidateBudget is how many token-matched candidates each new claim
// gathers at most (ADR 0028).
//
// Previous behaviour: every stored claim sharing any content token with a new
// claim was a candidate. Domain words ("service", "team", "pipeline") are
// shared by much of any brain, so a write on a 1M-belief brain related against
// ~500k candidates: 7 s per Remember, linear in the corpus.
//
// New behaviour: a claim's tokens are taken rarest first (fewest stored claims
// carrying them, ties by token), each with its whole posting list, while the
// running total stays within the budget. A token whose postings would push it
// over is skipped whole, never cut part-way. Claims the new ones cite and test
// results always stay candidates. Only pairs that share nothing but common
// tokens go unevaluated: the pairs least able to reach the overlap a
// relationship needs.
const DefaultCandidateBudget = 5000

// BudgetBound is how far a store must count a token's document frequency for
// PlanTokens to plan exactly: budget+1, since any token above the budget is
// skipped whatever its exact count. 0 means unlimited: no counting needed.
func (q CandidateQuery) BudgetBound() int {
	if b := q.budget(); b >= 0 {
		return b + 1
	}
	return 0
}

func (q CandidateQuery) budget() int {
	if q.Budget == 0 {
		return DefaultCandidateBudget
	}
	return q.Budget
}

// PlanTokens returns the tokens whose postings the candidate set takes, and how
// many tokens the budget skipped, given each token's document frequency (how
// many stored claims carry it). Every store computes candidates through it, so
// they all take the same tokens.
func (q CandidateQuery) PlanTokens(df map[string]int) (take []string, skipped int) {
	budget := q.budget()
	if budget < 0 {
		return slices.Clone(q.Tokens), 0
	}
	taken := map[string]bool{}
	for _, toks := range q.ClaimTokens {
		ordered := slices.Clone(toks)
		slices.SortFunc(ordered, func(a, b string) int {
			if c := df[a] - df[b]; c != 0 {
				return c
			}
			return strings.Compare(a, b)
		})
		total := 0
		for _, tok := range ordered {
			if total+df[tok] > budget {
				skipped++
				continue
			}
			total += df[tok]
			taken[tok] = true
		}
	}
	return slices.Sorted(maps.Keys(taken)), skipped
}

// CandidateQueryFor builds the query for newClaims.
func CandidateQueryFor(newClaims []domain.Claim) CandidateQuery {
	tokens := map[string]struct{}{}
	cited := map[string]struct{}{}
	perClaim := make([][]string, 0, len(newClaims))
	for _, c := range newClaims {
		own := map[string]struct{}{}
		tokenizeContentInto(own, c.Text)
		for tok := range own {
			tokens[tok] = struct{}{}
		}
		perClaim = append(perClaim, slices.Sorted(maps.Keys(own)))
		for _, ref := range claimIDRefRE.FindAllString(c.Text, -1) {
			cited[ref] = struct{}{}
		}
	}
	return CandidateQuery{Tokens: slices.Sorted(maps.Keys(tokens)), CitedIDs: slices.Sorted(maps.Keys(cited)), ClaimTokens: perClaim}
}

// IsTestConflictCandidate reports whether c is one a store must return
// regardless of tokens: a test_result claim test-conflict detection can pair.
func IsTestConflictCandidate(c domain.Claim) bool {
	return c.Type == domain.ClaimTypeTestResult && c.TestRequirementRef != ""
}

// Matches is the definition of the candidate set: whether existing claim c
// must be handed to DetectIncremental for the query's new claims. A store
// implementing the candidate port returns exactly the claims this accepts; the
// in-memory store uses it directly and the equivalence tests use it as the
// oracle.
func (q CandidateQuery) Matches(c domain.Claim) bool {
	if IsTestConflictCandidate(c) {
		return true
	}
	if _, ok := slices.BinarySearch(q.CitedIDs, c.ID); ok {
		return true
	}
	for tok := range ContentTokens(c.Text) {
		if _, ok := slices.BinarySearch(q.Tokens, tok); ok {
			return true
		}
	}
	return false
}

// SelectCandidates applies the candidate definition, budget included, to a
// whole corpus held in memory: the memory store's implementation and the
// oracle the SQL implementations are tested against. It returns the
// candidates in SortCandidates order and the number of skipped tokens.
func (q CandidateQuery) SelectCandidates(all []domain.Claim) ([]domain.Claim, int) {
	tokensOf := make([]map[string]struct{}, len(all))
	df := map[string]int{}
	wanted := map[string]bool{}
	for _, t := range q.Tokens {
		wanted[t] = true
	}
	for i, c := range all {
		tokensOf[i] = ContentTokens(c.Text)
		for tok := range tokensOf[i] {
			if wanted[tok] {
				df[tok]++
			}
		}
	}
	take, skipped := q.PlanTokens(df)
	takeSet := map[string]bool{}
	for _, t := range take {
		takeSet[t] = true
	}
	var out []domain.Claim
	for i, c := range all {
		keep := IsTestConflictCandidate(c)
		if !keep {
			_, keep = slices.BinarySearch(q.CitedIDs, c.ID)
		}
		if !keep {
			for tok := range tokensOf[i] {
				if takeSet[tok] {
					keep = true
					break
				}
			}
		}
		if keep {
			out = append(out, c)
		}
	}
	SortCandidates(out)
	return out, skipped
}

// TraceCandidateBudget writes one MNEMOS_RELATE_TRACE line when a candidate
// budget skipped tokens, so an operator can see the budget at work.
func TraceCandidateBudget(newClaims, candidates, skippedTokens int) {
	traceIncrementalOnce.Do(initTrace)
	if !traceIncrementalOn || skippedTokens == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "relate.candidates new=%d candidates=%d skipped_tokens=%d\n", newClaims, candidates, skippedTokens)
}

// SortCandidates puts claims in the order a candidate pass must see them:
// created_at, then id. ListAll orders by created_at alone and leaves ties to
// the database; candidate passes fix the tie so the supports budget's
// tie-break (earlier claim wins) is deterministic.
func SortCandidates(claims []domain.Claim) {
	slices.SortStableFunc(claims, func(a, b domain.Claim) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
}

// TokenizerVersion fingerprints the tokenizer's behaviour. A store persisting
// ContentTokens records it and rebuilds when it differs: tokens written by a
// different tokenizer would make candidates go missing silently, which is the
// one failure an index must never have. It hashes the stop and negation lists
// and the tokens of a probe text that exercises the stemmer, so editing either
// list or the stemmer changes it without anyone remembering to bump a constant.
var TokenizerVersion = tokenizerVersion()

// tokenizerProbe covers the stemmer's suffix rules, punctuation trimming,
// negation and stop words. TestTokenizerVersionTracksTheTokenizer guards it.
const tokenizerProbe = "Deployed deploying deploys deployment services service's (migrated) " +
	"migrations queries queried running runs ran happily happiness relational " +
	"not never no the a an is of payments payment's paid paying latency-sensitive v2.3 42ms"

func tokenizerVersion() string {
	h := sha256.New()
	for _, list := range []map[string]struct{}{stopWords, negationWords} {
		h.Write([]byte(strings.Join(slices.Sorted(maps.Keys(list)), ",")))
		h.Write([]byte{0})
	}
	h.Write([]byte(strings.Join(SortedContentTokens(tokenizerProbe), ",")))
	return "relate-tokens/" + hex.EncodeToString(h.Sum(nil))[:16]
}
