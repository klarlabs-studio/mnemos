package query

import (
	"context"
	"math"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/trust"
)

// currencyClaim is a live belief dated at validFrom with the given canonical
// trust. Text is shared so only the injected scores separate claims.
func currencyClaim(id string, validFrom time.Time, trustScore float64) domain.Claim {
	return domain.Claim{
		ID: id, Text: "payments rate limit", Type: domain.ClaimTypeFact,
		Status: domain.ClaimStatusActive, CreatedAt: validFrom, ValidFrom: validFrom,
		TrustScore: trustScore,
	}
}

func currencyEngine(claims []domain.Claim, hits []ports.TextHit) Engine {
	return NewEngine(fakeEventRepo{events: []domain.Event{{ID: "ev1", Content: "payments rate limit", Timestamp: time.Now().UTC()}}},
		fakeClaimRepo{claims: claims}, fakeRelationshipRepo{}).
		WithTextSearch(nil, fakeTextSearcher{hits: hits})
}

// The finding that motivated ADR 0039: a 650-day-old statement scored a little
// better on similarity than the current one and ranked first under every
// policy. Trust and freshness now enter the order, so the current one wins.
func TestRecall_FreshTrustedBeliefOutranksStaleSlightlyBetterMatch(t *testing.T) {
	now := time.Now().UTC()
	claims := []domain.Claim{
		currencyClaim("cl_stale", now.AddDate(0, 0, -650), 0.25),
		currencyClaim("cl_fresh", now.AddDate(0, 0, -4), 0.75),
	}
	e := currencyEngine(claims, []ports.TextHit{{ID: "cl_stale", Score: 10}, {ID: "cl_fresh", Score: 9}})

	if got := recallIDs(t, e, "payments rate limit", AnswerOptions{}); got[0] != "cl_fresh" {
		t.Fatalf("got %v, want the fresh, trusted belief first", got)
	}
	// Each component alone is enough here, so each ablation alone keeps the
	// order; with both off, similarity decides again.
	both := Ablation{Trust: true, Freshness: true}
	if got := recallIDs(t, e, "payments rate limit", AnswerOptions{Ablate: both}); got[0] != "cl_stale" {
		t.Fatalf("with trust and freshness ablated similarity should decide, got %v", got)
	}
}

// The bound: a fresh, fully trusted belief with much weaker relevance must not
// overtake a stale, distrusted strong match. Freshness re-weighs relevance; it
// does not replace it.
func TestRecall_FreshnessCannotSurfaceAWeakMatchOverAStrongOne(t *testing.T) {
	now := time.Now().UTC()
	claims := []domain.Claim{
		currencyClaim("cl_fresh_weak", now, 1.0),
		currencyClaim("cl_stale_strong", now.AddDate(0, 0, -2000), 0.0),
	}
	// Relative relevance just under the structural bound.
	weak := 10 * (currencyMinFactor - 0.01)
	e := currencyEngine(claims, []ports.TextHit{{ID: "cl_fresh_weak", Score: weak}, {ID: "cl_stale_strong", Score: 10}})

	if got := recallIDs(t, e, "payments rate limit", AnswerOptions{}); got[0] != "cl_stale_strong" {
		t.Fatalf("got %v: a fresh weak match overtook a strong one beyond the bound", got)
	}
}

// A belief with no relevance signal at all stays below every hit, however
// fresh and trusted it is.
func TestRankClaims_CurrencyNeverLiftsASignalLessBelief(t *testing.T) {
	now := time.Now().UTC()
	claims := []domain.Claim{
		currencyClaim("cl_unmatched", now, 1.0),
		currencyClaim("cl_hit", now.AddDate(0, 0, -2000), 0.0),
	}
	e := currencyEngine(nil, []ports.TextHit{{ID: "cl_hit", Score: 1}})
	got := e.rankClaims(context.Background(), "x", claims, rankTerms{currencyAt: now})
	if got[0].ID != "cl_hit" {
		t.Fatalf("got %v, want the only matching belief first", []string{got[0].ID, got[1].ID})
	}
}

// Freshness is measured at the read's instant, not the wall clock. At now both
// beliefs are long past the freshness floor, so relevance decides; asked as of
// a day when the second was ten days old and the first sixty, the second wins.
func TestRecall_FreshnessIsMeasuredAtTheAsOfInstant(t *testing.T) {
	now := time.Now().UTC()
	claims := []domain.Claim{
		currencyClaim("cl_older", now.AddDate(0, 0, -700), 0.5),
		currencyClaim("cl_newer", now.AddDate(0, 0, -650), 0.5),
	}
	e := currencyEngine(claims, []ports.TextHit{{ID: "cl_older", Score: 10}, {ID: "cl_newer", Score: 9.5}})

	if got := recallIDs(t, e, "payments rate limit", AnswerOptions{}); got[0] != "cl_older" {
		t.Fatalf("at now both are floored, relevance should decide; got %v", got)
	}
	asOf := now.AddDate(0, 0, -640)
	if got := recallIDs(t, e, "payments rate limit", AnswerOptions{AsOf: asOf}); got[0] != "cl_newer" {
		t.Fatalf("as of %s the newer belief was fresh; got %v", asOf.Format(time.DateOnly), got)
	}
}

