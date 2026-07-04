package trust

import (
	"math"

	"go.klarlabs.de/mnemos/internal/domain"
)

// Salience is distinct from trust. Trust answers "do I have reason to believe
// this is still true?" — it decays with time. SALIENCE answers "does this matter
// enough to keep, even if I rarely recall it and its trust has faded?" — an
// intrinsic importance that does NOT decay. It is the brain's amygdala/dopamine
// importance gate: a consequential-but-quiet memory (a Sev-1 post-mortem, a
// high-authority architectural decision) should survive the consolidation
// "sleep" pass while a low-confidence, unverified, single-source aside is let go.
//
// This is the rule-based analog of Generative Agents' LLM "poignancy" score — no
// LLM, no I/O, deterministic, computed from signals already on the claim. It is
// deliberately INTRINSIC: every input here is stable, so the score is the same
// whether computed at encoding or on demand at forgetting time. The corpus-
// relative NOVELTY term (embedding distance from existing memory at encode time)
// and chronos SURPRISE are the write-time enhancement tracked for a later pass —
// they must be captured at encoding and so need a persisted column, unlike these.

// SalienceInputs are the intrinsic importance signals read off a claim.
type SalienceInputs struct {
	Type            domain.ClaimType
	Confidence      float64 // 0..1, the extractor/source confidence
	SourceAuthority float64 // 0..1, how authoritative the source is
	EvidenceCount   int     // distinct corroborating evidence links
	VerifyCount     int     // human/tool re-verifications
}

// Salience scores a claim's intrinsic importance in [0,1]. The weights sum to 1
// and each term is independently clamped, so no single signal can dominate:
//   - confidence (0.35): a claim the source was sure of matters more;
//   - corroboration (0.25): 1 - 1/(1+evidenceCount) — more independent evidence,
//     saturating (the 10th source adds little over the 3rd);
//   - type prior (0.25): decisions and verified test results are consequential;
//     bare facts middling; hypotheses speculative and least protected;
//   - source authority (0.10): who said it;
//   - verification (0.05): min(verifyCount,3)/3 — re-confirmation, quickly
//     saturating.
//
// Authority and verification carry the least weight deliberately: they are often
// zero on a freshly-remembered claim (set later by curation / re-verification),
// so leaning on them would make every new claim look unimportant. The signals
// present at encoding — confidence, evidence, and kind — carry the score.
func Salience(in SalienceInputs) float64 {
	typePrior := 0.5
	switch in.Type {
	case domain.ClaimTypeDecision, domain.ClaimTypeTestResult:
		typePrior = 1.0
	case domain.ClaimTypeFact:
		typePrior = 0.6
	case domain.ClaimTypeHypothesis:
		typePrior = 0.3
	}
	corroboration := 1.0 - 1.0/float64(1+maxInt(0, in.EvidenceCount))
	verification := math.Min(float64(maxInt(0, in.VerifyCount)), 3) / 3.0

	s := 0.35*clamp01(in.Confidence) +
		0.25*corroboration +
		0.25*typePrior +
		0.10*clamp01(in.SourceAuthority) +
		0.05*verification
	return clamp01(s)
}

// SalienceOf is the convenience form that reads the inputs straight off a claim
// with its corroborating-evidence count.
func SalienceOf(c domain.Claim, evidenceCount int) float64 {
	return Salience(SalienceInputs{
		Type:            c.Type,
		Confidence:      c.Confidence,
		SourceAuthority: c.SourceAuthority,
		EvidenceCount:   evidenceCount,
		VerifyCount:     c.VerifyCount,
	})
}
