package relate

import (
	"crypto/sha256"
	"encoding/hex"
	"maps"
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
}

// CandidateQueryFor builds the query for newClaims.
func CandidateQueryFor(newClaims []domain.Claim) CandidateQuery {
	tokens := map[string]struct{}{}
	cited := map[string]struct{}{}
	for _, c := range newClaims {
		tokenizeContentInto(tokens, c.Text)
		for _, ref := range claimIDRefRE.FindAllString(c.Text, -1) {
			cited[ref] = struct{}{}
		}
	}
	return CandidateQuery{Tokens: slices.Sorted(maps.Keys(tokens)), CitedIDs: slices.Sorted(maps.Keys(cited))}
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
