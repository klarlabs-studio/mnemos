package beliefops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/query"
	"go.klarlabs.de/mnemos/internal/store"
)

// Edit names the belief a governance edit acts on, why, and who asked.
type Edit struct {
	ClaimID string
	Reason  string
	Actor   string
}

// StatusChange is the receipt of a deprecation.
type StatusChange struct {
	ClaimID   string `json:"belief_id"`
	OldStatus string `json:"old_status"`
	NewStatus string `json:"new_status"`
}

// Deprecate marks a belief deprecated, recording the reason (fallbackReason
// when the edit gives none) on the status transition. The belief, its
// evidence and its history stay queryable. MCP memory_deprecate and forget are
// both this operation; they differ only in the fallback reason they record
// ([ReasonDeprecate], [ReasonForget]).
func Deprecate(ctx context.Context, conn *store.Conn, w Writer, e Edit, fallbackReason string) (StatusChange, error) {
	claims, err := loadClaims(ctx, conn, strings.TrimSpace(e.ClaimID))
	if err != nil {
		return StatusChange{}, err
	}
	c := claims[0]
	old := c.Status
	c.Status = domain.ClaimStatusDeprecated
	why := govwrite.ClaimReason{Reason: reasonOr(e.Reason, fallbackReason), ChangedBy: e.Actor}
	if _, err := w.Claims(ctx, []domain.Claim{c}, why); err != nil {
		return StatusChange{}, fmt.Errorf("deprecate belief: %w", err)
	}
	return StatusChange{ClaimID: c.ID, OldStatus: string(old), NewStatus: string(c.Status)}, nil
}

// UpdateRequest rewrites a belief's text and, when Confidence > 0, its
// confidence.
type UpdateRequest struct {
	Edit
	Text       string
	Confidence float64
}

// Validate reports a malformed update (ErrInvalid) without reading the store.
func (r UpdateRequest) Validate() error {
	if strings.TrimSpace(r.ClaimID) == "" {
		return invalid("belief id is required")
	}
	if strings.TrimSpace(r.Text) == "" {
		return invalid("new text is required")
	}
	if r.Confidence < 0 || r.Confidence > 1 {
		return invalid("confidence must be in [0, 1]")
	}
	return nil
}

// Revision is the receipt of an update.
type Revision struct {
	ClaimID    string  `json:"belief_id"`
	OldText    string  `json:"old_text"`
	NewText    string  `json:"new_text"`
	Confidence float64 `json:"confidence"`
}

// Update rewrites a belief's text (and optionally its confidence), recording
// the reason on the change.
func Update(ctx context.Context, conn *store.Conn, w Writer, req UpdateRequest) (Revision, error) {
	if err := req.Validate(); err != nil {
		return Revision{}, err
	}
	text := strings.TrimSpace(req.Text)
	claims, err := loadClaims(ctx, conn, strings.TrimSpace(req.ClaimID))
	if err != nil {
		return Revision{}, err
	}
	c := claims[0]
	oldText := c.Text
	c.Text = text
	if req.Confidence > 0 {
		c.Confidence = req.Confidence
	}
	why := govwrite.ClaimReason{Reason: reasonOr(req.Reason, ReasonUpdate), ChangedBy: req.Actor}
	if _, err := w.Claims(ctx, []domain.Claim{c}, why); err != nil {
		return Revision{}, fmt.Errorf("update belief: %w", err)
	}
	return Revision{ClaimID: c.ID, OldText: oldText, NewText: text, Confidence: c.Confidence}, nil
}

// ResolveRequest picks the winner of two contradicting beliefs.
type ResolveRequest struct {
	WinnerID string
	LoserID  string
	Reason   string
	Actor    string
}

// Validate reports a malformed resolution (ErrInvalid) without reading the
// store.
func (r ResolveRequest) Validate() error {
	winner, loser := strings.TrimSpace(r.WinnerID), strings.TrimSpace(r.LoserID)
	if winner == "" || loser == "" {
		return invalid("winner_id and loser_id are required")
	}
	if winner == loser {
		return invalid("winner_id and loser_id must differ")
	}
	return nil
}

// Resolution is the receipt of a resolved dissonance.
type Resolution struct {
	WinnerID string `json:"winner_id"`
	LoserID  string `json:"loser_id"`
}

// Resolve moves the winner to resolved and the loser to deprecated, both
// transitions carrying the reason. Both beliefs must exist.
func Resolve(ctx context.Context, conn *store.Conn, w Writer, req ResolveRequest) (Resolution, error) {
	if err := req.Validate(); err != nil {
		return Resolution{}, err
	}
	winner, loser := strings.TrimSpace(req.WinnerID), strings.TrimSpace(req.LoserID)
	claims, err := loadClaims(ctx, conn, winner, loser)
	if err != nil {
		return Resolution{}, err
	}
	claims[0].Status = domain.ClaimStatusResolved
	claims[1].Status = domain.ClaimStatusDeprecated
	why := govwrite.ClaimReason{Reason: reasonOr(req.Reason, ReasonResolve), ChangedBy: req.Actor}
	if _, err := w.Claims(ctx, claims, why); err != nil {
		return Resolution{}, fmt.Errorf("resolve dissonance: %w", err)
	}
	return Resolution{WinnerID: winner, LoserID: loser}, nil
}

// Escalation is an agent's request for a human decision on a belief.
type Escalation struct {
	ClaimID          string `json:"belief_id"`
	Action           string `json:"action"`
	EscalationReason string `json:"escalation_reason"`
	Rationale        string `json:"rationale"`
}

// Escalate records that the caller cannot resolve a belief autonomously and
// returns the escalation verdict (query.Engine.EscalateClaimForAgent). It
// writes nothing: the verdict is the audit record the caller carries.
func Escalate(ctx context.Context, conn *store.Conn, claimID, reason string) (Escalation, error) {
	id := strings.TrimSpace(claimID)
	if _, err := loadClaims(ctx, conn, id); err != nil {
		return Escalation{}, err
	}
	v, err := query.NewEngine(conn.Events, conn.Claims, conn.Relationships).EscalateClaimForAgent(ctx, id, reason)
	if err != nil {
		return Escalation{}, err
	}
	return Escalation{ClaimID: id, Action: string(v.Action), EscalationReason: v.EscalationReason, Rationale: v.Rationale}, nil
}

// Verification is the receipt of an explicit re-verification.
type Verification struct {
	ClaimID    string    `json:"belief_id"`
	VerifiedAt time.Time `json:"verified_at"`
}

// Verify re-verifies a belief against fresh evidence: it bumps last_verified
// and verify_count and stamps last_confirmed (govwrite MarkVerified), so the
// trust score follows. MCP memory_promote is this operation. The belief must
// exist: MarkVerified on an unknown id would touch nothing and report success.
func Verify(ctx context.Context, conn *store.Conn, w Writer, claimID string, now time.Time) (Verification, error) {
	id := strings.TrimSpace(claimID)
	if _, err := loadClaims(ctx, conn, id); err != nil {
		return Verification{}, err
	}
	now = now.UTC()
	if err := w.MarkVerified(ctx, id, now, 0); err != nil {
		return Verification{}, fmt.Errorf("verify belief: %w", err)
	}
	return Verification{ClaimID: id, VerifiedAt: now}, nil
}
