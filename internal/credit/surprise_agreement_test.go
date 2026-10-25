package credit

import (
	"testing"
)

// #395: one observation must get one verdict. Reconciliation confirms when
// |predicted − observed| <= Tolerance; credit used `Surprise() <= 1`, and with
// Tolerance 0 Surprise was the raw error, so an observation 0.5 away was
// refuted by one and credited as validated by the other.
func TestDecisionDelta_AgreesWithTheExpectationVerdict(t *testing.T) {
	cases := []struct {
		name                string
		predicted, obs, tol float64
	}{
		{"zero tolerance, exact", 10, 10, 0},
		{"zero tolerance, small miss", 10, 10.5, 0},
		{"zero tolerance, large miss", 10, 30, 0},
		{"zero tolerance, zero prediction", 0, 0.5, 0},
		{"within tolerance", 10, 12, 5},
		{"at the edge", 10, 15, 5},
		{"just over", 10, 16, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exp := obsExp("c", tc.predicted, tc.obs, tc.tol)
			confirmed, ok := exp.Confirmed()
			if !ok {
				t.Fatal("Confirmed not ok with an observation")
			}
			d, ok := decisionDelta(exp)
			if !ok {
				t.Fatal("decisionDelta not ok")
			}
			if confirmed && d < 0 {
				t.Errorf("verdict confirmed but credit is negative (%v)", d)
			}
			if !confirmed && d >= 0 {
				t.Errorf("verdict refuted but credit is %v", d)
			}
		})
	}
}
