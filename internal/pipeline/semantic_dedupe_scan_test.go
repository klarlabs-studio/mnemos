package pipeline

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/embedding"
)

// scanPool builds a pool with near-duplicate groups spread across types,
// scopes and negation, so some above-threshold pairs cross a partition.
func scanPool(n, dim int, seed uint64) []indexedClaim {
	r := rand.New(rand.NewPCG(seed, seed))
	centers := make([][]float32, n/8+1)
	for i := range centers {
		centers[i] = make([]float32, dim)
		for d := range centers[i] {
			centers[i][d] = float32(r.NormFloat64())
		}
	}
	types := []domain.ClaimType{domain.ClaimTypeFact, domain.ClaimTypeDecision}
	scopes := []domain.Context{{}, {Service: "billing"}}
	pool := make([]indexedClaim, n)
	for i := range pool {
		c := centers[r.IntN(len(centers))]
		v := make([]float32, dim)
		for d := range v {
			v[d] = c[d] + float32(r.NormFloat64()*0.05)
		}
		if i%97 == 0 {
			v = make([]float32, dim) // a zero vector matches nothing
		}
		pool[i] = indexedClaim{
			claim:   domain.Claim{ID: fmt.Sprintf("c%04d", i), Type: types[r.IntN(4)/3], Scope: scopes[r.IntN(5)/4]},
			vec:     v,
			negated: r.IntN(10) == 0,
		}
	}
	return pool
}

// The scan skips pairs mayMerge would refuse on attributes alone and compares
// normalized vectors in parallel. Neither may change which pairs it finds:
// the result must equal comparing every pair with CosineSimilarity.
func TestCandidatePairs_EqualsBruteForce(t *testing.T) {
	const threshold = 0.95
	pool := scanPool(600, 64, 7)

	want := map[[2]int]float32{}
	for i := range pool {
		for j := i + 1; j < len(pool); j++ {
			a, b := pool[i], pool[j]
			if a.negated != b.negated || !a.claim.Scope.Equal(b.claim.Scope) || a.claim.Type != b.claim.Type {
				continue
			}
			sim, _ := embedding.CosineSimilarity(a.vec, b.vec)
			if float64(sim) >= threshold {
				want[[2]int{i, j}] = sim
			}
		}
	}
	if len(want) < 50 {
		t.Fatalf("fixture too sparse: %d candidate pairs", len(want))
	}

	got, err := candidatePairs(context.Background(), pool, threshold)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("found %d pairs, brute force finds %d", len(got), len(want))
	}
	for k, p := range got {
		ws, ok := want[[2]int{p.i, p.j}]
		if !ok {
			t.Fatalf("pair (%d,%d) is not a brute-force candidate", p.i, p.j)
		}
		if d := ws - p.sim; d > 1e-5 || d < -1e-5 {
			t.Errorf("pair (%d,%d): sim %v, brute force %v", p.i, p.j, p.sim, ws)
		}
		// Strongest first, index order on ties: the clustering depends on it.
		if k > 0 {
			prev := got[k-1]
			if prev.sim < p.sim || (prev.sim == p.sim && (prev.i > p.i || (prev.i == p.i && prev.j > p.j))) {
				t.Fatalf("candidates out of order at %d: %+v before %+v", k, prev, p)
			}
		}
	}
}

// A consolidation run under a deadline must stop scanning when it passes,
// not finish an hour of comparisons first.
func TestCandidatePairs_StopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := candidatePairs(ctx, scanPool(400, 16, 3), 0.9); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func BenchmarkCandidatePairs(b *testing.B) {
	pool := scanPool(4000, 768, 11)
	b.ResetTimer()
	for b.Loop() {
		if _, err := candidatePairs(context.Background(), pool, 0.92); err != nil {
			b.Fatal(err)
		}
	}
}
