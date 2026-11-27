package ports

import "sort"

// SortClaimHitsDesc orders hits by similarity, highest first. It is stable:
// hits with equal similarity keep the order the store read them in, so every
// backend breaks ties the same way.
//
// It replaces a per-backend insertion sort, which is quadratic. Recall scores
// every candidate belief (the floor is -Inf), so on a 68k-belief brain that
// sort alone took about 10 s of CPU per query and pushed the recall hook past
// its budget.
func SortClaimHitsDesc(hits []ClaimSimilarityHit) {
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Similarity > hits[j].Similarity })
}
