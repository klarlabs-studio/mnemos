package revision

import (
	"reflect"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func at(h int) time.Time { return t0.Add(time.Duration(h) * time.Hour) }

func belief(id string, created int) Belief {
	return Belief{ID: id, Status: domain.ClaimStatusActive, Current: true, CreatedAt: at(created)}
}

func TestPlan(t *testing.T) {
	tests := []struct {
		name    string
		in      Inputs
		want    []Step
		explain map[string]string // decision -> superseded claim that explained it
	}{
		{
			name: "no surprise revises nothing, even for recalled beliefs",
			in: Inputs{
				Beliefs: map[string]Belief{"a": belief("a", 0)},
			},
			want: nil,
		},
		{
			name: "surprise without contrary evidence flags every rested-on belief",
			in: Inputs{
				Surprises: []Surprise{{DecisionID: "d1", RestsOn: []string{"b", "a"}}},
				Beliefs:   map[string]Belief{"a": belief("a", 0), "b": belief("b", 0)},
			},
			want: []Step{
				{ClaimID: "a", Kind: StepFlag, DecisionIDs: []string{"d1"}},
				{ClaimID: "b", Kind: StepFlag, DecisionIDs: []string{"d1"}},
			},
		},
		{
			name: "newer contradicting evidence supersedes the surprised belief and explains the surprise away",
			in: Inputs{
				Surprises: []Surprise{{DecisionID: "d1", RestsOn: []string{"stale", "stable"}}},
				Beliefs: map[string]Belief{
					"stale":  belief("stale", 0),
					"stable": belief("stable", 0),
					"new":    belief("new", 5),
				},
				Contradicts: map[string][]string{"stale": {"new"}, "new": {"stale"}},
			},
			want: []Step{
				{ClaimID: "stale", Kind: StepSupersede, By: "new", DecisionIDs: []string{"d1"}},
			},
			explain: map[string]string{"d1": "stale"},
		},
		{
			name: "older contradicting evidence is not new evidence",
			in: Inputs{
				Surprises: []Surprise{{DecisionID: "d1", RestsOn: []string{"b"}}},
				Beliefs:   map[string]Belief{"b": belief("b", 5), "old": belief("old", 0)},
				Contradicts: map[string][]string{
					"b": {"old"},
				},
			},
			want: []Step{{ClaimID: "b", Kind: StepFlag, DecisionIDs: []string{"d1"}}},
		},
		{
			name: "a retired contradicting claim is not evidence",
			in: Inputs{
				Surprises: []Surprise{{DecisionID: "d1", RestsOn: []string{"b"}}},
				Beliefs: map[string]Belief{
					"b":   belief("b", 0),
					"new": {ID: "new", Status: domain.ClaimStatusActive, Current: false, CreatedAt: at(5)},
				},
				Contradicts: map[string][]string{"b": {"new"}},
			},
			want: []Step{{ClaimID: "b", Kind: StepFlag, DecisionIDs: []string{"d1"}}},
		},
		{
			name: "a contradicting belief the same decision rested on cannot decide against its partner",
			in: Inputs{
				Surprises: []Surprise{{DecisionID: "d1", RestsOn: []string{"a", "b"}}},
				Beliefs:   map[string]Belief{"a": belief("a", 0), "b": belief("b", 5)},
				Contradicts: map[string][]string{
					"a": {"b"}, "b": {"a"},
				},
			},
			want: []Step{
				{ClaimID: "a", Kind: StepFlag, DecisionIDs: []string{"d1"}},
				{ClaimID: "b", Kind: StepFlag, DecisionIDs: []string{"d1"}},
			},
		},
		{
			name: "the newest contradicting claim wins, ties broken by id",
			in: Inputs{
				Surprises: []Surprise{{DecisionID: "d1", RestsOn: []string{"s"}}},
				Beliefs: map[string]Belief{
					"s": belief("s", 0), "n1": belief("n1", 3), "n2": belief("n2", 7), "n3": belief("n3", 7),
				},
				Contradicts: map[string][]string{"s": {"n1", "n3", "n2"}},
			},
			want:    []Step{{ClaimID: "s", Kind: StepSupersede, By: "n2", DecisionIDs: []string{"d1"}}},
			explain: map[string]string{"d1": "s"},
		},
		{
			name: "a promoted belief is flagged, never superseded",
			in: Inputs{
				Surprises: []Surprise{{DecisionID: "d1", RestsOn: []string{"p"}}},
				Beliefs: map[string]Belief{
					"p":   {ID: "p", Status: domain.ClaimStatusActive, Current: true, Promoted: true, CreatedAt: at(0)},
					"new": belief("new", 5),
				},
				Contradicts: map[string][]string{"p": {"new"}},
			},
			want: []Step{{ClaimID: "p", Kind: StepFlag, DecisionIDs: []string{"d1"}}},
		},
		{
			name: "already-contested or retired beliefs need no flag",
			in: Inputs{
				Surprises: []Surprise{{DecisionID: "d1", RestsOn: []string{"c", "gone", "missing"}}},
				Beliefs: map[string]Belief{
					"c":    {ID: "c", Status: domain.ClaimStatusContested, Current: true, CreatedAt: at(0)},
					"gone": {ID: "gone", Status: domain.ClaimStatusActive, Current: false, CreatedAt: at(0)},
				},
			},
			want:    nil,
			explain: map[string]string{"d1": "gone"},
		},
		{
			name: "a surprise stays explained once its supersession is applied",
			in: Inputs{
				Surprises: []Surprise{{DecisionID: "d1", RestsOn: []string{"stale", "stable"}}},
				Beliefs: map[string]Belief{
					"stale":  {ID: "stale", Status: domain.ClaimStatusActive, Current: false, CreatedAt: at(0)},
					"stable": belief("stable", 0),
					"new":    belief("new", 5),
				},
				Contradicts: map[string][]string{"stale": {"new"}},
			},
			want:    nil,
			explain: map[string]string{"d1": "stale"},
		},
		{
			name: "an explained surprise clears the flag it set earlier on a co-rested belief",
			in: Inputs{
				Surprises: []Surprise{{DecisionID: "d1", RestsOn: []string{"stale", "stable"}}},
				Beliefs: map[string]Belief{
					"stale":  {ID: "stale", Status: domain.ClaimStatusContested, FlaggedBySurprise: true, Current: true, CreatedAt: at(0)},
					"stable": {ID: "stable", Status: domain.ClaimStatusContested, FlaggedBySurprise: true, Current: true, CreatedAt: at(0)},
					"new":    belief("new", 5),
				},
				Contradicts: map[string][]string{"stale": {"new"}},
			},
			want: []Step{
				{ClaimID: "stable", Kind: StepClear, DecisionIDs: []string{"d1"}},
				{ClaimID: "stale", Kind: StepSupersede, By: "new", DecisionIDs: []string{"d1"}},
			},
			explain: map[string]string{"d1": "stale"},
		},
		{
			name: "a flag stays while any unexplained surprise still rests on the belief",
			in: Inputs{
				Surprises: []Surprise{
					{DecisionID: "d1", RestsOn: []string{"stale", "shared"}},
					{DecisionID: "d2", RestsOn: []string{"shared"}},
				},
				Beliefs: map[string]Belief{
					"stale":  belief("stale", 0),
					"shared": {ID: "shared", Status: domain.ClaimStatusContested, FlaggedBySurprise: true, Current: true, CreatedAt: at(0)},
					"new":    belief("new", 5),
				},
				Contradicts: map[string][]string{"stale": {"new"}},
			},
			want: []Step{
				{ClaimID: "stale", Kind: StepSupersede, By: "new", DecisionIDs: []string{"d1"}},
			},
			explain: map[string]string{"d1": "stale"},
		},
		{
			name: "a contested belief flagged by something else is never cleared",
			in: Inputs{
				Surprises: []Surprise{{DecisionID: "d1", RestsOn: []string{"stale", "other"}}},
				Beliefs: map[string]Belief{
					"stale": belief("stale", 0),
					"other": {ID: "other", Status: domain.ClaimStatusContested, Current: true, CreatedAt: at(0)},
					"new":   belief("new", 5),
				},
				Contradicts: map[string][]string{"stale": {"new"}},
			},
			want: []Step{
				{ClaimID: "stale", Kind: StepSupersede, By: "new", DecisionIDs: []string{"d1"}},
			},
			explain: map[string]string{"d1": "stale"},
		},
		{
			name: "two surprises on one belief are recorded once, with both decisions",
			in: Inputs{
				Surprises: []Surprise{
					{DecisionID: "d2", RestsOn: []string{"a"}},
					{DecisionID: "d1", RestsOn: []string{"a"}},
				},
				Beliefs: map[string]Belief{"a": belief("a", 0)},
			},
			want: []Step{{ClaimID: "a", Kind: StepFlag, DecisionIDs: []string{"d1", "d2"}}},
		},
		{
			name: "a flag-cleared belief rested on by no surprise stays flagged",
			in: Inputs{
				Beliefs: map[string]Belief{
					"x": {ID: "x", Status: domain.ClaimStatusContested, FlaggedBySurprise: true, Current: true, CreatedAt: at(0)},
				},
			},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Plan(tt.in)
			if !reflect.DeepEqual(got.Steps, tt.want) {
				t.Fatalf("steps:\n got %+v\nwant %+v", got.Steps, tt.want)
			}
			wantExplained := tt.explain
			if wantExplained == nil {
				wantExplained = map[string]string{}
			}
			if !reflect.DeepEqual(got.ExplainedBy, wantExplained) {
				t.Fatalf("explained: got %v, want %v", got.ExplainedBy, wantExplained)
			}
		})
	}
}

// Plan is a pure function of its inputs: the order the store returns rows in
// must not change what is revised.
func TestPlan_IsOrderIndependent(t *testing.T) {
	in := Inputs{
		Surprises: []Surprise{
			{DecisionID: "d1", RestsOn: []string{"a", "b"}},
			{DecisionID: "d2", RestsOn: []string{"c"}},
		},
		Beliefs: map[string]Belief{
			"a": belief("a", 0), "b": belief("b", 0), "c": belief("c", 0), "n": belief("n", 4),
		},
		Contradicts: map[string][]string{"c": {"n"}},
	}
	first := Plan(in)
	in.Surprises[0], in.Surprises[1] = in.Surprises[1], in.Surprises[0]
	in.Surprises[1].RestsOn = []string{"b", "a"}
	second := Plan(in)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("plan depends on input order:\n%+v\n%+v", first, second)
	}
}
