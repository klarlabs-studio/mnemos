package query

import (
	"math"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/trust"
)

// Currency: trust and freshness as ranking inputs (ADR 0039).
//
// Recall used to order beliefs by similarity alone (plus the salience and
// inhibition terms), so a 650-day-old statement that matched the question a
// little better than its replacement ranked first. Trust and freshness were
// computed, reported and gated on, but never entered the order.
//
// The term is a MULTIPLICATIVE re-weight of a belief's relevance:
//
//	score' = relevance × trustFactor × freshnessFactor
//	trustFactor     = 1 − trustRankWeight     × (1 − trust)
//	freshnessFactor = 1 − freshnessRankWeight × staleness
//
// Multiplicative, not additive, because that is what makes both promises
// structural rather than a matter of tuning:
//
//   - Freshness alone cannot surface an irrelevant belief. A belief with no
//     relevance signal keeps its no-signal sentinel and is never scored, and a
//     scored belief's factor only ever shrinks its own relevance. A fresh,
//     fully trusted belief overtakes a stale, distrusted one only when its
//     relevance is at least currencyMinFactor of the other's.
//   - A stale high-similarity belief cannot drown a fresh relevant one: the
//     stale belief's relevance is discounted by up to 1 − currencyMinFactor.
//
// An additive term would give a weak fresh match the same absolute lift as a
// strong one, which is exactly the "freshness surfaces noise" failure.

const (
	// freshnessRankWeight is the largest share of relevance freshness can
	// remove: a belief decayed to the freshness floor keeps 70% of its
	// relevance. Chosen a priori, not fitted to a benchmark: below 0.5, so
	// freshness alone can never reorder two beliefs whose relevance differs
	// by 2x or more. ADR 0039 §5 records what it measured.
	freshnessRankWeight = 0.3

	// trustRankWeight is the largest share of relevance low trust can remove:
	// a belief with trust 0 keeps 80% of its relevance. Smaller than the
	// freshness weight because canonical trust already includes a freshness
	// factor (trust.At), so the two would otherwise count decay twice at full
	// strength; trust's own contribution here is confidence, corroboration and
	// credit.
	trustRankWeight = 0.2
)

// currencyMinFactor is the smallest combined factor: a belief at trust 0 and
// at the freshness floor keeps this share of its relevance. It is the bound in
// both promises above.
const currencyMinFactor = (1 - trustRankWeight) * (1 - freshnessRankWeight)

// rankTerms selects the bounded re-weights the claim ranker applies on top of
// relevance. The zero value is relevance alone (plus inhibition, which the
// engine controls).
type rankTerms struct {
	// salience applies the bounded stakes term (ADR 0013 §4).
	salience bool
	// currencyAt is the read's instant (readInstant). Zero disables the
	// trust and freshness factors.
	currencyAt time.Time
	// noTrust and noFreshness switch one factor off (Ablation).
	noTrust     bool
	noFreshness bool
}

// readInstant is the instant a read is answered at. A recorded-as-of read is
// the brain at T, so T is its now (ADR 0036 §1, as admission already uses for
// "valid now"). A valid-time read asks about the world at AsOf, and a belief's
// freshness is how recently it had been evidenced as of then. Otherwise it is
// the wall clock.
func readInstant(opts AnswerOptions, now time.Time) time.Time {
	switch {
	case !opts.RecordedAsOf.IsZero():
		return opts.RecordedAsOf.UTC()
	case !opts.AsOf.IsZero():
		return opts.AsOf.UTC()
	}
	return now
}

// currencyFactor is the multiplicative re-weight for one belief, in
// [currencyMinFactor, 1].
//
// Freshness is trust.Staleness — the exponential decay, floor and per-belief
// half-life canonical trust uses — rescaled from [0, 1−FreshnessFloor] to
// [0, 1]. Its reference is the one canonical trust decays from (trust.At): the
// belief's evidence date (ValidFrom, set from its source episode) or its last
// EXPLICIT confirmation, whichever is later. Not LastVerified: recall
// reconsolidation and sleep replay bump that as rehearsal (ADR 0026), and a
// ranking input that recall itself refreshes would let whatever was recalled
// once keep winning. A confirmation after the read's instant had not happened
// then and is ignored. An undated belief has staleness 0, as everywhere else:
// decay is unmeasurable for it, so it is not penalised.
//
// Trust is the belief's canonical TrustScore. On a recorded-as-of read the
// engine reads through internal/recorded, which sets it to trust at T and
// rewinds HalfLifeDays and LastConfirmed to T (ADR 0036 §7), so both factors
// are already at the read's instant.
func currencyFactor(c domain.Claim, t rankTerms) float64 {
	if t.currencyAt.IsZero() {
		return 1
	}
	f := 1.0
	if !t.noTrust {
		tr := math.Max(0, math.Min(1, c.TrustScore))
		f *= 1 - trustRankWeight*(1-tr)
	}
	if !t.noFreshness {
		confirmed := c.LastConfirmed
		if confirmed.After(t.currencyAt) {
			confirmed = time.Time{}
		}
		st := trust.Staleness(c.ValidFrom, confirmed, t.currencyAt, c.HalfLifeDays) / (1 - trust.FreshnessFloor)
		f *= 1 - freshnessRankWeight*math.Min(1, st)
	}
	return f
}

// applyCurrency re-weights a relevance score by f without letting the sign of
// a negative (anti-correlated) similarity invert the penalty: a discounted
// belief always moves down.
func applyCurrency(s, f float64) float64 {
	return s - math.Abs(s)*(1-f)
}
