package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// FeedbackRepository is the SQLite-backed implementation of
// [ports.FeedbackRepository]. Persists the per-claim feedback state
// in the claim_feedback side table; the claim row itself stays slim.
type FeedbackRepository struct {
	db *sql.DB
}

// NewFeedbackRepository returns a repository bound to the given *sql.DB.
func NewFeedbackRepository(db *sql.DB) FeedbackRepository {
	return FeedbackRepository{db: db}
}

// Get returns the row for claimID, or ok=false when no row exists.
// "No row" is not an error — feedback is sparse by design.
func (r FeedbackRepository) Get(ctx context.Context, claimID string) (domain.ClaimFeedback, bool, error) {
	var (
		streak       int64
		helpful      int64
		lastFeedback string
		lastNote     string
	)
	err := r.db.QueryRowContext(ctx, `
SELECT negative_feedback_streak, helpful_count, last_feedback_at, last_feedback_note
FROM claim_feedback
WHERE claim_id = ?`, claimID).Scan(&streak, &helpful, &lastFeedback, &lastNote)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ClaimFeedback{}, false, nil
	}
	if err != nil {
		return domain.ClaimFeedback{}, false, fmt.Errorf("get claim_feedback %s: %w", claimID, err)
	}
	out := domain.ClaimFeedback{
		ClaimID:                claimID,
		NegativeFeedbackStreak: int(streak),
		HelpfulCount:           int(helpful),
		LastFeedbackNote:       lastNote,
	}
	if lastFeedback != "" {
		t, perr := time.Parse(time.RFC3339Nano, lastFeedback)
		if perr != nil {
			return domain.ClaimFeedback{}, false, fmt.Errorf("parse last_feedback_at: %w", perr)
		}
		out.LastFeedbackAt = t
	}
	return out, true, nil
}

// Upsert writes the state atomically. last_feedback_at is rendered as
// RFC3339Nano so it sorts naturally and round-trips through Get.
func (r FeedbackRepository) Upsert(ctx context.Context, state domain.ClaimFeedback) error {
	if state.ClaimID == "" {
		return errors.New("feedback upsert: claim_id required")
	}
	var lastFeedbackStr string
	if !state.LastFeedbackAt.IsZero() {
		lastFeedbackStr = state.LastFeedbackAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO claim_feedback (claim_id, negative_feedback_streak, helpful_count, last_feedback_at, last_feedback_note)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(claim_id) DO UPDATE SET
  negative_feedback_streak = excluded.negative_feedback_streak,
  helpful_count = excluded.helpful_count,
  last_feedback_at = excluded.last_feedback_at,
  last_feedback_note = excluded.last_feedback_note
`, state.ClaimID, state.NegativeFeedbackStreak, state.HelpfulCount, lastFeedbackStr, state.LastFeedbackNote)
	if err != nil {
		return fmt.Errorf("upsert claim_feedback %s: %w", state.ClaimID, err)
	}
	return nil
}
