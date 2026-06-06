package trust

import (
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

const (
	// LiveWindow is the recency window considered "actively used".
	LiveWindow = 14 * 24 * time.Hour
	// StaleWindow bounds the medium-term window where data is still useful
	// but not recently executed/referenced.
	StaleWindow = 180 * 24 * time.Hour
	// ZombieTrustThreshold is the minimum trust score for old claims to be
	// considered zombie (old but still trusted) instead of dead.
	ZombieTrustThreshold = 0.60
)

// EffectiveExecutionTime returns the best available liveness timestamp.
// Priority: lastExecuted > lastVerified > validFrom > createdAt.
func EffectiveExecutionTime(lastExecuted, lastVerified, validFrom, createdAt time.Time) time.Time {
	if !lastExecuted.IsZero() {
		return lastExecuted
	}
	if !lastVerified.IsZero() {
		return lastVerified
	}
	if !validFrom.IsZero() {
		return validFrom
	}
	return createdAt
}

// EvaluateLiveness classifies a claim/source as live, stale, zombie, dead,
// or unknown based on recency and trust.
func EvaluateLiveness(lastExecuted, lastVerified, validFrom, createdAt, now time.Time, trustScore float64) domain.LivenessStatus {
	reference := EffectiveExecutionTime(lastExecuted, lastVerified, validFrom, createdAt)
	if reference.IsZero() {
		return domain.LivenessUnknown
	}
	age := now.Sub(reference)
	if age < 0 {
		age = 0
	}
	if age <= LiveWindow {
		return domain.LivenessLive
	}
	if age <= StaleWindow {
		return domain.LivenessStale
	}
	if trustScore >= ZombieTrustThreshold {
		return domain.LivenessZombie
	}
	return domain.LivenessDead
}
