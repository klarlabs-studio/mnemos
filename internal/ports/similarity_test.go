package ports

import (
	"math/rand"
	"reflect"
	"testing"
)

// insertionSortDesc is the sort every backend used before SortClaimHitsDesc.
// The replacement must order hits exactly as it did, ties included.
func insertionSortDesc(hits []ClaimSimilarityHit) {
	for i := 1; i < len(hits); i++ {
		for j := i; j > 0 && hits[j].Similarity > hits[j-1].Similarity; j-- {
			hits[j-1], hits[j] = hits[j], hits[j-1]
		}
	}
}

func TestSortClaimHitsDesc_MatchesTheInsertionSortItReplaces(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for _, n := range []int{0, 1, 2, 17, 500} {
		hits := make([]ClaimSimilarityHit, n)
		for i := range hits {
			// Few distinct values, so ties are common and order among them matters.
			hits[i] = ClaimSimilarityHit{ClaimID: string(rune('a'+i%26)) + string(rune('0'+i/26%10)), Similarity: float64(rng.Intn(5)) / 4}
		}
		want := make([]ClaimSimilarityHit, n)
		copy(want, hits)
		insertionSortDesc(want)
		SortClaimHitsDesc(hits)
		if !reflect.DeepEqual(hits, want) {
			t.Fatalf("n=%d: order differs from the insertion sort", n)
		}
	}
}
