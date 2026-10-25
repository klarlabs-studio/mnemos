package pipeline

import (
	"cmp"
	"context"
	"math"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
)

// candidatePairs returns every pair of pool indexes (i < j) whose cosine
// similarity is at or above threshold and that mayMerge does not refuse on
// attributes alone, strongest first and in index order on ties.
//
// The scan is still all-pairs, so its cost is quadratic in the pool. On a
// 44k-claim brain the original loop took over an hour and found nothing. Three
// changes keep the result identical and cut the cost:
//
//   - Pairs that differ in type, scope or negation, or in vector length, are
//     never compared. mayMerge refuses all of them, so the clustering never
//     used them.
//   - Each vector is normalized once, so a comparison is one dot product
//     instead of a dot product and two norms.
//   - Rows are scanned by half the CPUs, so a sleep pass does not take the
//     whole machine from the session it runs beside.
//
// ctx is checked before every row, so a deadline stops the scan.
func candidatePairs(ctx context.Context, pool []indexedClaim, threshold float64) ([]simPair, error) {
	units := make([][]float32, len(pool))
	partitions := map[scanKey][]int{}
	for i, c := range pool {
		u := unitVector(c.vec)
		if u == nil {
			continue // a zero or empty vector has cosine 0 with everything
		}
		units[i] = u
		k := scanKey{typ: string(c.claim.Type), scope: c.claim.Scope.Key(), negated: c.negated, dim: len(u)}
		partitions[k] = append(partitions[k], i)
	}

	// One work item per row: compare pool[row] with every later member of its
	// partition. Rows are handed out from a shared counter, which balances the
	// long early rows against the short late ones.
	type row struct {
		members []int
		at      int
	}
	var rows []row
	for _, members := range partitions {
		for at := range members[:max(len(members)-1, 0)] {
			rows = append(rows, row{members: members, at: at})
		}
	}

	t := float32(threshold)
	var (
		next    atomic.Int64
		mu      sync.Mutex
		out     []simPair
		scanErr error
		wg      sync.WaitGroup
	)
	for range max(runtime.GOMAXPROCS(0)/2, 1) {
		wg.Go(func() {
			var found []simPair
			for {
				n := int(next.Add(1)) - 1
				if n >= len(rows) {
					break
				}
				if err := ctx.Err(); err != nil {
					mu.Lock()
					scanErr = err
					mu.Unlock()
					break
				}
				r := rows[n]
				i := r.members[r.at]
				for _, j := range r.members[r.at+1:] {
					if sim := dot(units[i], units[j]); sim >= t {
						found = append(found, simPair{i: i, j: j, sim: sim})
					}
				}
			}
			mu.Lock()
			out = append(out, found...)
			mu.Unlock()
		})
	}
	wg.Wait()
	if scanErr != nil {
		return nil, scanErr
	}
	slices.SortFunc(out, func(a, b simPair) int {
		return cmp.Or(cmp.Compare(b.sim, a.sim), cmp.Compare(a.i, b.i), cmp.Compare(a.j, b.j))
	})
	return out, nil
}

// scanKey groups the claims mayMerge allows to merge with each other.
type scanKey struct {
	typ     string
	scope   string
	negated bool
	dim     int
}

// unitVector returns v scaled to length 1, or nil when v has no direction.
func unitVector(v []float32) []float32 {
	var norm float64
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	if norm == 0 {
		return nil
	}
	inv := 1 / math.Sqrt(norm)
	u := make([]float32, len(v))
	for i, x := range v {
		u[i] = float32(float64(x) * inv)
	}
	return u
}

// dot is the inner product of two equal-length vectors, unrolled four ways so
// the loop is not bound by one dependency chain.
func dot(a, b []float32) float32 {
	b = b[:len(a)]
	var s0, s1, s2, s3 float32
	i := 0
	for ; i+4 <= len(a); i += 4 {
		s0 += a[i] * b[i]
		s1 += a[i+1] * b[i+1]
		s2 += a[i+2] * b[i+2]
		s3 += a[i+3] * b[i+3]
	}
	for ; i < len(a); i++ {
		s0 += a[i] * b[i]
	}
	return s0 + s1 + s2 + s3
}
