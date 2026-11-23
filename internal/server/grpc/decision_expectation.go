package grpc

import (
	"context"
	"errors"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mnemos "go.klarlabs.de/mnemos"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
	mnemosv1 "go.klarlabs.de/mnemos/proto/gen/mnemos/v1"
)

// Decision-tied expectations (ADR 0049) over gRPC, through govwrite like
// REST and MCP. A decision expectation's surprise is attributed to every
// belief the decision rested on, so a run-scoped token may set or observe one
// only when every rested-on belief lies in its runs; any other answers
// NotFound, as a belief outside the runs does on SetExpectation.

// requireBeliefsInRuns applies requireBeliefInRuns to each rested-on belief.
// A token with no run restriction passes without a lookup.
func (s *Server) requireBeliefsInRuns(ctx context.Context, beliefs []string) error {
	if len(allowedRunsFromContext(ctx)) == 0 {
		return nil
	}
	for _, b := range beliefs {
		if err := s.requireBeliefInRuns(ctx, strings.TrimSpace(b)); err != nil {
			return err
		}
	}
	return nil
}

// checkDecisionExpectation validates d's optional expectation before d is
// written and returns the request to store after it (nil when none).
func (s *Server) checkDecisionExpectation(ctx context.Context, w *govwrite.Writer, d domain.Decision, in *mnemosv1.DecisionExpectation) (*govwrite.DecisionExpectationRequest, error) {
	if in == nil {
		return nil, nil
	}
	var horizon time.Time
	if in.GetHorizon() != nil {
		horizon = in.GetHorizon().AsTime()
	}
	req := govwrite.DecisionExpectationRequest{
		DecisionID: d.ID, RestsOn: d.Beliefs, Predicted: in.GetPredicted(), Tolerance: in.GetTolerance(), Horizon: horizon,
	}
	if err := s.requireBeliefsInRuns(ctx, d.Beliefs); err != nil {
		return nil, err
	}
	if _, err := govwrite.CheckDecisionExpectation(ctx, w.Conn(), req, time.Now().UTC()); err != nil {
		return nil, decisionExpectationError("check decision expectation", err)
	}
	return &req, nil
}

// RecordDecisionObservation records the observed outcome of a decision
// recorded with an expectation and returns the expectation as stored.
func (s *Server) RecordDecisionObservation(ctx context.Context, req *mnemosv1.RecordDecisionObservationRequest) (*mnemosv1.Expectation, error) {
	if err := s.requireScope(ctx, domain.ScopeClaimsWrite); err != nil {
		return nil, err
	}
	w := s.writerFor(ctx)
	if w == nil {
		return nil, status.Error(codes.Unavailable, "writer unavailable")
	}
	id := strings.TrimSpace(req.GetDecisionId())
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "decision_id is required")
	}
	if len(allowedRunsFromContext(ctx)) > 0 {
		beliefs, err := s.connFor(ctx).Decisions.ListBeliefs(ctx, id)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "list decision beliefs: %v", err)
		}
		if len(beliefs) == 0 {
			return nil, status.Error(codes.NotFound, "no expectation for decision")
		}
		if err := s.requireBeliefsInRuns(ctx, beliefs); err != nil {
			return nil, err
		}
	}
	exp, err := w.ObserveDecision(ctx, id, req.GetObserved())
	if err != nil {
		return nil, decisionExpectationError("record decision observation", err)
	}
	return expectationToProto(exp), nil
}

func decisionExpectationError(op string, err error) error {
	switch {
	case errors.Is(err, govwrite.ErrExpectationNotFound):
		return status.Error(codes.NotFound, "no expectation for decision")
	case errors.Is(err, govwrite.ErrDecisionIDRequired):
		return status.Error(codes.InvalidArgument, "decision id is required")
	case errors.Is(err, govwrite.ErrExpectationRestsOnNothing):
		return status.Error(codes.InvalidArgument, "a decision expectation must rest on at least one belief")
	}
	return expectationError(op, err)
}

func reconciliationsToProto(recs []govwrite.Reconciliation) []*mnemosv1.Reconciliation {
	out := make([]*mnemosv1.Reconciliation, 0, len(recs))
	for _, r := range recs {
		pr := &mnemosv1.Reconciliation{
			BeliefId: r.ClaimID, Predicted: r.Predicted, Observed: r.Observed, Surprise: r.Surprise, Confirmed: r.Confirmed,
			DecisionId: r.DecisionID, AttributedTo: r.AttributedTo, Rechecked: r.Rechecked,
		}
		for _, rv := range r.Revisions {
			pr.Revisions = append(pr.Revisions, &mnemosv1.SurpriseRevision{ClaimId: rv.ClaimID, Kind: rv.Kind, By: rv.By})
		}
		out = append(out, pr)
	}
	return out
}

func attributedToProto(as []mnemos.AttributedSurprise) []*mnemosv1.AttributedSurprise {
	out := make([]*mnemosv1.AttributedSurprise, 0, len(as))
	for _, a := range as {
		out = append(out, &mnemosv1.AttributedSurprise{
			ClaimId: a.ClaimID, Surprise: a.Surprise, Predictions: int32(a.Predictions), Refuted: int32(a.Refuted),
		})
	}
	return out
}
