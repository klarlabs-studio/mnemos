package relate

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

func budgetCorpus() []domain.Claim {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var all []domain.Claim
	add := func(id, text string) {
		all = append(all, domain.Claim{ID: id, Text: text, Type: domain.ClaimTypeFact, CreatedAt: at})
	}
	for i := 0; i < 100; i++ {
		add(fmt.Sprintf("c-common-%03d", i), "kafka broker")
	}
	for i := 0; i < 10; i++ {
		add(fmt.Sprintf("c-mid-%02d", i), "zookeeper quorum")
	}
	for i := 0; i < 3; i++ {
		add(fmt.Sprintf("c-rare-%d", i), "isr shrink")
	}
	add("cl_cited", "unrelated words entirely")
	all = append(all, domain.Claim{ID: "c-test", Text: "suite", Type: domain.ClaimTypeTestResult, TestRequirementRef: "REQ-1", CreatedAt: at})
	return all
}

func idsWithPrefix(cs []domain.Claim, prefix string) int {
	n := 0
	for _, c := range cs {
		if len(c.ID) >= len(prefix) && c.ID[:len(prefix)] == prefix {
			n++
		}
	}
	return n
}

// The budget takes a claim's tokens rarest first, each with all its postings,
// and skips a token that would overflow it whole. Citations and test results
// are never budgeted.
func TestCandidateBudget_RarestFirstWholeTokens(t *testing.T) {
	all := budgetCorpus()
	newClaim := domain.Claim{ID: "n1", Text: "kafka zookeeper isr see cl_cited"}
	cases := []struct {
		budget                   int
		common, mid, rare, skips int
	}{
		{budget: 13, common: 0, mid: 10, rare: 3, skips: 1},     // rare(3)+mid(10) fit; common(100) does not
		{budget: 12, common: 0, mid: 0, rare: 3, skips: 2},      // mid would reach 13: skipped whole, not cut to 9
		{budget: 1000, common: 100, mid: 10, rare: 3, skips: 0}, // everything fits
		{budget: -1, common: 100, mid: 10, rare: 3, skips: 0},   // unlimited
	}
	for _, c := range cases {
		q := CandidateQueryFor([]domain.Claim{newClaim})
		q.Budget = c.budget
		got, skipped := q.SelectCandidates(all)
		if idsWithPrefix(got, "c-common") != c.common || idsWithPrefix(got, "c-mid") != c.mid || idsWithPrefix(got, "c-rare") != c.rare || skipped != c.skips {
			t.Errorf("budget %d: common=%d mid=%d rare=%d skipped=%d; want %d/%d/%d/%d", c.budget,
				idsWithPrefix(got, "c-common"), idsWithPrefix(got, "c-mid"), idsWithPrefix(got, "c-rare"), skipped,
				c.common, c.mid, c.rare, c.skips)
		}
		if idsWithPrefix(got, "cl_cited") != 1 || idsWithPrefix(got, "c-test") != 1 {
			t.Errorf("budget %d dropped the cited claim or the test result", c.budget)
		}
	}
}

// Each new claim plans its own budget: a batch where one claim has only
// common tokens does not starve another of its rare ones.
func TestCandidateBudget_IsPerNewClaim(t *testing.T) {
	all := budgetCorpus()
	q := CandidateQueryFor([]domain.Claim{{ID: "n1", Text: "kafka broker"}, {ID: "n2", Text: "isr shrink"}})
	q.Budget = 50
	got, skipped := q.SelectCandidates(all)
	if idsWithPrefix(got, "c-rare") != 3 || idsWithPrefix(got, "c-common") != 0 || skipped != 2 {
		t.Fatalf("per-claim budget: rare=%d common=%d skipped=%d; want 3, 0, 2", idsWithPrefix(got, "c-rare"), idsWithPrefix(got, "c-common"), skipped)
	}
}

// Unlimited, the budgeted selection is the exact candidate set: the one
// TestCandidateQuery_IsExact proves relate-equivalent.
func TestCandidateBudget_UnlimitedIsTheExactSet(t *testing.T) {
	all := generateCorpus(defaultCorpusOptions(800, 61))
	batch := generateCorpus(defaultCorpusOptions(8, 62))
	for i := range batch {
		batch[i].ID = fmt.Sprintf("cl_new_%d", i)
	}
	q := CandidateQueryFor(batch)
	q.Budget = -1
	got, _ := q.SelectCandidates(all)
	var want []domain.Claim
	for _, c := range all {
		if q.Matches(c) {
			want = append(want, c)
		}
	}
	SortCandidates(want)
	gotIDs, wantIDs := make([]string, len(got)), make([]string, len(want))
	for i := range got {
		gotIDs[i] = got[i].ID
	}
	for i := range want {
		wantIDs[i] = want[i].ID
	}
	if !slices.Equal(gotIDs, wantIDs) {
		t.Fatalf("unlimited selection differs from the exact set (%d vs %d)", len(got), len(want))
	}
}
