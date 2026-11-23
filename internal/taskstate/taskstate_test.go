package taskstate

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// fakeProbe answers the long-term-memory questions from fixed tables.
type fakeProbe struct {
	known       map[string]string   // text -> claim id it duplicates
	contradicts map[string][]string // text -> claim ids it contradicts
	missing     map[string]bool     // claim ids that do not exist
	err         error
}

func (p fakeProbe) Assess(_ context.Context, text string) (Verdict, error) {
	if p.err != nil {
		return Verdict{}, p.err
	}
	return Verdict{KnownAs: p.known[text], Contradicts: p.contradicts[text]}, nil
}

func (p fakeProbe) Missing(_ context.Context, ids []string) ([]string, error) {
	var out []string
	for _, id := range ids {
		if p.missing[id] {
			out = append(out, id)
		}
	}
	return out, nil
}

func TestRenderParse_RoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		state State
	}{
		{"empty", State{}},
		{"every kind", State{Items: []Item{
			{Kind: KindQuestion, Text: "Is the cache warmed on deploy?"},
			{Kind: KindGoal, Text: "Ship the billing migration"},
			{Kind: KindHypothesis, Text: "Latency comes from the N+1 query", RestsOn: []string{"c1", "c2"}},
			{Kind: KindConstraint, Text: "Never touch the prod database directly", Against: []string{"c9"}},
			{Kind: KindCommitment, Text: "Write the ADR [draft] before Friday"},
		}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(Render(tt.state))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(got.Items) != len(tt.state.Items) {
				t.Fatalf("items = %d, want %d", len(got.Items), len(tt.state.Items))
			}
			// Render is placement-ordered; a second round trip must be stable.
			if Render(got) != Render(tt.state) {
				t.Fatalf("render not stable:\n%s\nvs\n%s", Render(got), Render(tt.state))
			}
		})
	}
}

// Placement: goals and constraints open the block whatever order they arrived.
func TestRender_GoalsAndConstraintsComeFirst(t *testing.T) {
	s := State{Items: []Item{
		{Kind: KindQuestion, Text: "q1"},
		{Kind: KindHypothesis, Text: "h1"},
		{Kind: KindConstraint, Text: "c1"},
		{Kind: KindCommitment, Text: "m1"},
		{Kind: KindGoal, Text: "g1"},
	}}
	lines := strings.Split(Render(s), "\n")
	var kinds []string
	for _, l := range lines[1:] {
		kinds = append(kinds, l[strings.Index(l, "[")+1:strings.Index(l, "]")])
	}
	want := "goal constraint commitment hypothesis question"
	if got := strings.Join(kinds, " "); got != want {
		t.Fatalf("order = %q, want %q", got, want)
	}
}

func TestParse_RejectsForeignContent(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"no header", "- [goal] x"},
		{"unknown kind", Header + "\n- [mood] x"},
		{"not an item", Header + "\nfree text"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse(tt.content); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestFit_EvictsLowestPriorityOldestFirst(t *testing.T) {
	long := strings.Repeat("x", 40)
	s := State{Items: []Item{
		{Kind: KindConstraint, Text: "constraint " + long},
		{Kind: KindQuestion, Text: "question one " + long},
		{Kind: KindHypothesis, Text: "hypothesis " + long},
		{Kind: KindQuestion, Text: "question two " + long},
		{Kind: KindGoal, Text: "goal " + long},
	}}
	tests := []struct {
		name        string
		policy      Eviction
		wantEvicted []string
	}{
		{"priority drops questions then hypotheses", EvictByPriority, []string{"question one", "question two", "hypothesis"}},
		{"oldest is plain FIFO", EvictOldest, []string{"constraint", "question one", "hypothesis"}},
	}
	limit := len(Render(State{Items: s.Items[:2]}))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, evicted := Fit(s, limit, tt.policy)
			if len(Render(got)) > limit {
				t.Fatalf("render %d chars > limit %d", len(Render(got)), limit)
			}
			if len(evicted) != len(tt.wantEvicted) {
				t.Fatalf("evicted %d items, want %d: %+v", len(evicted), len(tt.wantEvicted), evicted)
			}
			for i, w := range tt.wantEvicted {
				if !strings.HasPrefix(evicted[i].Text, w) {
					t.Errorf("evicted[%d] = %q, want prefix %q", i, evicted[i].Text, w)
				}
			}
		})
	}
}

