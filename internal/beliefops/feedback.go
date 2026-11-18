package beliefops

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/store"
)

// FeedbackPolicy tunes how an unhelpful vote moves a belief.
type FeedbackPolicy struct {
	// Decay multiplies the belief's confidence on each unhelpful vote. 0.9 ≈
	// "each push-back trims 10%": shallow enough that one bad vote cannot sink
	// a well-corroborated belief, steep enough to be observable.
	Decay float64
	// ContestThreshold is the consecutive-unhelpful count at which an active
	// belief becomes contested: enough to ride out one reviewer's bad day, low
	// enough to surface real drift.
	ContestThreshold int
}

// FeedbackPolicyFromEnv reads MNEMOS_FEEDBACK_DECAY (default 0.9) and
// MNEMOS_FEEDBACK_CONTEST_THRESHOLD (default 3). Every transport uses it, so
// an operator's tuning applies whichever surface the vote arrives on.
func FeedbackPolicyFromEnv() FeedbackPolicy {
	p := FeedbackPolicy{Decay: 0.9, ContestThreshold: 3}
	if v, err := strconv.ParseFloat(os.Getenv("MNEMOS_FEEDBACK_DECAY"), 64); err == nil {
		p.Decay = v
	}
	if v, err := strconv.Atoi(os.Getenv("MNEMOS_FEEDBACK_CONTEST_THRESHOLD")); err == nil {
		p.ContestThreshold = v
	}
	return p
}

// FeedbackRequest is one helpful/unhelpful vote on a belief.
type FeedbackRequest struct {
	ClaimID string
	Helpful bool
	// Note is kept verbatim (trimmed) on the belief's feedback row.
	Note  string
	Actor string
}

// FeedbackResult is the belief's state after the vote, so a caller can react
// to the streak transition without a second read.
type FeedbackResult struct {
	ClaimID                string  `json:"claim_id"`
	Helpful                bool    `json:"helpful"`
	NewStatus              string  `json:"new_status"`
	NewConfidence          float64 `json:"new_confidence"`
	NegativeFeedbackStreak int     `json:"negative_feedback_streak"`
	HelpfulCount           int     `json:"helpful_count"`
	AutoContested          bool    `json:"auto_contested"`
}

// RecordFeedback applies one vote. A helpful vote resets the negative streak
// and adds to the lifetime helpful tally; an unhelpful one extends the streak,
// decays confidence (and the corroboration component in lockstep), and
// contests an active belief once the streak reaches the threshold. Both the
// belief and its feedback row are written through w.
//
// The asymmetry is deliberate: HelpfulCount is a lifetime corroboration tally
// while the streak counts CONSECUTIVE misses, so one good recall clears a run
// of bad ones rather than merely offsetting it.
func RecordFeedback(ctx context.Context, conn *store.Conn, w Writer, req FeedbackRequest, policy FeedbackPolicy, now time.Time) (FeedbackResult, error) {
	if conn.Feedback == nil {
		return FeedbackResult{}, fmt.Errorf("feedback: %w", ErrUnsupported)
	}
	claims, err := loadClaims(ctx, conn, strings.TrimSpace(req.ClaimID))
	if err != nil {
		return FeedbackResult{}, err
	}
	claim := claims[0]
	state, _, err := conn.Feedback.Get(ctx, claim.ID)
	if err != nil {
		return FeedbackResult{}, fmt.Errorf("load feedback state: %w", err)
	}
	state.ClaimID = claim.ID
	state.LastFeedbackAt = now.UTC()
	state.LastFeedbackNote = strings.TrimSpace(req.Note)

	autoContested := false
	if req.Helpful {
		state.NegativeFeedbackStreak = 0
		state.HelpfulCount++
	} else {
		state.NegativeFeedbackStreak++
		claim.Confidence *= policy.Decay
		if claim.Confidence < 0 {
			claim.Confidence = 0
		}
		if v, ok := claim.ConfidenceComponents["corroboration"]; ok {
			claim.ConfidenceComponents["corroboration"] = v * policy.Decay
		}
		if state.NegativeFeedbackStreak >= policy.ContestThreshold && claim.Status == domain.ClaimStatusActive {
			claim.Status = domain.ClaimStatusContested
			autoContested = true
		}
	}

	reason := "agent-feedback"
	if !req.Helpful {
		reason = "agent-feedback-negative"
	}
	if _, err := w.Claims(ctx, []domain.Claim{claim}, govwrite.ClaimReason{Reason: reason, ChangedBy: req.Actor}); err != nil {
		return FeedbackResult{}, fmt.Errorf("update belief: %w", err)
	}
	if _, err := w.Feedback(ctx, state); err != nil {
		return FeedbackResult{}, fmt.Errorf("store feedback: %w", err)
	}
	return FeedbackResult{
		ClaimID:                claim.ID,
		Helpful:                req.Helpful,
		NewStatus:              string(claim.Status),
		NewConfidence:          claim.Confidence,
		NegativeFeedbackStreak: state.NegativeFeedbackStreak,
		HelpfulCount:           state.HelpfulCount,
		AutoContested:          autoContested,
	}, nil
}
