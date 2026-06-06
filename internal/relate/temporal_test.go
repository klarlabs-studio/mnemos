package relate

import (
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
)

func TestClassifyAspect_Markers(t *testing.T) {
	cases := []struct {
		text string
		want aspect
	}{
		{"The migration completed on Tuesday", aspectCompleted},
		{"The migration is still running", aspectOngoing},
		{"The migration is scheduled for Thursday", aspectPlanned},
		{"The migration was never deployed", aspectNever},
		{"Postgres is the primary database", aspectUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.text, func(t *testing.T) {
			got := classifyAspect(tc.text)
			if got != tc.want {
				t.Errorf("classifyAspect(%q) = %d, want %d", tc.text, got, tc.want)
			}
		})
	}
}

func TestAspectsConflict_OnlyKnownPairs(t *testing.T) {
	if aspectsConflict(aspectUnknown, aspectCompleted) {
		t.Error("unknown vs anything must not conflict")
	}
	if !aspectsConflict(aspectCompleted, aspectOngoing) {
		t.Error("completed vs ongoing must conflict")
	}
	if aspectsConflict(aspectCompleted, aspectCompleted) {
		t.Error("same aspect must not conflict")
	}
}

func TestDetectTemporalDivergence_FlagsCompletedVsOngoing(t *testing.T) {
	a := "The migration completed on Tuesday"
	b := "The migration is still running"
	aTok, _ := contentTokensAndPolarity(a)
	bTok, _ := contentTokensAndPolarity(b)
	if !detectTemporalDivergence(a, b, aTok, bTok) {
		t.Errorf("completed vs still running should flag")
	}
}

func TestDetectTemporalDivergence_RequiresSharedSubject(t *testing.T) {
	a := "The migration completed on Tuesday"
	b := "The customer is still waiting"
	aTok, _ := contentTokensAndPolarity(a)
	bTok, _ := contentTokensAndPolarity(b)
	if detectTemporalDivergence(a, b, aTok, bTok) {
		t.Errorf("different subjects must not flag despite aspect mismatch")
	}
}

func TestDetect_TemporalDivergenceEmitsContradicts(t *testing.T) {
	engine := NewEngine()
	engine.nextID = seqRelationshipIDs()
	rels, err := engine.Detect([]domain.Claim{
		{ID: "cl_1", Text: "The migration completed on Tuesday"},
		{ID: "cl_2", Text: "The migration is still running"},
		{ID: "cl_3", Text: "Engineering signed off on Monday"},
	})
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	hasContradicts := false
	for _, r := range rels {
		if r.Type == domain.RelationshipTypeContradicts {
			hasContradicts = true
		}
	}
	if !hasContradicts {
		t.Errorf("expected contradicts on temporal-aspect mismatch (got %v)", rels)
	}
}
