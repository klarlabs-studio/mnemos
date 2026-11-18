package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/beliefops"
	"go.klarlabs.de/mnemos/internal/domain"
)

// MCP counterparts for three REST endpoints that had none.
//
// The surfaces had drifted: `mnemos serve` exposes POST on
// /v1/beliefs/<id>/expectation, /observation and /feedback, while the MCP
// server — the surface Claude Code actually speaks — exposed 45 tools and none
// of them.
//
// That gap is not cosmetic. It is why three tables sit empty on a brain with
// 86,505 claims:
//
//	claim_expectations   0   → and therefore consolidate --credit reports credited: 0
//	claim_feedback       0
//	incidents            0
//
// ADR 0014 calls credit assignment "the capstone learning loop": it propagates
// the signed prediction error of each RESOLVED expectation back to the beliefs
// that informed a decision. With no way to record an expectation, nothing ever
// resolves, and the capstone has no input. The same shape as the action/outcome
// gap — a fully-built loop with no reachable entry point.
//
// These mirror the REST handlers deliberately rather than inventing a nicer
// shape: same fields, same validation, same store calls. Two surfaces that
// disagree about semantics are worse than one surface, and the REST behaviour
// is the one already covered by tests.

type mcpRecordExpectationInput struct {
	ClaimID   string  `json:"claimId" jsonschema:"required,description=Belief this prediction attaches to"`
	Predicted float64 `json:"predicted" jsonschema:"required,description=The predicted value"`
	Tolerance float64 `json:"tolerance,omitempty" jsonschema:"description=Acceptable deviation; the prediction validates when |observed-predicted| <= tolerance"`
	Horizon   string  `json:"horizon,omitempty" jsonschema:"description=When the prediction should be judged (RFC3339 / YYYY-MM-DD / 'now')"`
}

type mcpRecordExpectationOutput struct {
	ClaimID   string  `json:"claim_id"`
	Predicted float64 `json:"predicted"`
	Tolerance float64 `json:"tolerance"`
	Horizon   string  `json:"horizon,omitempty"`
}

// mcpRunRecordExpectation attaches a forward prediction to a belief.
//
// Mirrors POST /v1/beliefs/<id>/expectation. Tolerance is normalised to its
// absolute value exactly as memory.Expect does, so a caller passing -5 gets the
// same band as one passing 5 rather than an unsatisfiable one.
func mcpRunRecordExpectation(ctx context.Context, input mcpRecordExpectationInput) (mcpRecordExpectationOutput, error) {
	claimID := strings.TrimSpace(input.ClaimID)
	// Attach the expectation in the brain that HOLDS the belief (#341): a
	// federated recall hands out workspace-tier ids, and the expectation must
	// live beside the belief it predicts or credit assignment never sees it.
	ctx, _, err := mcpScopeToClaimBrain(ctx, claimID)
	if err != nil {
		return mcpRecordExpectationOutput{}, err
	}
	w, err := openWriter(ctx)
	if err != nil {
		return mcpRecordExpectationOutput{}, err
	}
	defer closeWriter(w)
	conn := w.Conn()
	if conn.Expectations == nil {
		return mcpRecordExpectationOutput{}, fmt.Errorf("expectations are not supported by this store")
	}
	found, err := conn.Claims.ListByIDs(ctx, []string{claimID})
	if err != nil {
		return mcpRecordExpectationOutput{}, fmt.Errorf("look up claim: %w", err)
	}
	if len(found) == 0 {
		return mcpRecordExpectationOutput{}, fmt.Errorf("no such belief %s", claimID)
	}

	tolerance := input.Tolerance
	if tolerance < 0 {
		tolerance = -tolerance
	}
	var horizon time.Time
	if s := strings.TrimSpace(input.Horizon); s != "" {
		t, terr := parseTimeArg(s)
		if terr != nil {
			return mcpRecordExpectationOutput{}, fmt.Errorf("horizon: %w", terr)
		}
		horizon = t
	}

	exp := domain.Expectation{
		ClaimID:   claimID,
		Predicted: input.Predicted,
		Tolerance: tolerance,
		Horizon:   horizon,
		CreatedAt: time.Now().UTC(),
	}
	if err := conn.Expectations.Upsert(ctx, exp); err != nil {
		return mcpRecordExpectationOutput{}, fmt.Errorf("upsert expectation: %w", err)
	}
	out := mcpRecordExpectationOutput{ClaimID: claimID, Predicted: exp.Predicted, Tolerance: exp.Tolerance}
	if !horizon.IsZero() {
		out.Horizon = horizon.UTC().Format(time.RFC3339)
	}
	return out, nil
}

type mcpRecordObservationInput struct {
	ClaimID  string  `json:"claimId" jsonschema:"required,description=Belief whose expectation this observation resolves"`
	Observed float64 `json:"observed" jsonschema:"required,description=The value actually observed"`
}

type mcpRecordObservationOutput struct {
	ClaimID   string  `json:"claim_id"`
	Predicted float64 `json:"predicted"`
	Observed  float64 `json:"observed"`
	Validates bool    `json:"validates"`
}

