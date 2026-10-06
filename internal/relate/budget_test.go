package relate

import (
	"fmt"
	"slices"
	"sort"
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
)

func relKey(r domain.Relationship) string {
	return string(r.Type) + "|" + r.FromClaimID + "|" + r.ToClaimID
}

// expectBudgeted derives, independently of keepWithinBudget, what a budget of k
// must keep from the unbudgeted output: every non-supports edge, and per source
// the k supports edges with the highest Jaccard overlap of content tokens, ties
// to the earlier target in the corpus.
func expectBudgeted(unbudgeted []domain.Relationship, texts map[string]string, order map[string]int, k int) map[string]bool {
	keep := map[string]bool{}
	bySource := map[string][]domain.Relationship{}
	for _, r := range unbudgeted {
		if r.Type != domain.RelationshipTypeSupports {
			keep[relKey(r)] = true
			continue
		}
		bySource[r.FromClaimID] = append(bySource[r.FromClaimID], r)
	}
	jaccard := func(a, b string) float64 {
		ta, _ := contentTokensAndPolarity(texts[a])
		tb, _ := contentTokensAndPolarity(texts[b])
		inter := 0
		for t := range ta {
			if _, ok := tb[t]; ok {
				inter++
			}
		}
		return float64(inter) / float64(len(ta)+len(tb)-inter)
	}
	for _, rs := range bySource {
		sort.SliceStable(rs, func(i, j int) bool {
			si, sj := jaccard(rs[i].FromClaimID, rs[i].ToClaimID), jaccard(rs[j].FromClaimID, rs[j].ToClaimID)
			if si != sj {
				return si > sj
			}
			return order[rs[i].ToClaimID] < order[rs[j].ToClaimID]
		})
		for i := 0; i < len(rs) && i < k; i++ {
			keep[relKey(rs[i])] = true
		}
	}
	return keep
}

// assertBudgetApplied checks budgeted against the unbudgeted output: exactly the
// expected edges survive, in their original relative order.
func assertBudgetApplied(t *testing.T, label string, unbudgeted, budgeted []domain.Relationship, want map[string]bool) {
	t.Helper()
	var wantSeq []string
	for _, r := range unbudgeted {
		if want[relKey(r)] {
			wantSeq = append(wantSeq, relKey(r))
		}
	}
	if len(budgeted) != len(wantSeq) {
		t.Fatalf("%s: budget kept %d edges, want %d", label, len(budgeted), len(wantSeq))
	}
	for i, r := range budgeted {
		if relKey(r) != wantSeq[i] {
			t.Fatalf("%s: edge %d = %s, want %s", label, i, relKey(r), wantSeq[i])
		}
	}
}

// The supports budget on the write path: against a corpus whose hot vocabulary
// gives each new claim far more than the budget of supports candidates, the
// budgeted pass keeps exactly the strongest few per claim and every
// contradiction, and nothing else changes.
func TestDetectIncremental_SupportsBudgetKeepsTheStrongest(t *testing.T) {
	opts := corpusOptions{Size: 600, VocabSize: 120, Zipf: 1.0, Seed: 41, MinTokens: 4, MaxTokens: 14,
		ShortShare: 0.15, NumShare: 0.25, NegShare: 0.15, AspectShar: 0.2, ProperShar: 0.15}
	existing := generateCorpus(opts)
	batch := opts
	batch.Size, batch.Seed = 15, 4141
	newClaims := generateCorpus(batch)
	for i := range newClaims {
		newClaims[i].ID = fmt.Sprintf("cl_new_%04d", i)
	}
	texts, order := map[string]string{}, map[string]int{}
	for i, c := range existing {
		texts[c.ID], order[c.ID] = c.Text, i
	}
	for _, c := range newClaims {
		texts[c.ID] = c.Text
	}

	for _, k := range []int{1, 5, DefaultSupportsBudget} {
		unbudgeted, err := equivalenceEngine().WithSupportsBudget(-1).DetectIncremental(newClaims, existing)
		if err != nil {
			t.Fatal(err)
		}
		budgeted, stats, err := equivalenceEngine().WithSupportsBudget(k).DetectIncrementalWithStats(newClaims, existing)
		if err != nil {
			t.Fatal(err)
		}
		want := expectBudgeted(unbudgeted, texts, order, k)
		assertBudgetApplied(t, fmt.Sprintf("budget %d", k), unbudgeted, budgeted, want)
		if stats.SupportsOverBudget != len(unbudgeted)-len(budgeted) {
			t.Errorf("budget %d: SupportsOverBudget = %d, want %d", k, stats.SupportsOverBudget, len(unbudgeted)-len(budgeted))
		}
		if k == DefaultSupportsBudget && stats.SupportsOverBudget == 0 {
			t.Fatal("no candidate exceeded the default budget; the corpus no longer exercises it")
		}
		contradictions := 0
		for _, r := range budgeted {
			if r.Type == domain.RelationshipTypeContradicts {
				contradictions++
			}
		}
		if k == 1 && contradictions == 0 {
			t.Fatal("the corpus produced no contradictions, so 'contradictions are never budgeted' went untested")
		}
	}
}

