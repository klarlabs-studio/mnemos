package relate

import (
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
)

func TestExtractNumerics_ParsesIntsFloatsAndUnits(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []numericValue
	}{
		{"plain int", "12 prior refunds", []numericValue{{value: 12, unit: "raw:prior"}}},
		{"unitless int", "the count is 5", []numericValue{{value: 5}}},
		{"with unit", "p99 latency was 250ms", []numericValue{{value: 0.250, unit: "time"}}},
		{"percent", "error rate hit 32%", []numericValue{{value: 32, unit: "percent"}}},
		{"currency", "lifetime value $4500", []numericValue{{value: 4500, unit: "usd"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractNumerics(tc.text)
			if len(got) == 0 {
				t.Fatalf("extractNumerics(%q) returned no values", tc.text)
			}
			if got[0].value != tc.want[0].value {
				t.Errorf("value = %v, want %v", got[0].value, tc.want[0].value)
			}
			if got[0].unit != tc.want[0].unit {
				t.Errorf("unit = %q, want %q", got[0].unit, tc.want[0].unit)
			}
		})
	}
}

func TestNumericValuesAgree_HonoursUnitConversion(t *testing.T) {
	a := extractNumerics("response time is 1 second")
	b := extractNumerics("response time is 1000 ms")
	if !numericValuesAgree(a, b) {
		t.Errorf("1 second and 1000 ms should agree (unit conversion)")
	}
}

func TestNumericValuesAgree_DifferentValuesDoNotAgree(t *testing.T) {
	a := extractNumerics("user has 12 refunds")
	b := extractNumerics("user has 0 refunds")
	if numericValuesAgree(a, b) {
		t.Errorf("12 and 0 must not agree")
	}
}

func TestNumericValuesAgree_DifferentFamiliesDoNotAgree(t *testing.T) {
	// "5 minutes" (time family, normalized to 300s) vs "5 GB" (bytes family).
	// Same magnitude raw, different unit family → disagreement.
	a := extractNumerics("the limit is 5 minutes")
	b := extractNumerics("the limit is 5 GB")
	if numericValuesAgree(a, b) {
		t.Errorf("different unit families should not agree")
	}
}

func TestDetectNumericDivergence_FlagsNumericDisagreement(t *testing.T) {
	aText := "the user has 12 prior refunds"
	bText := "the user has 0 prior refunds"
	aTok, _ := contentTokensAndPolarity(aText)
	bTok, _ := contentTokensAndPolarity(bText)
	if !detectNumericDivergence(aText, bText, aTok, bTok) {
		t.Errorf("12 vs 0 prior refunds should be flagged as divergent")
	}
}

func TestDetectNumericDivergence_AgreesOnEquivalentValues(t *testing.T) {
	aText := "p99 latency hit 1 second yesterday"
	bText := "p99 latency hit 1000 ms yesterday"
	aTok, _ := contentTokensAndPolarity(aText)
	bTok, _ := contentTokensAndPolarity(bText)
	if detectNumericDivergence(aText, bText, aTok, bTok) {
		t.Errorf("1s vs 1000ms must not flag")
	}
}

func TestDetect_NumericDivergenceOverridesSupports(t *testing.T) {
	// "12 refunds" and "0 refunds" share most tokens (would normally
	// be classified as `supports`), but the numeric disagreement must
	// flip the verdict to `contradicts`.
	engine := NewEngine()
	engine.nextID = seqRelationshipIDs()
	rels, err := engine.Detect([]domain.Claim{
		{ID: "cl_1", Text: "The user has 12 prior refunds"},
		{ID: "cl_2", Text: "The user has 0 prior refunds"},
	})
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if len(rels) == 0 {
		t.Fatal("expected at least one relationship between numeric-disagreeing claims")
	}
	hasContradicts := false
	for _, r := range rels {
		if r.Type == domain.RelationshipTypeContradicts {
			hasContradicts = true
		}
	}
	if !hasContradicts {
		t.Errorf("expected contradicts (got %v)", rels)
	}
}

func TestDetectNumericDivergence_RejectsUnrelatedClaims(t *testing.T) {
	// Different topics, both contain numbers — should NOT flag.
	aText := "the deployment took 12 minutes"
	bText := "the team has 0 incidents this quarter"
	aTok, _ := contentTokensAndPolarity(aText)
	bTok, _ := contentTokensAndPolarity(bText)
	if detectNumericDivergence(aText, bText, aTok, bTok) {
		t.Errorf("unrelated claims must not be flagged on numeric difference alone")
	}
}
