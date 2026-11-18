package provenance

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// fakeLookup answers like a store over a fixed set of episodes.
type fakeLookup []domain.Event

func (f fakeLookup) ListByIDs(_ context.Context, ids []string) ([]domain.Event, error) {
	return f.match(func(e domain.Event) string { return e.ID }, ids), nil
}

func (f fakeLookup) ListBySourceInputIDs(_ context.Context, ids []string) ([]domain.Event, error) {
	return f.match(func(e domain.Event) string { return e.SourceInputID }, ids), nil
}

func (f fakeLookup) ListBySourcePath(_ context.Context, path string) ([]domain.Event, error) {
	return f.match(func(e domain.Event) string { return e.Metadata["input_source_path"] }, []string{path}), nil
}

func (f fakeLookup) match(key func(domain.Event) string, ids []string) []domain.Event {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var out []domain.Event
	for _, e := range f {
		if want[key(e)] {
			out = append(out, e)
		}
	}
	return out
}

func ev(id, source, content string) domain.Event {
	return domain.Event{ID: id, SourceInputID: source, Content: content}
}

func edges(deps []domain.EventDependency) []string {
	out := make([]string, len(deps))
	for i, d := range deps {
		out[i] = fmt.Sprintf("%s->%s %s/%s", d.EventID, d.DependsOn, d.Kind, d.Signal)
	}
	sort.Strings(out)
	return out
}

// The evidence_retraction scenario's documents: only the blog says where its
// sentence came from, and only the blog depends on the advisory.
func TestDetect_RecordsOnlyWhatADocumentSaysItDependsOn(t *testing.T) {
	advisory := ev("advisory", "vendor-advisory", "The libfoo 2.3 release fixed the heap overflow CVE-2026-1234.")
	store := fakeLookup{advisory,
		ev("blog", "security-blog", "The libfoo 2.3 release fixed the heap overflow CVE-2026-1234.\nSource: the libfoo vendor advisory."),
		ev("newsletter", "newsletter", "According to the vendor, CVE-2026-1234 is patched in libfoo 2.3."),
		ev("review", "checkout-review", "The checkout service is no longer exposed to CVE-2026-1234."),
		ev("notes", "release-notes", "libfoo 2.3 was released on 3 March 2026."),
	}
	now := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	var got []domain.EventDependency
	for _, e := range store[1:] {
		deps, err := Detect(context.Background(), store, []domain.Event{e}, now, "tester")
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, deps...)
	}
	if want := []string{"blog->advisory cites/named_source"}; fmt.Sprint(edges(got)) != fmt.Sprint(want) {
		t.Fatalf("dependencies = %v, want %v", edges(got), want)
	}
	d := got[0]
	if d.Reference != "Source: the libfoo vendor advisory." || d.CreatedBy != "tester" || !d.CreatedAt.Equal(now) {
		t.Errorf("dependency = %+v, want the citation line, the actor and the time", d)
	}
	if err := d.Validate(); err != nil {
		t.Errorf("detected dependency is invalid: %v", err)
	}
}

func TestDetect_Resolution(t *testing.T) {
	paged := fakeLookup{}
	for i := 0; i <= MaxEpisodesPerSource; i++ {
		paged = append(paged, ev(fmt.Sprintf("cap-%d", i), "capture", "x"))
	}
	cases := []struct {
		name   string
		stored fakeLookup
		write  []domain.Event
		want   []string
	}{
		{
			name:   "metadata names one episode exactly",
			stored: fakeLookup{ev("a1", "doc-a", "x"), ev("a2", "doc-a", "y")},
			write:  []domain.Event{{ID: "b", SourceInputID: "doc-b", Content: "z", Metadata: map[string]string{"input_derived_from": "a2"}}},
			want:   []string{"b->a2 derived_from/metadata"},
		},
		{
			name:   "a named source covers every episode of that source",
			stored: fakeLookup{ev("a1", "pr_7", "x"), ev("a2", "pr_7", "y")},
			write:  []domain.Event{ev("b", "doc-b", "Quoted from: pr_7")},
			want:   []string{"b->a1 copies/named_source", "b->a2 copies/named_source"},
		},
		{
			name:   "a link resolves through the file path",
			stored: fakeLookup{{ID: "a1", SourceInputID: "in_1", Metadata: map[string]string{"input_source_path": "https://x.example/a"}}},
			write:  []domain.Event{ev("b", "doc-b", "details at https://x.example/a.")},
			want:   []string{"b->a1 cites/link"},
		},
		{
			name:   "a citation in one chunk applies to the whole document",
			stored: fakeLookup{ev("a1", "vendor-advisory", "x")},
			write:  []domain.Event{ev("b1", "blog", "claim one"), ev("b2", "blog", "Source: the vendor advisory")},
			want:   []string{"b1->a1 cites/named_source", "b2->a1 cites/named_source"},
		},
		{
			name:   "a document never depends on itself",
			stored: fakeLookup{ev("b0", "blog", "earlier chunk")},
			write:  []domain.Event{ev("b1", "blog", "Source: blog")},
			want:   []string{},
		},
		{
			name:   "two sources spelled the same way are ambiguous",
			stored: fakeLookup{ev("a1", "vendor-advisory", "x"), ev("a2", "vendor_advisory", "y")},
			write:  []domain.Event{ev("b", "blog", "Source: the vendor advisory")},
			want:   []string{},
		},
		{
			name:   "the longest run wins over a shorter, different source",
			stored: fakeLookup{ev("a1", "vendor-advisory", "x"), ev("a2", "vendor", "y")},
			write:  []domain.Event{ev("b", "blog", "Source: the vendor advisory")},
			want:   []string{"b->a1 cites/named_source"},
		},
		{
			name:   "a source too large to be a specific citation records nothing",
			stored: paged,
			write:  []domain.Event{ev("b", "blog", "Source: capture")},
			want:   []string{},
		},
		{
			name:   "a phrase naming no stored source records nothing",
			stored: fakeLookup{ev("a1", "vendor-advisory", "x")},
			write:  []domain.Event{ev("b", "news", "According to the vendor, it is patched.")},
			want:   []string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lookup := append(append(fakeLookup{}, tc.stored...), tc.write...)
			deps, err := Detect(context.Background(), lookup, tc.write, time.Now(), "t")
			if err != nil {
				t.Fatal(err)
			}
			if got := edges(deps); fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("dependencies = %v, want %v", got, tc.want)
			}
		})
	}
}