// mcpRunRecordObservation records what actually happened, resolving a belief's
// expectation into the signed prediction error credit assignment consumes.
//
// Mirrors POST /v1/beliefs/<id>/observation, including its 404 for a claim with
// no expectation: an observation with nothing to resolve is a caller error, not
// an empty success, and silently accepting it would lose the datum.
//
// `validates` is reported back because the whole point of an observation is the
// verdict, and making the caller re-derive it from predicted/observed/tolerance
// invites two implementations of one rule.
func mcpRunRecordObservation(ctx context.Context, input mcpRecordObservationInput) (mcpRecordObservationOutput, error) {
	claimID := strings.TrimSpace(input.ClaimID)
	// Resolve the belief's own brain (#341) — the expectation this observation
	// resolves was written there, so looking anywhere else reports "no
	// expectation" for a belief that has one.
	ctx, _, err := mcpScopeToClaimBrain(ctx, claimID)
	if err != nil {
		return mcpRecordObservationOutput{}, err
	}
	w, err := openWriter(ctx)
	if err != nil {
		return mcpRecordObservationOutput{}, err
	}
	defer closeWriter(w)
	conn := w.Conn()
	if conn.Expectations == nil {
		return mcpRecordObservationOutput{}, fmt.Errorf("expectations are not supported by this store")
	}

	exp, ok, err := conn.Expectations.Get(ctx, claimID)
	if err != nil {
		return mcpRecordObservationOutput{}, fmt.Errorf("get expectation: %w", err)
	}
	if !ok {
		return mcpRecordObservationOutput{}, fmt.Errorf("no expectation for claim %s; record one first", claimID)
	}
	exp.Observed = input.Observed
	exp.HasObservation = true
	if err := conn.Expectations.Upsert(ctx, exp); err != nil {
		return mcpRecordObservationOutput{}, fmt.Errorf("upsert observation: %w", err)
	}
	return mcpRecordObservationOutput{
		ClaimID:   claimID,
		Predicted: exp.Predicted,
		Observed:  exp.Observed,
		Validates: expectationValidates(exp),
	}, nil
}

// expectationValidates reports whether an observation fell inside the predicted
// band. A zero tolerance demands an exact match, which is the honest reading of
// "predicted X with no stated slack".
func expectationValidates(e domain.Expectation) bool {
	d := e.Observed - e.Predicted
	if d < 0 {
		d = -d
	}
	return d <= e.Tolerance
}

type mcpRecordFeedbackInput struct {
	ClaimID string `json:"claimId" jsonschema:"required,description=Belief the feedback is about"`
	Helpful bool   `json:"helpful" jsonschema:"required,description=true if the recalled belief was useful; false records an unhelpful vote"`
	Note    string `json:"note,omitempty" jsonschema:"description=Why it was or was not helpful; kept verbatim for the audit trail"`
}

// mcpRecordFeedbackOutput is the vote's effect on the belief. The first
// three fields are the original receipt; the rest are the fields REST's
// POST /v1/beliefs/<id>/feedback has always returned, now that both run
// beliefops.RecordFeedback.
type mcpRecordFeedbackOutput struct {
	ClaimID                string  `json:"claim_id"`
	HelpfulCount           int     `json:"helpful_count"`
	NegativeFeedbackStreak int     `json:"negative_feedback_streak"`
	Helpful                bool    `json:"helpful"`
	NewStatus              string  `json:"new_status"`
	NewConfidence          float64 `json:"new_confidence"`
	AutoContested          bool    `json:"auto_contested"`
}

// mcpRunRecordFeedback records whether a recalled belief was actually useful.
//
// It runs beliefops.RecordFeedback, the one implementation REST, gRPC and the
// library share: a helpful vote resets the negative streak, an unhelpful one
// extends it, decays confidence, and contests the belief once the streak
// reaches MNEMOS_FEEDBACK_CONTEST_THRESHOLD. This tool used to keep the tally
// without the decay or the contest, so the same vote moved a belief over REST
// and not over MCP.
func mcpRunRecordFeedback(ctx context.Context, actor string, input mcpRecordFeedbackInput) (mcpRecordFeedbackOutput, error) {
	claimID := strings.TrimSpace(input.ClaimID)
	// The recall that produced this feedback federates both tiers (#341), so the
	// belief being judged is as likely to be a workspace one as a global one.
	ctx, _, err := mcpScopeToClaimBrain(ctx, claimID)
	if err != nil {
		return mcpRecordFeedbackOutput{}, err
	}
	w, err := openWriter(ctx)
	if err != nil {
		return mcpRecordFeedbackOutput{}, err
	}
	defer closeWriter(w)
	res, err := beliefops.RecordFeedback(ctx, w.Conn(), w, beliefops.FeedbackRequest{
		ClaimID: claimID, Helpful: input.Helpful, Note: input.Note, Actor: actor,
	}, beliefops.FeedbackPolicyFromEnv(), time.Now())
	if err != nil {
		return mcpRecordFeedbackOutput{}, mcpBeliefOpError(err)
	}
	return mcpRecordFeedbackOutput{
		ClaimID: res.ClaimID, HelpfulCount: res.HelpfulCount, NegativeFeedbackStreak: res.NegativeFeedbackStreak,
		Helpful: res.Helpful, NewStatus: res.NewStatus, NewConfidence: res.NewConfidence, AutoContested: res.AutoContested,
	}, nil
}
