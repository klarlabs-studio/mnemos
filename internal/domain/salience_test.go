package domain

import "testing"

func TestBeliefSalience_DefaultNeutralWhenUnset(t *testing.T) {
	c := Belief{ID: "c1"}
	if v, ok := c.Salience(); ok || v != NeutralSalience {
		t.Fatalf("unset salience should be neutral/absent: v=%.2f ok=%v", v, ok)
	}
	if c.EffectiveSalience() != NeutralSalience {
		t.Fatalf("EffectiveSalience unset should be neutral, got %.2f", c.EffectiveSalience())
	}
}

func TestBeliefSalience_ReadsExplicitComponent(t *testing.T) {
	c := Belief{ID: "c1", ConfidenceComponents: map[string]float64{SalienceComponentKey: 0.9}}
	v, ok := c.Salience()
	if !ok || v != 0.9 {
		t.Fatalf("explicit salience: v=%.2f ok=%v", v, ok)
	}
	if c.EffectiveSalience() != 0.9 {
		t.Fatalf("EffectiveSalience = %.2f, want 0.9", c.EffectiveSalience())
	}
}

func TestBeliefSalience_ClampsDefensively(t *testing.T) {
	hi := Belief{ConfidenceComponents: map[string]float64{SalienceComponentKey: 2}}
	if v, _ := hi.Salience(); v != 1 {
		t.Fatalf("out-of-range high should clamp to 1, got %.2f", v)
	}
	lo := Belief{ConfidenceComponents: map[string]float64{SalienceComponentKey: -1}}
	if v, _ := lo.Salience(); v != 0 {
		t.Fatalf("out-of-range low should clamp to 0, got %.2f", v)
	}
}

// A salience component with a valid [0,1] value must pass claim validation — it is
// an ordinary (non-credit) component, so no new schema/validator rule is needed.
func TestBeliefSalience_ValidatesAsOrdinaryComponent(t *testing.T) {
	c := Claim{
		ID: "c1", Text: "x", Type: ClaimTypeFact, Confidence: 0.5,
		Status:               ClaimStatusActive,
		ConfidenceComponents: map[string]float64{SalienceComponentKey: 0.8},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("salience component should validate: %v", err)
	}
}
