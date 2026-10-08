package relate

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// The candidate set is exact: a detection pass over only the claims
// CandidateQuery.Matches accepts produces the same relationships, in the same
// order with the same ids, as a pass over the whole corpus in candidate order.
// That is what lets a store answer the query instead of loading every claim on
// each write. The corpora carry all three reasons a claim is needed — shared
// tokens, a citation from a new claim, a test result on a shared requirement —
// and the test fails if any of them stops being exercised.
func TestCandidateQuery_IsExact(t *testing.T) {
	shapes := []corpusOptions{
		{VocabSize: 150, Zipf: 1.0, RankOffset: 5, MinTokens: 5, MaxTokens: 18, ShortShare: 0.10, NumShare: 0.20, NegShare: 0.12, AspectShar: 0.15, ProperShar: 0.10},
		{VocabSize: 4000, Zipf: 1.0, RankOffset: 120, MinTokens: 5, MaxTokens: 18, ShortShare: 0.10, NumShare: 0.20, NegShare: 0.12, AspectShar: 0.15, ProperShar: 0.10},
		{VocabSize: 60, Zipf: 0.7, MinTokens: 2, MaxTokens: 3, ShortShare: 1.0, NumShare: 0.35, NegShare: 0.25, AspectShar: 0.35, ProperShar: 0.4},
	}
	var excluded, cites, tests int
	for si, shape := range shapes {
		for _, seed := range []int64{21, 22} {
			opts := shape
			opts.Size, opts.Seed = 800, seed
			existing := generateCorpus(opts)
			rng := rand.New(rand.NewPCG(uint64(seed), uint64(si)))
			base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			for i := range existing {
				// Coarse timestamps so created_at ties occur and the id
				// tie-break is exercised.
				existing[i].CreatedAt = base.Add(time.Duration(rng.IntN(50)) * time.Hour)
				if i%97 == 0 {
					existing[i].Type = domain.ClaimTypeTestResult
					existing[i].TestRequirementRef = fmt.Sprintf("REQ-%d", i%3)
					existing[i].TestPassCount, existing[i].TestFailCount = rng.IntN(3), rng.IntN(3)
				}
			}
			batch := opts
			batch.Size, batch.Seed = 12, seed+900
			newClaims := generateCorpus(batch)
			for i := range newClaims {
				newClaims[i].ID = fmt.Sprintf("cl_new_%04d", i)
				newClaims[i].CreatedAt = base.Add(100 * time.Hour)
				if i%4 == 0 { // cite an arbitrary existing claim
					newClaims[i].Text += " see " + existing[rng.IntN(len(existing))].ID
				}
				if i == 1 {
					newClaims[i].Type = domain.ClaimTypeTestResult
					newClaims[i].TestRequirementRef = "REQ-1"
					newClaims[i].TestPassCount = 2
				}
			}

			all := slices.Clone(existing)
			SortCandidates(all)
			q := CandidateQueryFor(newClaims)
			var subset []domain.Claim
			for _, c := range all {
				if q.Matches(c) {
					subset = append(subset, c)
				}
			}
			excluded += len(all) - len(subset)
			cites += len(q.CitedIDs)
			for _, c := range subset {
				if IsTestConflictCandidate(c) {
					tests++
				}
			}

			want, err := equivalenceEngine().DetectIncremental(newClaims, all)
			if err != nil {
				t.Fatal(err)
			}
			got, err := equivalenceEngine().DetectIncremental(newClaims, subset)
			if err != nil {
				t.Fatal(err)
			}
			assertSameRelationships(t, fmt.Sprintf("shape %d seed %d", si, seed), want, got)
		}
	}
	if excluded == 0 || cites == 0 || tests == 0 {
		t.Fatalf("vacuous: excluded=%d cited=%d test candidates=%d; every reason must be exercised", excluded, cites, tests)
	}
}

// TokenizerVersion must change whenever the tokenizer's output can: a store
// keeps tokens written under it, and a stale set drops candidates silently.
func TestTokenizerVersionTracksTheTokenizer(t *testing.T) {
	if TokenizerVersion == "" || TokenizerVersion != tokenizerVersion() {
		t.Fatalf("TokenizerVersion %q is not stable", TokenizerVersion)
	}
	stopWords["zzprobe"] = struct{}{}
	changed := tokenizerVersion()
	delete(stopWords, "zzprobe")
	if changed == TokenizerVersion {
		t.Error("adding a stop word did not change the tokenizer version")
	}
	// The probe must reach the stemmer: stemming changes its token set.
	if got := SortedContentTokens(tokenizerProbe); len(got) == 0 || slices.Contains(got, "deployed") {
		t.Errorf("probe tokens %v do not exercise the stemmer", got)
	}
}
