package brainbench

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	"go.klarlabs.de/mnemos/internal/query"
)

func TestPackContext_FillsInRankOrderAndStopsAtTheFirstItemThatDoesNotFit(t *testing.T) {
	// Each line is "- " + text + "\n", and tokens are chars/4.
	short := strings.Repeat("a", 10) // line = 13 chars
	long := strings.Repeat("b", 40)  // line = 43 chars
	ranked := []string{short, long, short}

	// 13 chars = 3 tokens; 13+43 = 56 chars = 14 tokens.
	packed, tokens := packContext(ranked, 10)
	if !reflect.DeepEqual(packed, []string{short}) {
		t.Fatalf("budget 10: packed %q, want only the first item", packed)
	}
	if tokens != 3 {
		t.Fatalf("budget 10: tokens %d, want 3", tokens)
	}
	// The third item would fit on its own after the first, but the second
	// does not fit, and the block ends there, as BuildContextBlock's does: a
	// lower-ranked item never jumps a higher-ranked one that was cut.

	packed, tokens = packContext(ranked, 14)
	if len(packed) != 2 || tokens != 14 {
		t.Fatalf("budget 14: packed %d items in %d tokens, want 2 in 14 (the boundary is inclusive)", len(packed), tokens)
	}
}

func TestPackContext_EdgeBudgets(t *testing.T) {
	ranked := []string{"alpha beta gamma", "delta"}
	if packed, tokens := packContext(ranked, 0); len(packed) != 0 || tokens != 0 {
		t.Fatalf("budget 0 packed %q (%d tokens), want nothing", packed, tokens)
	}
	if packed, _ := packContext(nil, 100); len(packed) != 0 {
		t.Fatalf("an empty ranking packed %q", packed)
	}
	packed, tokens := packContext(ranked, 1<<20)
	if !reflect.DeepEqual(packed, ranked) {
		t.Fatalf("a huge budget should pack everything, got %q", packed)
	}
	want := query.ApproxTokens(contextLine(ranked[0]) + contextLine(ranked[1]))
	if tokens != want {
		t.Fatalf("tokens %d, want %d: the pack must count with the Context Block's estimate", tokens, want)
	}
}

func TestAssess_SuccessDefinition(t *testing.T) {
	q := question{ID: "q", Expect: "eu-central-1", MustNot: []string{"eu-west-1"}}
	gold := "The API is deployed to eu-central-1"
	stale := "The API is deployed to eu-west-1"
	other := "Alerts route to the payments pager"
	huge := 1 << 20
	// Budget that holds exactly one line of these lengths.
	one := query.ApproxTokens(contextLine(gold)) + 1

	cases := []struct {
		name   string
		ranked []string
		budget int
		want   Outcome
		rank   int
	}{
		{"gold alone", []string{gold, other}, huge, OutcomeSuccess, 1},
		{"gold beside the stale statement", []string{gold, stale}, huge, OutcomeForbidden, 1},
		{"stale statement cut by the budget", []string{gold, stale}, one, OutcomeSuccess, 1},
		{"stale statement first, gold cut", []string{stale, gold}, one, OutcomeGoldOverBudget, 2},
		{"gold never ranked", []string{other, stale}, huge, OutcomeGoldNotRetrieved, 0},
		{"nothing ranked", nil, huge, OutcomeGoldNotRetrieved, 0},
		{"gold matched case-insensitively", []string{strings.ToUpper(gold)}, huge, OutcomeSuccess, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := assess(c.ranked, c.budget, q)
			if a.Outcome != c.want {
				t.Fatalf("outcome %s, want %s (%+v)", a.Outcome, c.want, a)
			}
			if a.GoldRank != c.rank {
				t.Fatalf("gold rank %d, want %d", a.GoldRank, c.rank)
			}
		})
	}

	// A question with no gold-invalid statements succeeds on the gold alone.
	if a := assess([]string{stale, gold}, huge, question{Expect: "eu-central-1"}); a.Outcome != OutcomeSuccess {
		t.Fatalf("no must_not: outcome %s, want success", a.Outcome)
	}
	// Binding is reported when the budget cut the list, and only then.
	if a := assess([]string{gold, stale}, one, q); !a.Binding || a.Packed != 1 || a.Ranked != 2 {
		t.Fatalf("binding cut not reported: %+v", a)
	}
	if a := assess([]string{gold}, huge, q); a.Binding {
		t.Fatalf("binding reported where everything fit: %+v", a)
	}
}

