package trust

import (
	"math"
	"time"

	"go.klarlabs.de/mnemos/internal/credit"
	"go.klarlabs.de/mnemos/internal/domain"
)

// ModelVersion identifies the trust model [At] implements. Stored trust_score
// values are a cache of At under this version (ADR 0026 §5); rows computed by
// the earlier global-time-constant formula are trust/v1.
const ModelVersion = "trust/v2"

// At is THE trust of a belief at an instant (ADR 0026). Every subsystem that
// needs a belief's trust reads the stored value or calls At; nothing computes a
// variant of its own. It is pure: no I/O, and the instant is a parameter.
//
//	At     = clamp01( base + clamp(credit, ±credit.CreditCap) )
//	base   = clamp01( confidence × (1 + 0.2·ln(max(1, n))) × freshness )
//	fresh. = max(FreshnessFloor, exp(−d/τ)),  τ = HalfLifeDays, or the default
//	d      = days from max(LatestEvidence, LastConfirmed) to at
//
// τ is an e-folding time: freshness is e^-1 ≈ 0.37 at d = τ, not 0.5. The field
// keeps its historical name (ADR 0026 §4). A missing or future reference leaves
// freshness at 1, as Score always has, so an undated claim is not penalised.
func At(in domain.TrustInput, at time.Time) float64 {
	c := clamp01(in.Confidence)
	n := in.EvidenceCount
	if n < 1 {
		n = 1
	}
	corroboration := 1 + math.Log(float64(n))*CorroborationCoefficient
	ref := in.LatestEvidence
	if in.LastConfirmed.After(ref) {
		ref = in.LastConfirmed
	}
	base := clamp01(c * corroboration * freshnessWithTimeConstant(ref, at, in.HalfLifeDays))
	applied := math.Max(-credit.CreditCap, math.Min(credit.CreditCap, in.Credit))
	return clamp01(base + applied)
}

// Scorer returns a recompute pass of At bound to one instant and stamped with
// ModelVersion, in the shape the storage ports take. A pass must score every
// row against the same instant, so callers fix it once rather than reading the
// clock per row, and the backend persists that instant beside each score.
func Scorer(at time.Time) domain.TrustScoring {
	return domain.TrustScoring{
		At:           at,
		ModelVersion: ModelVersion,
		Score:        func(in domain.TrustInput) float64 { return At(in, at) },
	}
}

func freshnessWithTimeConstant(ref, at time.Time, timeConstantDays float64) float64 {
	if ref.IsZero() {
		return 1.0
	}
	days := at.Sub(ref).Hours() / 24
	if days <= 0 {
		return 1.0
	}
	tau := timeConstantDays
	if tau <= 0 {
		tau = FreshnessHalfLifeDays
	}
	return math.Max(FreshnessFloor, math.Exp(-days/tau))
}