// Each Ablation field removes its own factor and only that one, so the
// leave-one-out rows measure what they name.
func TestAblation_TrustAndFreshnessEachRemoveOnlyTheirFactor(t *testing.T) {
	now := time.Now().UTC()
	q := "payments rate limit"
	hits := []ports.TextHit{{ID: "a", Score: 5}, {ID: "b", Score: 5}}

	// Equal relevance and age; b is better trusted.
	byTrust := currencyEngine([]domain.Claim{
		currencyClaim("a", now.AddDate(0, 0, -1), 0.2), currencyClaim("b", now.AddDate(0, 0, -1), 0.9),
	}, hits)
	if got := recallIDs(t, byTrust, q, AnswerOptions{}); got[0] != "b" {
		t.Fatalf("trust should order equal matches, got %v", got)
	}
	if got := recallIDs(t, byTrust, q, AnswerOptions{Ablate: Ablation{Freshness: true}}); got[0] != "b" {
		t.Fatalf("freshness ablation removed the trust factor, got %v", got)
	}
	if got := recallIDs(t, byTrust, q, AnswerOptions{Ablate: Ablation{Trust: true}}); got[0] != "a" {
		t.Fatalf("trust ablation should leave the tie in input order, got %v", got)
	}

	// Equal relevance and trust; b is fresher.
	byAge := currencyEngine([]domain.Claim{
		currencyClaim("a", now.AddDate(0, 0, -300), 0.5), currencyClaim("b", now.AddDate(0, 0, -3), 0.5),
	}, hits)
	if got := recallIDs(t, byAge, q, AnswerOptions{}); got[0] != "b" {
		t.Fatalf("freshness should order equal matches, got %v", got)
	}
	if got := recallIDs(t, byAge, q, AnswerOptions{Ablate: Ablation{Trust: true}}); got[0] != "b" {
		t.Fatalf("trust ablation removed the freshness factor, got %v", got)
	}
	if got := recallIDs(t, byAge, q, AnswerOptions{Ablate: Ablation{Freshness: true}}); got[0] != "a" {
		t.Fatalf("freshness ablation should leave the tie in input order, got %v", got)
	}
}

func TestReadInstant_PrefersRecordedThenValidThenNow(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	valid := now.AddDate(0, -2, 0)
	recordedT := now.AddDate(0, -1, 0)
	cases := []struct {
		name string
		opts AnswerOptions
		want time.Time
	}{
		{"wall clock", AnswerOptions{}, now},
		{"valid-time read", AnswerOptions{AsOf: valid}, valid},
		{"recorded-as-of read", AnswerOptions{RecordedAsOf: recordedT}, recordedT},
		{"both axes: the brain's own now wins", AnswerOptions{AsOf: valid, RecordedAsOf: recordedT}, recordedT},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := readInstant(tc.opts, now); !got.Equal(tc.want) {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

// The factor is the trust package's own decay, not a second one: its
// freshness half is a rescaling of trust.Staleness.
func TestCurrencyFactor_ReusesTheTrustDecay(t *testing.T) {
	now := time.Now().UTC()
	c := currencyClaim("c", now.AddDate(0, 0, -45), 1.0)
	c.HalfLifeDays = 14
	st := trust.Staleness(c.ValidFrom, time.Time{}, now, c.HalfLifeDays) / (1 - trust.FreshnessFloor)
	want := 1 - freshnessRankWeight*st
	if got := currencyFactor(c, rankTerms{currencyAt: now, noTrust: true}); math.Abs(got-want) > 1e-12 {
		t.Fatalf("freshness factor %v, want %v", got, want)
	}
	undated := domain.Claim{ID: "u", TrustScore: 1}
	if got := currencyFactor(undated, rankTerms{currencyAt: now}); got != 1 {
		t.Fatalf("an undated, fully trusted belief should be unpenalised, got %v", got)
	}
	if got := currencyFactor(c, rankTerms{}); got != 1 {
		t.Fatalf("a zero instant disables the term, got %v", got)
	}
}

// The freshness reference is canonical trust's: an explicit confirmation
// refreshes a belief, rehearsal (LastVerified, bumped by recall itself) does
// not, and a confirmation after the read's instant had not happened yet.
func TestCurrencyFactor_FreshnessReferenceIsTheConfirmationNotRehearsal(t *testing.T) {
	now := time.Now().UTC()
	terms := rankTerms{currencyAt: now, noTrust: true}
	old := currencyClaim("c", now.AddDate(0, 0, -400), 1.0)
	floored := currencyFactor(old, terms)
	if want := 1 - freshnessRankWeight; math.Abs(floored-want) > 1e-12 {
		t.Fatalf("a 400-day-old belief should sit at the floor factor %v, got %v", want, floored)
	}

	rehearsed := old
	rehearsed.LastVerified = now
	if got := currencyFactor(rehearsed, terms); got != floored {
		t.Fatalf("rehearsal refreshed the ranking: %v, want %v", got, floored)
	}

	confirmed := old
	confirmed.LastConfirmed = now.Add(-time.Hour)
	if got := currencyFactor(confirmed, terms); got <= floored {
		t.Fatalf("an explicit confirmation should refresh the belief, got %v (floor %v)", got, floored)
	}

	asOfBefore := rankTerms{currencyAt: now.AddDate(0, 0, -10), noTrust: true}
	if got, want := currencyFactor(confirmed, asOfBefore), currencyFactor(old, asOfBefore); got != want {
		t.Fatalf("a confirmation after the read's instant counted: %v, want %v", got, want)
	}
}
