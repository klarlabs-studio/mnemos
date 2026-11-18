package relate

import "testing"

// The two stale_and_superseded probes that ranking could not fix (ADR 0039
// §Limitations): the current and the superseded statement both reach the
// context because no contradiction edge links them, so curation never drops
// the loser. Each pair below is a later statement replacing an earlier one.
func TestContradiction_SupersedingRestatementsAreFlagged(t *testing.T) {
	cases := []struct{ detector, a, b string }{
		{DetectorNumeric,
			"The payments API rate limit is one hundred requests per minute.",
			"The payments API rate limit is one thousand requests per minute."},
		{DetectorNumeric, "The cluster runs three replicas of the API", "The cluster runs five replicas of the API"},
		{DetectorNumeric, "The cluster runs twenty-five worker pods", "The cluster runs 30 worker pods"},
		{DetectorFrame,
			"Deployments are performed manually by an engineer running the release script.",
			"Deployments are performed automatically by the release pipeline on merge to main."},
		{DetectorFrame, "Backups are taken manually every Friday", "Backups are taken automatically by the scheduler"},
		{DetectorValue, "Production deploys are manual for the payments service", "Production deploys are automatic for the payments service"},
	}
	for _, tc := range cases {
		t.Run(tc.a+" / "+tc.b, func(t *testing.T) {
			got := contradictionVerdicts(t, tc.a, tc.b)
			if len(got) != 1 {
				t.Fatalf("want one contradiction edge, got %d", len(got))
			}
			if want := ContradictionDerivedBy(tc.detector); got[0].DerivedBy != want {
				t.Errorf("DerivedBy = %q, want %q", got[0].DerivedBy, want)
			}
		})
	}
}

// What the two new readings must not flag.
func TestContradiction_SupersessionReadingsStayPrecise(t *testing.T) {
	cases := []struct{ class, a, b string }{
		{"spelled and digit forms agree", "The cluster runs three replicas of the API", "The cluster runs 3 replicas of the API"},
		{"spelled forms agree", "The rate limit is one thousand requests per minute", "The rate limit is one thousand requests per minute for every tenant"},
		{"lone 'one' is a determiner, not a count", "One reviewer approved the change to the API gateway", "Two reviewers approved the change to the API gateway config"},
		{"ordering antonyms coexist", "Tests run before the merge to main", "Tests run after the deploy to staging"},
		{"different environment scope", "Backups run automatically in production", "Backups run manually in staging"},
		{"negated manner agrees", "Deployments are not performed manually", "Deployments are performed automatically by the pipeline"},
		{"different counted nouns", "Five commits, tree clean, tests and lint green", "693 tests green, tree clean"},
		{"count against a percent", "Nine mutants total, all killed", "All mutants killed, 100% coverage"},
		{"partitive", "Five of the six fail against the permissive stub", "Six of the seven fail against the stub"},
		{"no shared frame", "Manual review catches style issues", "Automatic formatting fixes style issues"},
	}
	for _, tc := range cases {
		t.Run(tc.class, func(t *testing.T) {
			if got := contradictionVerdicts(t, tc.a, tc.b); len(got) != 0 {
				t.Errorf("%s flagged as a contradiction (%s): %q vs %q", tc.class, got[0].DerivedBy, tc.a, tc.b)
			}
		})
	}
}

func TestSpelledNumerics(t *testing.T) {
	cases := map[string][]numericValue{
		"one hundred requests":                {{value: 100, unit: "raw:requests"}},
		"one thousand requests per minute":    {{value: 1000, unit: "raw:requests"}},
		"twenty-five pods":                    {{value: 25, unit: "raw:pods"}},
		"twenty five pods":                    {{value: 25, unit: "raw:pods"}},
		"two hundred and fifty users":         {{value: 250, unit: "raw:users"}},
		"three million rows":                  {{value: 3_000_000, unit: "raw:rows"}},
		"ten minutes":                         {{value: 600, unit: "time"}},
		"Seven replicas":                      {{value: 7, unit: "raw:replicas"}},
		"one hundred, then two hundred calls": {{value: 200, unit: "raw:calls"}},
		"one of the reviewers":                nil,
		"someone fixed it":                    nil,
		"the tenth time":                      nil,
		"five of the six fail":                nil,
		"six of seven fail":                   nil,
		"all three are false positives":       nil,
		"three four five":                     nil,
	}
	for in, want := range cases {
		got := spelledNumerics(in)
		if len(got) != len(want) {
			t.Errorf("spelledNumerics(%q) = %+v, want %+v", in, got, want)
			continue
		}
		for i := range want {
			if got[i].value != want[i].value || got[i].unit != want[i].unit {
				t.Errorf("spelledNumerics(%q)[%d] = %v %q, want %v %q", in, i, got[i].value, got[i].unit, want[i].value, want[i].unit)
			}
		}
	}
}
