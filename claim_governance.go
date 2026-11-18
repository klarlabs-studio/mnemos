package mnemos

import (
	"context"
	"fmt"
	"time"

	axidomain "go.klarlabs.de/axi/domain"

	"go.klarlabs.de/mnemos/internal/beliefops"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/kernel"
)

// Claim governance, feedback and the per-claim provenance and history reads
// on the library surface. Each method is a thin adapter over
// internal/beliefops, the one implementation the MCP tools, REST routes and
// gRPC methods also call, so a deprecation or a feedback vote means the same
// thing whichever surface carries it.
//
// The writes are dispatched through the library's own governed kernel (one
// action per operation, so each lands on [Memory.LastWriteSession]); inside
// the executor beliefops writes through govwrite.Direct, the same executor
// bodies the daemon's governed writer runs. The request is checked BEFORE
// dispatch (beliefops.Require and the requests' Validate) because the kernel
// reports an executor failure as text, which would lose the sentinel.

// ErrClaimNotFound is returned (wrapped) when a named claim does not exist,
// by the governance edits and by [Memory.Expect] (a prediction about nothing
// could never be credited). Match it with errors.Is.
var ErrClaimNotFound = beliefops.ErrNotFound

// ErrInvalidArgument is returned (wrapped) for a malformed request: a blank
// id, empty replacement text, a confidence outside [0, 1], a contradiction
// resolved against itself. Match it with errors.Is.
var ErrInvalidArgument = beliefops.ErrInvalid

// ErrUnsupported is returned (wrapped) when the backend lacks the storage an
// operation needs (feedback state, version history).
var ErrUnsupported = beliefops.ErrUnsupported

// Library write actions for the governance edits and feedback.
const (
	actionDeprecateClaim       = "deprecate_claim"
	actionUpdateClaim          = "update_claim"
	actionResolveContradiction = "resolve_contradiction"
	actionVerifyClaim          = "verify_claim"
	actionRecordFeedback       = "record_feedback"
)

// ClaimStatusChange is the receipt of [Memory.DeprecateClaim].
type ClaimStatusChange struct {
	ClaimID   string
	OldStatus string
	NewStatus string
}

// ClaimRevision is the receipt of [Memory.UpdateClaim].
type ClaimRevision struct {
	ClaimID    string
	OldText    string
	NewText    string
	Confidence float64
}

// ClaimEscalation is a request for a human decision on a claim.
type ClaimEscalation struct {
	ClaimID          string
	EscalationReason string
	// Rationale is the audit sentence naming the claim and the reason.
	Rationale string
}

// FeedbackResult is a claim's state after [Memory.RecordFeedback].
type FeedbackResult struct {
	ClaimID        string
	Helpful        bool
	Status         string
	Confidence     float64
	NegativeStreak int
	HelpfulCount   int
	// AutoContested is set when this vote crossed the negative-streak
	// threshold and moved an active claim to contested.
	AutoContested bool
}

// ClaimProvenance explains how a claim's trust score was computed.
type ClaimProvenance struct {
	ClaimID string
	Text    string
	// Score is the final credibility score in [0, 1].
	Score float64
	// Signals is the per-component breakdown, largest contribution first.
	Signals        []ProvenanceSignal
	Rationale      string
	ProseRationale string
	SourceDocument string
	Liveness       string
}

// ProvenanceSignal is one component of a [ClaimProvenance].
type ProvenanceSignal struct {
	Name         string
	Value        float64
	Weight       float64
	Contribution float64
	Detail       string
}

// ClaimVersion is one entry of a claim's version chain.
type ClaimVersion struct {
	ClaimID    string
	Version    int
	Text       string
	Confidence float64
	Status     string
	WrittenAt  time.Time
	WrittenBy  string
}

type claimEditInput struct {
	Edit beliefops.Edit
}

type updateClaimInput struct {
	Req beliefops.UpdateRequest
}

type resolveContradictionInput struct {
	Req beliefops.ResolveRequest
}

type verifyClaimInput struct {
	ClaimID string
	At      time.Time
}

type recordFeedbackInput struct {
	Req beliefops.FeedbackRequest
	At  time.Time
}

// governanceActions are the write actions this file adds to the library
// kernel; writeActions includes them.
func governanceActions() []kernel.Action {
	return []kernel.Action{
		{Name: actionDeprecateClaim, Effect: axidomain.EffectWriteLocal, Idempotent: true,
			Description: "Deprecate a claim, recording the reason on the status transition."},
		{Name: actionUpdateClaim, Effect: axidomain.EffectWriteLocal, Idempotent: true,
			Description: "Rewrite a claim's text (and optionally its confidence), recording the reason."},
		{Name: actionResolveContradiction, Effect: axidomain.EffectWriteLocal, Idempotent: true,
			Description: "Resolve a contradiction: the winner becomes resolved, the loser deprecated."},
		{Name: actionVerifyClaim, Effect: axidomain.EffectWriteLocal, Idempotent: false,
			Description: "Re-verify a claim: bump last_verified and verify_count, stamp last_confirmed."},
		{Name: actionRecordFeedback, Effect: axidomain.EffectWriteLocal, Idempotent: false,
			Description: "Record a helpful/unhelpful vote on a claim; unhelpful votes decay confidence and can contest it."},
	}
}

