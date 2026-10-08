package store

import (
	"math/rand/v2"
	"slices"
)

// ChooseSample returns min(n, len(ids)) of ids, chosen uniformly by a partial
// Fisher-Yates shuffle seeded by seed, sorted. ids must be sorted, so the
// choice depends only on the set and the seed. Every backend's sampler uses it
// so they agree on the sample.
func ChooseSample(ids []string, n int, seed uint64) []string {
	if n >= len(ids) {
		return slices.Clone(ids)
	}
	pool := slices.Clone(ids)
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	for i := 0; i < n; i++ {
		j := i + rng.IntN(len(pool)-i)
		pool[i], pool[j] = pool[j], pool[i]
	}
	chosen := pool[:n]
	slices.Sort(chosen)
	return chosen
}
