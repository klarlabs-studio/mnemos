package trust

import (
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

func TestEffectiveExecutionTimePriority(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	validFrom := created.Add(24 * time.Hour)
	verified := created.Add(48 * time.Hour)
	executed := created.Add(72 * time.Hour)

	got := EffectiveExecutionTime(executed, verified, validFrom, created)
	if !got.Equal(executed) {
		t.Fatalf("expected lastExecuted to win, got %v", got)
	}

	got = EffectiveExecutionTime(time.Time{}, verified, validFrom, created)
	if !got.Equal(verified) {
		t.Fatalf("expected lastVerified to win, got %v", got)
	}

	got = EffectiveExecutionTime(time.Time{}, time.Time{}, validFrom, created)
	if !got.Equal(validFrom) {
		t.Fatalf("expected validFrom to win, got %v", got)
	}

	got = EffectiveExecutionTime(time.Time{}, time.Time{}, time.Time{}, created)
	if !got.Equal(created) {
		t.Fatalf("expected createdAt fallback, got %v", got)
	}
}

func TestEvaluateLivenessBands(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	created := now.Add(-365 * 24 * time.Hour)

	tests := []struct {
		name     string
		lastExec time.Time
		trust    float64
		want     domain.LivenessStatus
	}{
		{name: "live", lastExec: now.Add(-7 * 24 * time.Hour), trust: 0.2, want: domain.LivenessLive},
		{name: "stale", lastExec: now.Add(-60 * 24 * time.Hour), trust: 0.2, want: domain.LivenessStale},
		{name: "zombie", lastExec: now.Add(-300 * 24 * time.Hour), trust: 0.8, want: domain.LivenessZombie},
		{name: "dead", lastExec: now.Add(-300 * 24 * time.Hour), trust: 0.2, want: domain.LivenessDead},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluateLiveness(tc.lastExec, time.Time{}, time.Time{}, created, now, tc.trust)
			if got != tc.want {
				t.Fatalf("EvaluateLiveness() = %q, want %q", got, tc.want)
			}
		})
	}
}