// governanceExecutors binds governanceActions, keyed by kernel.ExecutorRef.
func governanceExecutors(m *memory) map[string]axidomain.ActionExecutor {
	return map[string]axidomain.ActionExecutor{
		kernel.ExecutorRef(actionDeprecateClaim): beliefExecutor[claimEditInput, beliefops.StatusChange]{m: m, action: actionDeprecateClaim,
			run: func(ctx context.Context, m *memory, in claimEditInput) (beliefops.StatusChange, error) {
				return beliefops.Deprecate(ctx, m.conn, govwrite.Direct(m.conn), in.Edit, beliefops.ReasonDeprecate)
			}},
		kernel.ExecutorRef(actionUpdateClaim): beliefExecutor[updateClaimInput, beliefops.Revision]{m: m, action: actionUpdateClaim,
			run: func(ctx context.Context, m *memory, in updateClaimInput) (beliefops.Revision, error) {
				return beliefops.Update(ctx, m.conn, govwrite.Direct(m.conn), in.Req)
			}},
		kernel.ExecutorRef(actionResolveContradiction): beliefExecutor[resolveContradictionInput, beliefops.Resolution]{m: m, action: actionResolveContradiction,
			run: func(ctx context.Context, m *memory, in resolveContradictionInput) (beliefops.Resolution, error) {
				return beliefops.Resolve(ctx, m.conn, govwrite.Direct(m.conn), in.Req)
			}},
		kernel.ExecutorRef(actionVerifyClaim): beliefExecutor[verifyClaimInput, beliefops.Verification]{m: m, action: actionVerifyClaim,
			run: func(ctx context.Context, m *memory, in verifyClaimInput) (beliefops.Verification, error) {
				return beliefops.Verify(ctx, m.conn, govwrite.Direct(m.conn), in.ClaimID, in.At)
			}},
		kernel.ExecutorRef(actionRecordFeedback): beliefExecutor[recordFeedbackInput, beliefops.FeedbackResult]{m: m, action: actionRecordFeedback,
			run: func(ctx context.Context, m *memory, in recordFeedbackInput) (beliefops.FeedbackResult, error) {
				return beliefops.RecordFeedback(ctx, m.conn, govwrite.Direct(m.conn), in.Req, beliefops.FeedbackPolicyFromEnv(), in.At)
			}},
	}
}

// beliefExecutor lifts a beliefops call into a library write executor that
// records one evidence row naming the action and its result.
type beliefExecutor[In, Out any] struct {
	m      *memory
	action string
	run    func(context.Context, *memory, In) (Out, error)
}

// Execute runs the operation and records its result on the evidence chain.
func (e beliefExecutor[In, Out]) Execute(ctx context.Context, input any, _ axidomain.CapabilityInvoker) (axidomain.ExecutionResult, []axidomain.EvidenceRecord, error) {
	in, ok := writeInput[In](input)
	if !ok {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("%s: unexpected input type %T", e.action, input)
	}
	out, err := e.run(ctx, e.m, in)
	if err != nil {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("%s: %w", e.action, err)
	}
	return axidomain.ExecutionResult{
		Data:    out,
		Summary: fmt.Sprintf("%s: %+v", e.action, out),
	}, []axidomain.EvidenceRecord{{
		Kind: "mnemos." + e.action, Source: evidenceSourceLibrary,
		Value: map[string]any{"result": fmt.Sprintf("%+v", out), "changed_by": e.m.actorID},
	}}, nil
}

// DeprecateClaim implements [Memory.DeprecateClaim].
func (m *memory) DeprecateClaim(ctx context.Context, claimID, reason string) (ClaimStatusChange, error) {
	if err := beliefops.Require(ctx, m.conn, claimID); err != nil {
		return ClaimStatusChange{}, fmt.Errorf("mnemos: DeprecateClaim: %w", err)
	}
	out, err := dispatchWrite[beliefops.StatusChange](ctx, m, actionDeprecateClaim, claimEditInput{
		Edit: beliefops.Edit{ClaimID: claimID, Reason: reason, Actor: m.actorID},
	})
	if err != nil {
		return ClaimStatusChange{}, err
	}
	return ClaimStatusChange(out), nil
}

// UpdateClaim implements [Memory.UpdateClaim].
func (m *memory) UpdateClaim(ctx context.Context, claimID, text string, confidence float64, reason string) (ClaimRevision, error) {
	req := beliefops.UpdateRequest{Edit: beliefops.Edit{ClaimID: claimID, Reason: reason, Actor: m.actorID}, Text: text, Confidence: confidence}
	if err := req.Validate(); err != nil {
		return ClaimRevision{}, fmt.Errorf("mnemos: UpdateClaim: %w", err)
	}
	if err := beliefops.Require(ctx, m.conn, claimID); err != nil {
		return ClaimRevision{}, fmt.Errorf("mnemos: UpdateClaim: %w", err)
	}
	out, err := dispatchWrite[beliefops.Revision](ctx, m, actionUpdateClaim, updateClaimInput{Req: req})
	if err != nil {
		return ClaimRevision{}, err
	}
	m.embedAsync(out.ClaimID, "claim", out.NewText)
	return ClaimRevision(out), nil
}