// The same budget holds within a single batch (Detect), which is what a large
// document ingest goes through before it is related to the corpus.
func TestDetect_SupportsBudgetKeepsTheStrongest(t *testing.T) {
	claims := generateCorpus(corpusOptions{Size: 300, VocabSize: 60, Zipf: 1.0, Seed: 43, MinTokens: 4, MaxTokens: 12,
		ShortShare: 0.1, NumShare: 0.2, NegShare: 0.15, AspectShar: 0.2, ProperShar: 0.1})
	texts, order := map[string]string{}, map[string]int{}
	for i, c := range claims {
		texts[c.ID], order[c.ID] = c.Text, i
	}
	unbudgeted, err := equivalenceEngine().WithSupportsBudget(-1).Detect(claims)
	if err != nil {
		t.Fatal(err)
	}
	budgeted, err := equivalenceEngine().Detect(claims)
	if err != nil {
		t.Fatal(err)
	}
	if len(budgeted) >= len(unbudgeted) {
		t.Fatalf("budget dropped nothing (%d vs %d); the corpus no longer exercises it", len(budgeted), len(unbudgeted))
	}
	assertBudgetApplied(t, "Detect", unbudgeted, budgeted, expectBudgeted(unbudgeted, texts, order, DefaultSupportsBudget))
}

func TestSupportsBudget_Defaults(t *testing.T) {
	if got := (Engine{}).budget(); got != DefaultSupportsBudget {
		t.Errorf("zero-value engine budget = %d, want the default %d", got, DefaultSupportsBudget)
	}
	if got := NewEngine().budget(); got != DefaultSupportsBudget {
		t.Errorf("NewEngine budget = %d, want %d", got, DefaultSupportsBudget)
	}
	edges := []candidateEdge{
		{from: 0, to: 3, relType: domain.RelationshipTypeSupports, strength: 0.5},
		{from: 0, to: 1, relType: domain.RelationshipTypeSupports, strength: 0.5},
		{from: 0, to: 2, relType: domain.RelationshipTypeContradicts, strength: 0.1},
		{from: 0, to: 4, relType: domain.RelationshipTypeSupports, strength: 0.9},
	}
	if got := keepWithinBudget(edges, -1); got[0] != true || got[1] != true || got[3] != true {
		t.Errorf("negative budget dropped edges: %v", got)
	}
	// Budget 2: 0.9 survives, then the 0.5 tie goes to the lower target (1, not 3).
	want := []bool{false, true, true, true}
	got := keepWithinBudget(edges, 2)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("keepWithinBudget(…, 2) = %v, want %v", got, want)
		}
	}
}

// Pruning a brain written without the budget leaves exactly the edges the
// budgeted write path would have written: the pruner and the detection pass
// rank and break ties identically.
func TestSupportsPruner_MatchesTheBudgetedWritePath(t *testing.T) {
	opts := corpusOptions{Size: 600, VocabSize: 120, Zipf: 1.0, Seed: 47, MinTokens: 4, MaxTokens: 14,
		ShortShare: 0.15, NumShare: 0.25, NegShare: 0.15, AspectShar: 0.2, ProperShar: 0.15}
	existing := generateCorpus(opts)
	batch := opts
	batch.Size, batch.Seed = 15, 4747
	newClaims := generateCorpus(batch)
	for i := range newClaims {
		newClaims[i].ID = fmt.Sprintf("cl_new_%04d", i)
	}
	unbudgeted, err := equivalenceEngine().WithSupportsBudget(-1).DetectIncremental(newClaims, existing)
	if err != nil {
		t.Fatal(err)
	}
	budgeted, err := equivalenceEngine().DetectIncremental(newClaims, existing)
	if err != nil {
		t.Fatal(err)
	}

	p := NewSupportsPruner(append(slices.Clone(existing), newClaims...), 0)
	drop := map[string]bool{}
	for _, c := range newClaims {
		for _, id := range p.OverBudget(c.ID, unbudgeted) {
			drop[id] = true
		}
	}
	if len(drop) == 0 {
		t.Fatal("pruner dropped nothing; the corpus no longer exceeds the budget")
	}
	var pruned []string
	for _, r := range unbudgeted {
		if !drop[r.ID] {
			pruned = append(pruned, relKey(r))
		}
	}
	var want []string
	for _, r := range budgeted {
		want = append(want, relKey(r))
	}
	if !slices.Equal(pruned, want) {
		t.Fatalf("pruned brain has %d edges, budgeted write path %d; they must be identical", len(pruned), len(want))
	}
	unlimited := NewSupportsPruner(append(slices.Clone(existing), newClaims...), -1)
	for _, c := range newClaims {
		if unlimited.OverBudget(c.ID, unbudgeted) != nil {
			t.Fatal("a negative budget must prune nothing")
		}
	}
}
