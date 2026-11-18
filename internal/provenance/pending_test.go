package provenance

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

func TestCanonicalKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"vendor-advisory", "vendor advisory"},
		{"Vendor_Advisory", "vendor advisory"},
		{"  the   vendor\tadvisory ", "the vendor advisory"},
		{"notes/2026.md", "notes 2026 md"},
		{"https://x.example/a", "https: x example a"},
		{"inline:ev_1", "inline:ev 1"},
		{"--", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := CanonicalKey(tc.in); got != tc.want {
			t.Errorf("CanonicalKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestEpisodeKeys(t *testing.T) {
	e := domain.Event{ID: "ev_1", SourceInputID: "Vendor-Advisory", Metadata: map[string]string{"input_source_path": "https://x.example/a"}}
	got := EpisodeKeys(e)
	want := []string{"ev 1", "vendor advisory", "https: x example a"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("EpisodeKeys = %q, want %q", got, want)
	}
}

func pendingOf(p []domain.PendingReference) map[string][]string {
	out := map[string][]string{}
	for _, r := range p {
		out[r.EventID] = append(out[r.EventID], r.LookupKey)
	}
	for _, keys := range out {
		sort.Strings(keys)
	}
	return out
}

// ADR 0038, late citations: a reference that resolved to no stored episode is
// reported as pending under the canonical keys a later source would carry. A
// reference that resolved, or that is ambiguous and so can never resolve, is
// not pending.
func TestDetectAll_PendingKeys(t *testing.T) {
	now := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		stored fakeLookup
		write  []domain.Event
		want   map[string][]string
	}{
		{
			name:   "metadata ids not stored are pending in canonical form",
			stored: fakeLookup{ev("a1", "doc-a", "x")},
			write:  []domain.Event{{ID: "b", SourceInputID: "doc-b", Metadata: map[string]string{"cites": "a1, Missing_Doc.v2"}}},
			want:   map[string][]string{"b": {"missing doc v2"}},
		},
		{
			name:  "an unresolved url is pending in canonical form",
			write: []domain.Event{ev("b", "doc-b", "see https://x.example/Report.")},
			want:  map[string][]string{"b": {"https: x example report"}},
		},
		{
			name:  "a named source not stored is pending under every word run",
			write: []domain.Event{ev("b", "doc-b", "Source: the vendor advisory")},
			want: map[string][]string{"b": {
				"advisory", "the vendor", "the vendor advisory", "vendor", "vendor advisory",
			}},
		},
		{
			name:  "a named identifier written whole is pending in canonical form too",
			write: []domain.Event{ev("b", "doc-b", "Source: inline:ev_9")},
			want:  map[string][]string{"b": {"9", "ev", "ev 9", "inline", "inline ev", "inline ev 9", "inline:ev 9"}},
		},
		{
			name:   "a resolved reference is not pending",
			stored: fakeLookup{ev("a1", "vendor-advisory", "x")},
			write:  []domain.Event{ev("b", "doc-b", "Source: the vendor advisory\nderived from https://x.example/a")},
			want:   map[string][]string{"b": {"https: x example a"}},
		},
		{
			name:   "an ambiguous reference is not pending",
			stored: fakeLookup{ev("a1", "vendor-advisory", "x"), ev("a2", "vendor_advisory", "y")},
			write:  []domain.Event{ev("b", "doc-b", "Source: the vendor advisory")},
			want:   map[string][]string{},
		},
		{
			name:  "the pending key is stamped on the citing chunk only",
			write: []domain.Event{ev("b1", "blog", "claim one"), ev("b2", "blog", "Source: pr_7")},
			want:  map[string][]string{"b2": {"7", "pr", "pr 7"}},
		},
		{
			name:   "a document's own keys are never pending",
			stored: fakeLookup{ev("b0", "blog", "earlier chunk")},
			write:  []domain.Event{ev("b1", "blog", "Source: blog\ncites nothing")},
			want:   map[string][]string{},
		},
		{
			name:   "an exact id naming the document's own earlier chunk is not pending",
			stored: fakeLookup{ev("b0", "blog", "earlier chunk")},
			write:  []domain.Event{{ID: "b1", SourceInputID: "blog", Metadata: map[string]string{"cites": "b0"}}},
			want:   map[string][]string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lookup := append(append(fakeLookup{}, tc.stored...), tc.write...)
			got, err := DetectAll(context.Background(), lookup, tc.write, now, "t")
			if err != nil {
				t.Fatal(err)
			}
			if p := pendingOf(got.Pending); fmt.Sprint(p) != fmt.Sprint(tc.want) {
				t.Errorf("pending = %v, want %v", p, tc.want)
			}
			for _, p := range got.Pending {
				if !p.CreatedAt.Equal(now) {
					t.Errorf("pending %+v not stamped with the write time", p)
				}
				if err := p.Validate(); err != nil {
					t.Errorf("pending %+v invalid: %v", p, err)
				}
			}
		})
	}
}

// A pending key is what a later source's own key must equal: the source the
// citation named, written later, carries one of the keys the citation left.
func TestPendingKeys_MatchTheLaterSourcesKeys(t *testing.T) {
	cases := []struct {
		citing domain.Event
		later  domain.Event
	}{
		{ev("b", "blog", "Source: the vendor advisory"), ev("a", "vendor-advisory", "x")},
		{ev("b", "blog", "Source: the vendor advisory"), ev("a", "vendor_advisory", "x")},
		{ev("b", "blog", "details at https://x.example/a."), domain.Event{ID: "a", SourceInputID: "in_1", Metadata: map[string]string{"input_source_path": "https://x.example/a"}}},
		{domain.Event{ID: "b", SourceInputID: "blog", Metadata: map[string]string{"copied_from": "ev_src"}}, ev("ev_src", "inline:ev_src", "x")},
	}
	for _, tc := range cases {
		got, err := DetectAll(context.Background(), fakeLookup{tc.citing}, []domain.Event{tc.citing}, time.Now(), "t")
		if err != nil {
			t.Fatal(err)
		}
		keys := map[string]bool{}
		for _, p := range got.Pending {
			keys[p.LookupKey] = true
		}
		matched := false
		for _, k := range EpisodeKeys(tc.later) {
			matched = matched || keys[k]
		}
		if !matched {
			t.Errorf("pending keys %v of %q share none with the later source's keys %v", keys, tc.citing.Content, EpisodeKeys(tc.later))
		}
		// And once stored, the citation resolves to it.
		deps, err := Detect(context.Background(), fakeLookup{tc.citing, tc.later}, []domain.Event{tc.citing}, time.Now(), "t")
		if err != nil || len(deps) != 1 || deps[0].DependsOn != tc.later.ID {
			t.Errorf("after the source is stored, %q resolves to %v (%v), want %s", tc.citing.Content, edges(deps), err, tc.later.ID)
		}
	}
}
