package relate

import (
	"reflect"
	"testing"
)

// The scope-blind numeric pairs of the ADR 0042 reconciliation set
// (data/eval/reconciliation/pairs.yaml, hw-scope-* and hw-bait-06): the same
// noun counted in two different scopes. relate/v5 filed every one of them as a
// numeric contradiction.
func TestNumericScope_DifferentScopesDoNotContradict(t *testing.T) {
	cases := []struct{ class, a, b string }{
		{"branch", "The main branch has 12 failing tests", "The release branch has 3 failing tests"},
		{"repository", "The api repo has 40 open issues", "The web repo has 12 open issues"},
		{"session", "Yesterday's session produced 5 commits", "Today's session produced 9 commits"},
		{"environment", "The staging cluster runs 3 replicas of the API", "The production cluster runs 12 replicas of the API"},
		{"acronym region", "The EU region serves 200 requests per second", "The US region serves 900 requests per second"},
		{"label number", "Sprint 14 closed 23 tickets", "Sprint 15 closed 31 tickets"},
		{"service", "The Go service has 85% test coverage", "The Python service has 62% test coverage"},
		{"branch, inflected count", "The feature branch is 4 commits ahead of main", "The hotfix branch is 1 commit ahead of main"},
		{"plan", "The free plan allows 3 projects", "The pro plan allows 50 projects"},
		{"environment, version", "Our staging environment uses Postgres 15", "Our production environment uses Postgres 16"},
		{"trailing qualifier", "The login page loads in 1.2 seconds on desktop", "The login page loads in 3.4 seconds on mobile"},
		{"version, different feature", "Version 2 of the API adds pagination", "Version 3 of the API adds filtering"},
		// Shapes from the real-brain measurement (ADR 0044), paraphrased.
		{"package", "web/astro test: Test Files 5 passed", "web/vue test: Test Files 4 passed"},
		{"project", "the coach project uses 74 keys", "the clinic project uses 100 keys"},
		{"pull request label", "PR #104 is merged after all 21 CI checks passed", "PR #107 is merged after all 20 CI checks passed"},
		{"one-sided environment", "The API runs 3 replicas in staging", "The API runs 12 replicas"},
	}
	for _, tc := range cases {
		t.Run(tc.class, func(t *testing.T) {
			if got := contradictionVerdicts(t, tc.a, tc.b); len(got) != 0 {
				t.Errorf("different scopes flagged as a contradiction (%s): %q vs %q", got[0].DerivedBy, tc.a, tc.b)
			}
		})
	}
}

// The same-scope counterparts of the cases above, and the supersessions the
// numeric detector exists to catch: the scope check must leave them alone.
func TestNumericScope_SameScopeStillContradicts(t *testing.T) {
	cases := []struct{ class, a, b string }{
		{"branch", "The main branch has 12 failing tests", "The main branch has 3 failing tests"},
		{"environment", "The staging cluster runs 3 replicas of the API", "The staging cluster runs 6 replicas of the API"},
		{"label number", "Sprint 14 closed 23 tickets", "Sprint 14 closed 31 tickets"},
		{"acronym region", "The EU region serves 200 requests per second", "The EU region serves 900 requests per second"},
		{"trailing qualifier", "The login page loads in 1.2 seconds on desktop", "The login page loads in 3.4 seconds on desktop"},
		{"version", "The deploy uses Postgres 15", "The deploy uses Postgres 16"},
		{"inserted restatement filler", "The payments connection pool size is 50", "The payments connection pool size is now 200"},
		{"spelled counts", "All three lanes are back in the queue", "All five lanes are back in the queue"},
		{"spelled count, status", "Two slices are still running on the cluster", "Three slices are still running on the cluster"},
		{"duration", "The nightly build takes about ten minutes", "The nightly build will take about 35 minutes"},
		{"spelled rate", "The payments API rate limit is one hundred requests per minute.", "The payments API rate limit is one thousand requests per minute."},
	}
	for _, tc := range cases {
		t.Run(tc.class, func(t *testing.T) {
			got := contradictionVerdicts(t, tc.a, tc.b)
			if len(got) != 1 {
				t.Fatalf("want one contradiction edge, got %d: %q vs %q", len(got), tc.a, tc.b)
			}
			if want := ContradictionDerivedBy(DetectorNumeric); got[0].DerivedBy != want {
				t.Errorf("DerivedBy = %q, want %q", got[0].DerivedBy, want)
			}
		})
	}
}

func TestQualifierSwapped(t *testing.T) {
	cases := []struct {
		name string
		a, b []string
		want bool
	}{
		{"identical", []string{"main", "branch", "fail", "test"}, []string{"main", "branch", "fail", "test"}, false},
		{"swap before shared word", []string{"main", "branch"}, []string{"releas", "branch"}, true},
		{"swap after last shared word", []string{"page", "load", "desktop"}, []string{"page", "load", "mobil"}, true},
		{"insertion on one side", []string{"pool", "size"}, []string{"pool", "size", "rais"}, false},
		{"insertions in different slots", []string{"commit", "branch", "race"}, []string{"audit", "clos", "commit", "branch"}, false},
		{"reordered shared words", []string{"disk", "free"}, []string{"free", "disk", "full"}, false},
		{"empty", nil, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := qualifierSwapped(tc.a, tc.b); got != tc.want {
				t.Errorf("qualifierSwapped(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestScopeQualifiers(t *testing.T) {
	cases := map[string][]string{
		"The main branch has 12 failing tests":            {"main", "branch", "fail", "test"},
		"The US region serves 900 requests per second":    {"US", "region", "serv", "request", "per"},
		"Disk: it's back to 97% full (16 GB free)":        {"disk", "full", "free"},
		"The limit was raised to one thousand per minute": {"limit", "per"},
		"Yesterday's session produced 5 commits":          {"yesterday", "session", "produc", "commit"},
	}
	for in, want := range cases {
		got := scopeQualifiers(in)
		for i := range want {
			want[i] = conceptOf(lowerASCII(want[i]))
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("scopeQualifiers(%q) = %v, want %v", in, got, want)
		}
	}
}

func lowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

func TestScopeLabels(t *testing.T) {
	cases := []struct {
		in   string
		want map[string]string
	}{
		{"Sprint 14 closed 23 tickets", map[string]string{"sprint": "14"}},
		{"PR #104 is merged after 21 checks passed", map[string]string{"pr": "104"}},
		{"In 2025 the team shipped 40 releases", map[string]string{"year": "2025"}},
		// The only number is the asserted value, not a label.
		{"The deploy uses version 16", nil},
		{"Sprint 14 is done", nil},
		{"The cluster runs 3 replicas", nil},
	}
	for _, tc := range cases {
		if got := scopeLabels(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("scopeLabels(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
