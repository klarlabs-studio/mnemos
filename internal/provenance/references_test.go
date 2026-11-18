package provenance

import (
	"reflect"
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
)

func TestReferences_FindsEachDeterministicSignal(t *testing.T) {
	cases := []struct {
		name string
		ev   domain.Event
		want []Reference
	}{
		{
			name: "source line names a source",
			ev:   domain.Event{Content: "The libfoo 2.3 release fixed the heap overflow.\nSource: the libfoo vendor advisory."},
			want: []Reference{{Kind: domain.DependencyCites, Signal: domain.SignalNamedSource, Text: "Source: the libfoo vendor advisory.", Phrase: "the libfoo vendor advisory"}},
		},
		{
			name: "bulleted copied-from line is a copy",
			ev:   domain.Event{Content: "- **Copied from:** pr_42"},
			want: []Reference{{Kind: domain.DependencyCopies, Signal: domain.SignalNamedSource, Text: "- **Copied from:** pr_42", Phrase: "pr_42"}},
		},
		{
			name: "inline according-to stops at punctuation",
			ev:   domain.Event{Content: "According to the vendor, CVE-2026-1234 is patched."},
			want: []Reference{{Kind: domain.DependencyCites, Signal: domain.SignalNamedSource, Text: "According to the vendor", Phrase: "the vendor"}},
		},
		{
			name: "based on is a derivation",
			ev:   domain.Event{Content: "Based on: incident-review-7"},
			want: []Reference{{Kind: domain.DependencyDerivedFrom, Signal: domain.SignalNamedSource, Text: "Based on: incident-review-7", Phrase: "incident-review-7"}},
		},
		{
			name: "a URL anywhere is a link",
			ev:   domain.Event{Content: "See (https://vendor.example/advisory/42)."},
			want: []Reference{{Kind: domain.DependencyCites, Signal: domain.SignalLink, Text: "https://vendor.example/advisory/42", Exact: []string{"https://vendor.example/advisory/42"}}},
		},
		{
			name: "metadata keys, prefixed or not, comma separated",
			ev: domain.Event{Content: "no markers here", Metadata: map[string]string{
				"input_cites": "ev_1, ev_2", "derived_from": "src-a",
			}},
			want: []Reference{
				{Kind: domain.DependencyDerivedFrom, Signal: domain.SignalMetadata, Text: "derived_from: src-a", Exact: []string{"src-a"}},
				{Kind: domain.DependencyCites, Signal: domain.SignalMetadata, Text: "input_cites: ev_1, ev_2", Exact: []string{"ev_1", "ev_2"}},
			},
		},
		{
			name: "plain prose has no references",
			ev:   domain.Event{Content: "libfoo 2.3 was released on 3 March 2026.\nThe source of the bug was a race."},
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := References(tc.ev)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("References() =\n %+v\nwant\n %+v", got, tc.want)
			}
		})
	}
}

func TestNameRuns_LongestFirstWithEverySpelling(t *testing.T) {
	runs := nameRuns("the libfoo vendor advisory")
	if len(runs) == 0 || len(runs[0]) == 0 {
		t.Fatalf("no runs: %v", runs)
	}
	// The longest run is the whole phrase; the run that resolves the
	// scenario's citation is the two-word one.
	want := map[string]bool{"vendor-advisory": false, "vendor_advisory": false, "vendor advisory": false}
	for _, group := range runs {
		for _, v := range group {
			if _, ok := want[v]; ok {
				want[v] = true
			}
		}
	}
	for v, seen := range want {
		if !seen {
			t.Errorf("spelling %q missing from %v", v, runs)
		}
	}
	for _, group := range runs {
		for _, v := range group {
			if v == "the" {
				t.Errorf("a lone stop word must never be a candidate: %v", runs)
			}
		}
	}
	if got := len(runs[0][0]); got < len(runs[len(runs)-1][0]) {
		t.Errorf("runs are not longest first: %v", runs)
	}
}