// Every query.Ablation field must have a leave-one-out row that sets it alone.
// A component added to the engine's ablation without a row would otherwise
// never be measured, and the report would still look complete.
func TestAblationPolicies_EveryAblationFieldHasALeaveOneOutRow(t *testing.T) {
	typ := reflect.TypeOf(query.Ablation{})
	covered := map[string]bool{}
	for _, p := range AblationPolicies() {
		if p.Kind != PolicyLeaveOut {
			continue
		}
		v := reflect.ValueOf(p.opts.Ablate)
		var set []string
		for i := 0; i < typ.NumField(); i++ {
			if v.Field(i).Bool() {
				set = append(set, typ.Field(i).Name)
			}
		}
		if len(set) == 1 {
			covered[set[0]] = true
		}
	}
	for i := 0; i < typ.NumField(); i++ {
		if name := typ.Field(i).Name; !covered[name] {
			t.Errorf("query.Ablation.%s has no leave-one-out policy", name)
		}
	}
	if got := AblationPolicies()[0]; got.Name != PolicyDefault || !reflect.DeepEqual(got.opts.Ablate, query.Ablation{}) {
		t.Fatalf("the first policy must be the unablated default, got %+v", got)
	}
}

// End to end on a timeline with a retraction and a supersession: the curation
// ablation must reach the store, so the retracted statement comes back from
// similarity_only and minus_curation and not from the default.
func TestRunAblation_CurationReachesTheStore(t *testing.T) {
	sc, err := ParseScenario([]byte(timelineYAML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	budgets := []int{32, 4096}
	rep, err := RunAblation(context.Background(), []Scenario{sc}, t.TempDir(), budgets)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !rep.Deterministic {
		t.Fatalf("two default copies disagreed on %v", rep.Drifting)
	}
	if len(rep.Policies) != len(AblationPolicies()) {
		t.Fatalf("%d policies reported, want %d", len(rep.Policies), len(AblationPolicies()))
	}
	for _, p := range rep.Policies {
		if len(p.Cells) != len(budgets) {
			t.Fatalf("%s: %d cells, want %d", p.Policy.Name, len(p.Cells), len(budgets))
		}
		for _, c := range p.Cells {
			if c.N != len(rep.Questions) || len(c.Outcomes) != len(rep.Questions) {
				t.Fatalf("%s@%d: %d outcomes for %d questions", p.Policy.Name, c.Budget, len(c.Outcomes), len(rep.Questions))
			}
		}
	}
	if want := (len(rep.Policies) - 1) * len(budgets); len(rep.Effects) != want {
		t.Fatalf("%d effects, want %d (every non-default policy at every budget)", len(rep.Effects), want)
	}

	key := sc.ID + "/now-cause"
	retracted := func(name string) bool {
		for _, p := range rep.Policies {
			if p.Policy.Name == name {
				return anyForbidden(p.Ranked[key], []string{"disk saturation"})
			}
		}
		t.Fatalf("policy %s missing", name)
		return false
	}
	if retracted(PolicyDefault) {
		t.Error("the default recalled the retracted diagnosis")
	}
	for _, name := range []string{PolicySimilarityOnly, "minus_curation"} {
		if !retracted(name) {
			t.Errorf("%s did not recall the retracted diagnosis: the curation ablation did not reach admission", name)
		}
	}

	var buf bytes.Buffer
	if err := rep.WriteHuman(&buf); err != nil {
		t.Fatalf("write human: %v", err)
	}
	for _, want := range []string{PolicyDefault, PolicySimilarityOnly, key, "Limitations:"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("human report lacks %q", want)
		}
	}
}
