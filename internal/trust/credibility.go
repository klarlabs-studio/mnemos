package trust

import (
	"fmt"
	"math"
	"time"

	"github.com/felixgeelhaar/mnemos/internal/domain"
)

// CredibilityInputs contains provenance signals for source credibility scoring.
type CredibilityInputs struct {
	CurrentTrust    float64
	SourceAuthority float64
	// AgentAuthority is the authority score of the agent that submitted
	// the claim (domain.Agent.AuthorityScore). Zero means unknown — no
	// penalty is applied so existing callers that don't pass an agent
	// continue to behave as before.
	AgentAuthority float64
	Liveness       domain.LivenessStatus
	CitationCount  int
	LastExecuted   time.Time
	LastVerified   time.Time
	ValidFrom      time.Time
	CreatedAt      time.Time
	Now            time.Time

	// Test provenance — populated when the underlying claim is a
	// test_result. When TestLastRunAt is non-zero it overrides claim-level
	// recency: a test claim's recency should reflect when the test last
	// ran, not when the claim row was last touched. PassCount/FailCount
	// drive a separate decisiveness signal: a test that passed 50/50 is
	// less decisive than one that passed 50/0, even at equal recency.
	IsTest        bool
	TestLastRunAt time.Time
	TestPassCount int
	TestFailCount int
}

// ScoreCredibility combines trust + provenance signals into a score and
// human-readable rationale.
func ScoreCredibility(in CredibilityInputs) (float64, string) {
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	base := clamp01(in.CurrentTrust)
	if base == 0 {
		base = 0.5
	}

	authority := clamp01(in.SourceAuthority)
	if in.SourceAuthority == 0 {
		authority = 0.5
	}

	citationSignal := clamp01(math.Log1p(float64(maxInt(0, in.CitationCount))) / math.Log(11))

	// Recency: for test_result claims with a recorded run timestamp, prefer
	// that over claim-level timestamps — a test that ran yesterday is more
	// trustworthy than one whose claim row was created yesterday but ran a
	// year ago. Falls back to EffectiveExecutionTime otherwise.
	var ref time.Time
	if in.IsTest && !in.TestLastRunAt.IsZero() {
		ref = in.TestLastRunAt
	} else {
		ref = EffectiveExecutionTime(in.LastExecuted, in.LastVerified, in.ValidFrom, in.CreatedAt)
	}
	recencySignal := 0.5
	if !ref.IsZero() {
		days := now.Sub(ref).Hours() / 24
		if days < 0 {
			days = 0
		}
		recencySignal = clamp01(math.Exp(-days / 180.0))
	}

	livenessSignal := livenessWeight(in.Liveness)

	// Test decisiveness: |pass-fail|/total. 50/50 → 0 (flaky); 10/0 → 1.
	// Only contributes for test claims; non-tests get 0.5 (neutral) so the
	// signal is always present in the rationale and weights stay constant.
	testDecisiveness := 0.5
	if in.IsTest {
		total := in.TestPassCount + in.TestFailCount
		if total > 0 {
			diff := in.TestPassCount - in.TestFailCount
			if diff < 0 {
				diff = -diff
			}
			testDecisiveness = float64(diff) / float64(total)
		} else {
			testDecisiveness = 0
		}
	}

	score := clamp01(
		base*0.50 +
			authority*0.15 +
			citationSignal*0.13 +
			recencySignal*0.10 +
			livenessSignal*0.05 +
			testDecisiveness*0.07,
	)

	// AgentAuthority is a multiplicative final factor: an agent with a
	// known poor track record (low AuthorityScore) deflates the score;
	// a zero value means "unknown" — no penalty, treated as neutral 1.0.
	agentFactor := 1.0
	if in.AgentAuthority > 0 {
		agentFactor = clamp01(in.AgentAuthority)
	}
	score = clamp01(score * agentFactor)

	rationale := fmt.Sprintf(
		"base=%.2f authority=%.2f citations=%d(%.2f) recency=%.2f liveness=%s agent_authority=%.2f",
		base,
		authority,
		in.CitationCount,
		citationSignal,
		recencySignal,
		in.Liveness,
		agentFactor,
	)
	if in.IsTest {
		rationale += fmt.Sprintf(
			" test_decisiveness=%d/%d(%.2f)",
			in.TestPassCount,
			in.TestPassCount+in.TestFailCount,
			testDecisiveness,
		)
	}

	return score, rationale
}

func livenessWeight(s domain.LivenessStatus) float64 {
	switch s {
	case domain.LivenessLive:
		return 1.0
	case domain.LivenessStale:
		return 0.75
	case domain.LivenessZombie:
		return 0.65
	case domain.LivenessDead:
		return 0.25
	default:
		return 0.5
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
