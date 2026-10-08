package query

import (
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// MinTrust gates on canonical trust (ADR 0026 §2). A belief whose credibility
// is lifted above the bar by authority and citations, but whose trust is
// below it, must be filtered — "trust >= 0.5" means the trust Get reports.
func TestAdmitClaims_MinTrustGatesOnCanonicalTrust(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	lowTrustHighCred := domain.Claim{
		ID: "c1", Text: "x", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive,
		Confidence: 0.9, TrustScore: 0.4, SourceAuthority: 1, CitationCount: 50,
		CreatedAt: now, ValidFrom: now, LastExecuted: now,
	}
	out := admitClaims([]domain.Claim{lowTrustHighCred}, AnswerOptions{}, now)
	if len(out) != 1 {
		t.Fatalf("fixture: claim not admitted without a trust bar")
	}
	if out[0].Credibility < 0.5 {
		t.Fatalf("fixture: credibility %.3f is not above the bar, so this test would prove nothing", out[0].Credibility)
	}
	if out[0].TrustScore != 0.4 {
		t.Fatalf("admission rewrote TrustScore to %v; it must stay the canonical value", out[0].TrustScore)
	}
	if got := admitClaims([]domain.Claim{lowTrustHighCred}, AnswerOptions{MinTrust: 0.5}, now); len(got) != 0 {
		t.Fatalf("MinTrust 0.5 admitted a belief with trust 0.4 because its credibility was %.3f", out[0].Credibility)
	}
}
