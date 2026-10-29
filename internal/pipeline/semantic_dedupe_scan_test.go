package pipeline

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

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

// A scan that runs out of budget keeps what it found. Every pair it returns is
// a real candidate, and it says it stopped early, so the caller can merge the
// duplicates it did find instead of losing an hour of comparisons.
func TestCandidatePairsBudget_ReturnsPartialResultWhenTimeRunsOut(t *testing.T) {
	const threshold = 0.95
	pool := scanPool(3000, 64, 5)

	full, err := candidatePairs(context.Background(), pool, threshold)
	if err != nil {
		t.Fatal(err)
	}
	want := map[[2]int]float32{}
	for _, p := range full {
		want[[2]int{p.i, p.j}] = p.sim
	}

	got, stats, err := candidatePairsBudget(context.Background(), pool, threshold, time.Nanosecond)
	if err != nil {
		t.Fatalf("an exhausted budget is not an error: %v", err)
	}
	if !stats.Truncated || stats.RowsScanned >= stats.RowsTotal {
		t.Fatalf("scan should report it stopped early: %+v", stats)
	}
	for _, p := range got {
		if _, ok := want[[2]int{p.i, p.j}]; !ok {
			t.Fatalf("partial scan returned (%d,%d), which a full scan does not", p.i, p.j)
		}
	}
	if len(got) >= len(full) {
		t.Fatalf("partial scan found %d pairs, the full scan %d", len(got), len(full))
	}
}

func TestCandidatePairsBudget_ZeroMeansUnlimited(t *testing.T) {
	pool := scanPool(600, 64, 7)

	want, err := candidatePairs(context.Background(), pool, 0.95)
	if err != nil {
		t.Fatal(err)
	}
	got, stats, err := candidatePairsBudget(context.Background(), pool, 0.95, 0)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Truncated || stats.RowsScanned != stats.RowsTotal {
		t.Fatalf("an unlimited scan covers every row: %+v", stats)
	}
	if len(got) != len(want) {
		t.Fatalf("unlimited scan found %d pairs, candidatePairs %d", len(got), len(want))
	}
}

// Cancellation is still an error: the caller asked to stop, which is not the
// same as the scan choosing to stop.
func TestCandidatePairsBudget_CancellationStillFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := candidatePairsBudget(ctx, scanPool(400, 16, 3), 0.9, time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