func TestGate(t *testing.T) {
	goal := Item{Kind: KindGoal, Text: "Migrate the billing service to Postgres"}
	base := State{Items: []Item{goal}}
	probe := fakeProbe{
		known: map[string]string{
			"The billing service runs on MySQL today":                  "c_known",
			"The office coffee machine is broken":                      "c_coffee",
			"Postgres billing service migration must keep invoice ids": "c_rel",
		},
		contradicts: map[string][]string{"The office coffee machine is broken": {"c_fixed"}},
		missing:     map[string]bool{"c_ghost": true},
	}
	tests := []struct {
		name        string
		state       State
		item        Item
		admit       bool
		wantReason  Reason
		wantAgainst []string
		wantErr     error
	}{
		{"goal always admitted", State{}, Item{Kind: KindGoal, Text: "Ship it"}, true, ReasonGoal, nil, nil},
		{"novel item admitted", base, Item{Kind: KindConstraint, Text: "Never deploy on Fridays"}, true, ReasonNovel, nil, nil},
		{"known but relevant admitted", base, Item{Kind: KindConstraint, Text: "Postgres billing service migration must keep invoice ids"}, true, ReasonRelevant, nil, nil},
		{"known but on-goal admitted", base, Item{Kind: KindHypothesis, Text: "The billing service runs on MySQL today"}, true, ReasonRelevant, nil, nil},
		{"known, irrelevant, surprising admitted", base, Item{Kind: KindHypothesis, Text: "The office coffee machine is broken"}, true, ReasonSurprising, []string{"c_fixed"}, nil},
		{"duplicate of held item rejected", base, Item{Kind: KindGoal, Text: "migrate the billing service to postgres."}, false, ReasonDuplicate, nil, nil},
		{"unknown claim reference refused", base, Item{Kind: KindHypothesis, Text: "x is y", RestsOn: []string{"c_ghost"}}, false, "", nil, ErrUnknownClaim},
		{"empty text refused", base, Item{Kind: KindQuestion, Text: "  "}, false, "", nil, ErrInvalidItem},
		{"bad kind refused", base, Item{Kind: "mood", Text: "x"}, false, "", nil, ErrInvalidItem},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := Gate(context.Background(), tt.state, tt.item, probe)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if d.Admit != tt.admit {
				t.Fatalf("admit = %v, want %v (%+v)", d.Admit, tt.admit, d)
			}
			if !d.Has(tt.wantReason) {
				t.Errorf("reasons = %v, want %q", d.Reasons, tt.wantReason)
			}
			if strings.Join(d.Against, ",") != strings.Join(tt.wantAgainst, ",") {
				t.Errorf("against = %v, want %v", d.Against, tt.wantAgainst)
			}
		})
	}
}

// A known, irrelevant, unsurprising item is the one the gate exists to keep out.
func TestGate_RejectsKnownIrrelevantUnsurprising(t *testing.T) {
	s := State{Items: []Item{{Kind: KindGoal, Text: "Migrate the billing service to Postgres"}}}
	probe := fakeProbe{known: map[string]string{"The office coffee machine is broken": "c1"}}
	d, err := Gate(context.Background(), s, Item{Kind: KindQuestion, Text: "The office coffee machine is broken"}, probe)
	if err != nil {
		t.Fatal(err)
	}
	if d.Admit || !d.Has(ReasonKnown) {
		t.Fatalf("decision = %+v, want rejected as known", d)
	}
}

func TestGate_ProbeErrorPropagates(t *testing.T) {
	_, err := Gate(context.Background(), State{}, Item{Kind: KindQuestion, Text: "q"}, fakeProbe{err: errors.New("boom")})
	if err == nil {
		t.Fatal("want error")
	}
}

func TestBuildHandoff(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	s := State{Items: []Item{
		{Kind: KindGoal, Text: "Ship the migration"},
		{Kind: KindConstraint, Text: "Never deploy on Fridays", Against: []string{"c_fri"}},
		{Kind: KindHypothesis, Text: "Latency is the N+1 query", RestsOn: []string{"c_q"}},
		{Kind: KindQuestion, Text: "Is the cache warm?"},
		{Kind: KindCommitment, Text: "Write the ADR"},
	}}
	h, ok := BuildHandoff("agent", "s1", s, now, "tester")
	if !ok {
		t.Fatal("want a handoff")
	}
	if err := h.Event.Validate(); err != nil {
		t.Fatalf("event: %v", err)
	}
	if !strings.Contains(h.Event.Content, "Is the cache warm?") {
		t.Error("open questions must survive in the episode")
	}
	if len(h.Claims) != 4 {
		t.Fatalf("claims = %d, want 4 (questions are not assertions)", len(h.Claims))
	}
	for _, c := range h.Claims {
		if c.Lifecycle != domain.ClaimLifecycleCandidate {
			t.Errorf("%q lifecycle = %q, want candidate", c.Text, c.Lifecycle)
		}
		if err := c.Validate(); err != nil {
			t.Errorf("%q: %v", c.Text, err)
		}
	}
	if len(h.Links) != 4 {
		t.Errorf("links = %d, want one per claim", len(h.Links))
	}
	types := map[domain.RelationshipType]int{}
	for _, r := range h.Relationships {
		if err := r.Validate(); err != nil {
			t.Errorf("rel: %v", err)
		}
		types[r.Type]++
	}
	if types[domain.RelationshipTypeDerivedFrom] != 1 || types[domain.RelationshipTypeContradicts] != 1 {
		t.Errorf("relationships = %v, want one derived_from and one contradicts", types)
	}
	// Idempotent: the same state hands off the same ids.
	again, _ := BuildHandoff("agent", "s1", s, now.Add(time.Hour), "tester")
	if again.Event.ID != h.Event.ID || again.Claims[0].ID != h.Claims[0].ID {
		t.Error("handoff ids must be deterministic so a retried handoff does not duplicate")
	}
	if _, ok := BuildHandoff("agent", "s1", State{}, now, "tester"); ok {
		t.Error("an empty state hands nothing off")
	}
}

func TestLabel(t *testing.T) {
	tests := []struct {
		session, label string
	}{
		{"", "task_state:current"},
		{"abc", "task_state:abc"},
	}
	for _, tt := range tests {
		if got := Label(tt.session); got != tt.label {
			t.Errorf("Label(%q) = %q, want %q", tt.session, got, tt.label)
		}
		s, ok := SessionOf(tt.label)
		if !ok || Label(s) != tt.label {
			t.Errorf("SessionOf(%q) = %q, %v", tt.label, s, ok)
		}
	}
	if _, ok := SessionOf("persona"); ok {
		t.Error("persona is not a task-state label")
	}
}