// ResolveContradiction implements [Memory.ResolveContradiction].
func (m *memory) ResolveContradiction(ctx context.Context, winnerID, loserID, reason string) error {
	req := beliefops.ResolveRequest{WinnerID: winnerID, LoserID: loserID, Reason: reason, Actor: m.actorID}
	if err := req.Validate(); err != nil {
		return fmt.Errorf("mnemos: ResolveContradiction: %w", err)
	}
	if err := beliefops.Require(ctx, m.conn, winnerID, loserID); err != nil {
		return fmt.Errorf("mnemos: ResolveContradiction: %w", err)
	}
	_, err := dispatchWrite[beliefops.Resolution](ctx, m, actionResolveContradiction, resolveContradictionInput{Req: req})
	return err
}

// EscalateClaim implements [Memory.EscalateClaim]. It writes nothing.
func (m *memory) EscalateClaim(ctx context.Context, claimID, reason string) (ClaimEscalation, error) {
	esc, err := beliefops.Escalate(ctx, m.conn, claimID, reason)
	if err != nil {
		return ClaimEscalation{}, fmt.Errorf("mnemos: EscalateClaim: %w", err)
	}
	return ClaimEscalation{ClaimID: esc.ClaimID, EscalationReason: esc.EscalationReason, Rationale: esc.Rationale}, nil
}

// VerifyClaim implements [Memory.VerifyClaim].
func (m *memory) VerifyClaim(ctx context.Context, claimID string) (time.Time, error) {
	if err := beliefops.Require(ctx, m.conn, claimID); err != nil {
		return time.Time{}, fmt.Errorf("mnemos: VerifyClaim: %w", err)
	}
	out, err := dispatchWrite[beliefops.Verification](ctx, m, actionVerifyClaim, verifyClaimInput{ClaimID: claimID, At: time.Now()})
	if err != nil {
		return time.Time{}, err
	}
	return out.VerifiedAt, nil
}

// RecordFeedback implements [Memory.RecordFeedback].
func (m *memory) RecordFeedback(ctx context.Context, claimID string, helpful bool, note string) (FeedbackResult, error) {
	if m.conn.Feedback == nil {
		return FeedbackResult{}, fmt.Errorf("mnemos: RecordFeedback: feedback: %w", ErrUnsupported)
	}
	if err := beliefops.Require(ctx, m.conn, claimID); err != nil {
		return FeedbackResult{}, fmt.Errorf("mnemos: RecordFeedback: %w", err)
	}
	out, err := dispatchWrite[beliefops.FeedbackResult](ctx, m, actionRecordFeedback, recordFeedbackInput{
		Req: beliefops.FeedbackRequest{ClaimID: claimID, Helpful: helpful, Note: note, Actor: m.actorID},
		At:  time.Now(),
	})
	if err != nil {
		return FeedbackResult{}, err
	}
	return FeedbackResult{
		ClaimID: out.ClaimID, Helpful: out.Helpful, Status: out.NewStatus, Confidence: out.NewConfidence,
		NegativeStreak: out.NegativeFeedbackStreak, HelpfulCount: out.HelpfulCount, AutoContested: out.AutoContested,
	}, nil
}

// ClaimProvenance implements [Memory.ClaimProvenance].
func (m *memory) ClaimProvenance(ctx context.Context, claimID string) (ClaimProvenance, error) {
	rep, err := beliefops.Provenance(ctx, m.conn, claimID)
	if err != nil {
		return ClaimProvenance{}, fmt.Errorf("mnemos: ClaimProvenance: %w", err)
	}
	out := ClaimProvenance{
		ClaimID: rep.ClaimID, Text: rep.ClaimText, Score: rep.Score, Rationale: rep.Rationale,
		ProseRationale: rep.ProseRationale, SourceDocument: rep.SourceDocument, Liveness: string(rep.Liveness),
	}
	for _, s := range rep.Signals {
		out.Signals = append(out.Signals, ProvenanceSignal(s))
	}
	return out, nil
}

// ClaimHistory implements [Memory.ClaimHistory].
func (m *memory) ClaimHistory(ctx context.Context, claimID string) ([]ClaimVersion, error) {
	versions, err := beliefops.History(ctx, m.conn, claimID)
	if err != nil {
		return nil, fmt.Errorf("mnemos: ClaimHistory: %w", err)
	}
	out := make([]ClaimVersion, 0, len(versions))
	for _, v := range versions {
		out = append(out, ClaimVersion{
			ClaimID: v.ClaimID, Version: v.Version, Text: v.Text, Confidence: v.Confidence,
			Status: string(v.Status), WrittenAt: v.WrittenAt, WrittenBy: v.WrittenBy,
		})
	}
	return out, nil
}
