package trust

import (
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
)

func TestSalience_RangeAndOrdering(t *testing.T) {
	// A consequential, corroborated, verified decision from an authoritative
	// source should score high; a speculative, unconfirmed, single-source
	// hypothesis should score low.
	strong := Salience(SalienceInputs{
		Type: domain.ClaimTypeDecision, Confidence: 0.95, SourceAuthority: 0.9,
		EvidenceCount: 6, VerifyCount: 3,
	})
	weak := Salience(SalienceInputs{
		Type: domain.ClaimTypeHypothesis, Confidence: 0.2, SourceAuthority: 0.1,
		EvidenceCount: 1, VerifyCount: 0,
	})
	if strong <= weak {
		t.Fatalf("strong salience %.3f must exceed weak %.3f", strong, weak)
	}
	for _, v := range []float64{strong, weak} {
		if v < 0 || v > 1 {
			t.Fatalf("salience %.3f out of [0,1]", v)
		}
	}
}

func TestSalience_MonotonicInSignals(t *testing.T) {
	base := SalienceInputs{Type: domain.ClaimTypeFact, Confidence: 0.5, EvidenceCount: 1}

	more := base
	more.EvidenceCount = 5
	if Salience(more) <= Salience(base) {
		t.Fatal("more corroborating evidence must not lower salience")
	}

	surer := base
	surer.Confidence = 0.9
	if Salience(surer) <= Salience(base) {
		t.Fatal("higher confidence must raise salience")
	}

	verified := base
	verified.VerifyCount = 3
	if Salience(verified) <= Salience(base) {
		t.Fatal("verification must raise salience")
	}
}

func TestSalience_ClampsNegativeInputs(t *testing.T) {
	// Garbage inputs must not escape [0,1] nor panic.
	got := Salience(SalienceInputs{
		Type: domain.ClaimTypeFact, Confidence: -5, SourceAuthority: 2,
		EvidenceCount: -3, VerifyCount: -1,
	})
	if got < 0 || got > 1 {
		t.Fatalf("salience %.3f out of [0,1] on garbage input", got)
	}
}

func TestSalienceOf_ReadsClaimFields(t *testing.T) {
	c := domain.Claim{
		Type: domain.ClaimTypeDecision, Confidence: 0.8,
		SourceAuthority: 0.7, VerifyCount: 2,
	}
	if got := SalienceOf(c, 4); got != Salience(SalienceInputs{
		Type: c.Type, Confidence: c.Confidence, SourceAuthority: c.SourceAuthority,
		EvidenceCount: 4, VerifyCount: c.VerifyCount,
	}) {
		t.Fatalf("SalienceOf must read fields off the claim, got %.3f", got)
	}
}
