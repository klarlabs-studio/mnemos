package salience

import (
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
)

func TestCompute_DefaultNeutralWhenNoSignal(t *testing.T) {
	got := Compute(Inputs{})
	if got != Neutral {
		t.Fatalf("no signal should be neutral %.2f, got %.2f", Neutral, got)
	}
}

func TestCompute_ExplicitOverrideWins(t *testing.T) {
	// Override beats even a critical decision — a human marking stakes is authoritative.
	got := Compute(Inputs{
		Override:    0.2,
		HasOverride: true,
		RiskLevel:   domain.RiskLevelCritical,
		Outcome:     domain.OutcomeResultFailure,
		HasOutcome:  true,
	})
	if got != 0.2 {
		t.Fatalf("override should win outright, got %.2f", got)
	}
	// And it is clamped to [0,1].
	if v := Compute(Inputs{Override: 5, HasOverride: true}); v != 1 {
		t.Fatalf("override should clamp to 1, got %.2f", v)
	}
	if v := Compute(Inputs{Override: -3, HasOverride: true}); v != 0 {
		t.Fatalf("override should clamp to 0, got %.2f", v)
	}
}

func TestCompute_RiskLadderMonotonic(t *testing.T) {
	low := Compute(Inputs{RiskLevel: domain.RiskLevelLow})
	med := Compute(Inputs{RiskLevel: domain.RiskLevelMedium})
	high := Compute(Inputs{RiskLevel: domain.RiskLevelHigh})
	crit := Compute(Inputs{RiskLevel: domain.RiskLevelCritical})
	if !(low < med && med < high && high < crit) {
		t.Fatalf("risk salience must be monotonic: low=%.2f med=%.2f high=%.2f crit=%.2f", low, med, high, crit)
	}
	if crit != 1.0 {
		t.Fatalf("critical risk should be maximal, got %.2f", crit)
	}
	if med != Neutral {
		t.Fatalf("medium risk should sit at neutral, got %.2f", med)
	}
}

func TestCompute_OutcomeSeverityRaisesButSuccessDoesNot(t *testing.T) {
	// A failure is a severe consequence → high stakes.
	fail := Compute(Inputs{Outcome: domain.OutcomeResultFailure, HasOutcome: true})
	if fail <= Neutral {
		t.Fatalf("failure outcome should raise salience above neutral, got %.2f", fail)
	}
	// Success carries no stakes signal → neutral, never LOWERED.
	ok := Compute(Inputs{Outcome: domain.OutcomeResultSuccess, HasOutcome: true})
	if ok != Neutral {
		t.Fatalf("success outcome should stay neutral, got %.2f", ok)
	}
}

func TestCompute_TakesMaxOfSignals(t *testing.T) {
	// Low risk but a failure outcome → the stronger (failure) signal governs.
	got := Compute(Inputs{
		RiskLevel:  domain.RiskLevelLow,
		Outcome:    domain.OutcomeResultFailure,
		HasOutcome: true,
	})
	fail, _ := OutcomeSalience(domain.OutcomeResultFailure)
	if got != fail {
		t.Fatalf("max-of-signals expected %.2f, got %.2f", fail, got)
	}
}
