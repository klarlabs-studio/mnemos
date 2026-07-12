package plasticity

import (
	"testing"

	"go.klarlabs.de/mnemos/internal/credit"
)

func TestGain_SensitivityZeroIsNeutral(t *testing.T) {
	if g := Gain([]float64{0.1, 5.0, 0.2, 9.0}, DefaultRecentWindow, 0); g != 1 {
		t.Fatalf("sensitivity 0 must disable neuromodulation (gain 1), got %v", g)
	}
}

func TestGain_TooLittleHistoryIsNeutral(t *testing.T) {
	if g := Gain([]float64{5.0}, DefaultRecentWindow, 1); g != 1 {
		t.Fatalf("a single sample carries no volatility signal, want 1 got %v", g)
	}
	if g := Gain(nil, DefaultRecentWindow, 1); g != 1 {
		t.Fatalf("empty series want 1 got %v", g)
	}
}

func TestGain_RisingSurpriseRaisesGain(t *testing.T) {
	// Baseline low, recent high → the world became less predictable → gain > 1.
	series := []float64{0.1, 0.1, 0.1, 0.1, 2.0, 2.0, 2.0}
	g := Gain(series, 3, 1)
	if g <= 1 {
		t.Fatalf("rising recent surprise should raise gain above 1, got %v", g)
	}
	if g > credit.MaxGain+1e-12 {
		t.Fatalf("gain must be bounded by MaxGain %v, got %v", credit.MaxGain, g)
	}
}

func TestGain_SettlingSurpriseLowersGain(t *testing.T) {
	// Baseline high, recent low → settled → gain < 1 (consolidate mode).
	series := []float64{3.0, 3.0, 3.0, 3.0, 0.2, 0.2, 0.2}
	g := Gain(series, 3, 1)
	if g >= 1 {
		t.Fatalf("settling recent surprise should lower gain below 1, got %v", g)
	}
	if g < credit.MinGain-1e-12 {
		t.Fatalf("gain must be bounded by MinGain %v, got %v", credit.MinGain, g)
	}
}

func TestGain_FlatSeriesIsNeutral(t *testing.T) {
	// Steady surprise (expected uncertainty) — recent == baseline → gain ≈ 1.
	series := []float64{1.0, 1.0, 1.0, 1.0, 1.0, 1.0}
	if g := Gain(series, 3, 1); g < 0.999 || g > 1.001 {
		t.Fatalf("a flat series carries no CHANGE signal, want ~1 got %v", g)
	}
}

func TestGain_NearZeroBaselineIsNeutral(t *testing.T) {
	if g := Gain([]float64{0, 0, 0, 0}, 2, 1); g != 1 {
		t.Fatalf("a near-zero series has nothing to be surprised about, want 1 got %v", g)
	}
}
