package grpc

import (
	"context"
	"errors"
	"strings"
	"time"

	mnemos "go.klarlabs.de/mnemos"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/runscope"
	mnemosv1 "go.klarlabs.de/mnemos/proto/gen/mnemos/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Prediction-error and brain-health RPCs delegate to the library Memory facade,
// the one implementation the Go API, REST and MCP use, so the transports cannot
// disagree about what a prediction error or a verdict is.

// PredictiveError reports hierarchical prediction error (ADR 0017).
func (s *Server) PredictiveError(ctx context.Context, _ *mnemosv1.PredictiveErrorRequest) (*mnemosv1.PredictiveErrorResponse, error) {
	mem := s.memFor(ctx)
	if mem == nil {
		return nil, s.brainUnavailable()
	}
	pe, err := mem.PredictiveError(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "predictive error: %v", err)
	}
	out := &mnemosv1.PredictiveErrorResponse{
		Total: pe.Total, Hotspot: pe.Hotspot, LevelsMeasured: int32(pe.LevelsMeasured),
		Attributed: attributedToProto(pe.Attributed),
	}
	for _, l := range pe.Levels {
		out.Levels = append(out.Levels, &mnemosv1.PredictiveErrorLevel{
			Level: l.Level, Error: l.Error, Samples: int32(l.Samples), Basis: l.Basis,
		})
	}
	return out, nil
}

// BrainHealth reports the unified brain-health verdict (ADR 0019). full forces
// the exact scan, as REST ?full=true does.
func (s *Server) BrainHealth(ctx context.Context, req *mnemosv1.BrainHealthRequest) (*mnemosv1.BrainHealthResponse, error) {
	mem := s.memFor(ctx)
	if mem == nil {
		return nil, s.brainUnavailable()
	}
	health := mem.BrainHealth
	if req.GetFull() {
		health = mem.BrainHealthFull
	}
	rep, err := health(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "brain health: %v", err)
	}
	return brainHealthToProto(rep), nil
}

func brainHealthToProto(rep mnemos.BrainHealth) *mnemosv1.BrainHealthResponse {
	out := &mnemosv1.BrainHealthResponse{Status: string(rep.Status), Mode: rep.Mode}
	if !rep.At.IsZero() {
		out.At = timestamppb.New(rep.At)
	}
	for _, v := range rep.Vitals {
		out.Vitals = append(out.Vitals, &mnemosv1.Vital{Name: v.Name, Value: v.Value, Status: string(v.Status), Detail: v.Detail})
	}
	for _, p := range rep.Pathologies {
		out.Pathologies = append(out.Pathologies, &mnemosv1.Pathology{Kind: p.Kind, Count: int32(p.Count), Status: string(p.Status), Detail: p.Detail})
	}
	if len(rep.Estimates) > 0 {
		out.Estimates = make(map[string]*mnemosv1.VitalEstimate, len(rep.Estimates))
		for name, e := range rep.Estimates {
			out.Estimates[name] = &mnemosv1.VitalEstimate{
				SampleSize: int32(e.SampleSize), Population: int32(e.Population), Margin_95: e.Margin95,
			}
		}
	}
	return out
}

// The expectation RPCs write through the request's governed writer: govwrite
// holds the one implementation of setting, observing and reconciling a
// prediction, shared with REST, MCP and the Go library. A run-scoped token may
// touch only beliefs whose evidence lies in its runs (runscope.ClaimAllowed);
// any other belief answers NotFound, exactly like one that does not exist.

// SetExpectation attaches (or replaces) a belief's forward prediction and
// returns it as stored.
func (s *Server) SetExpectation(ctx context.Context, req *mnemosv1.SetExpectationRequest) (*mnemosv1.Expectation, error) {
	if err := s.requireScope(ctx, domain.ScopeClaimsWrite); err != nil {
		return nil, err
	}
	w := s.writerFor(ctx)
	if w == nil {
		return nil, status.Error(codes.Unavailable, "writer unavailable")
	}
	id := strings.TrimSpace(req.GetBeliefId())
	if err := s.requireBeliefInRuns(ctx, id); err != nil {
		return nil, err
	}
	var horizon time.Time // unset: no horizon
	if req.GetHorizon() != nil {
		horizon = req.GetHorizon().AsTime()
	}
	exp, err := w.SetExpectation(ctx, govwrite.ExpectationRequest{
		ClaimID: id, Predicted: req.GetPredicted(), Tolerance: req.GetTolerance(), Horizon: horizon,
	})
	if err != nil {
		return nil, expectationError("set expectation", err)
	}
	return expectationToProto(exp), nil
}

// GetExpectation reads a belief's expectation; NotFound when it has none.
func (s *Server) GetExpectation(ctx context.Context, req *mnemosv1.GetExpectationRequest) (*mnemosv1.Expectation, error) {
	id := strings.TrimSpace(req.GetBeliefId())
	if err := s.requireBeliefInRuns(ctx, id); err != nil {
		return nil, err
	}
	repo := s.connFor(ctx).Expectations
	if repo == nil {
		return nil, status.Error(codes.Unimplemented, "expectations are not supported by this store")
	}
	exp, ok, err := repo.Get(ctx, id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get expectation: %v", err)
	}
	if !ok {
		return nil, status.Error(codes.NotFound, "no expectation for belief")
	}
	return expectationToProto(exp), nil
}

// RecordObservation records the observed value for a belief's expectation and
// returns the expectation as stored.
func (s *Server) RecordObservation(ctx context.Context, req *mnemosv1.RecordObservationRequest) (*mnemosv1.Expectation, error) {
	if err := s.requireScope(ctx, domain.ScopeClaimsWrite); err != nil {
		return nil, err
	}
	w := s.writerFor(ctx)
	if w == nil {
		return nil, status.Error(codes.Unavailable, "writer unavailable")
	}
	id := strings.TrimSpace(req.GetBeliefId())
	if err := s.requireBeliefInRuns(ctx, id); err != nil {
		return nil, err
	}
	exp, err := w.ObserveExpectation(ctx, id, req.GetObserved())
	if err != nil {
		return nil, expectationError("record observation", err)
	}
	return expectationToProto(exp), nil
}

// ReconcileExpectations closes every observed prediction into a
// validates/refutes verdict. It spans every run, so a run-scoped token is
// refused rather than allowed to resolve predictions outside its runs.
func (s *Server) ReconcileExpectations(ctx context.Context, _ *mnemosv1.ReconcileExpectationsRequest) (*mnemosv1.ReconcileExpectationsResponse, error) {
	if err := s.requireScope(ctx, domain.ScopeClaimsWrite); err != nil {
		return nil, err
	}
	if len(allowedRunsFromContext(ctx)) > 0 {
		return nil, status.Error(codes.PermissionDenied, "token is run-scoped: reconciliation spans every run")
	}
	w := s.writerFor(ctx)
	if w == nil {
		return nil, status.Error(codes.Unavailable, "writer unavailable")
	}
	recs, err := w.ReconcileExpectations(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "reconcile expectations: %v", err)
	}
	return &mnemosv1.ReconcileExpectationsResponse{Reconciliations: reconciliationsToProto(recs)}, nil
}

// requireBeliefInRuns refuses a missing belief id, and a belief outside a
// run-scoped token's runs as NotFound.
func (s *Server) requireBeliefInRuns(ctx context.Context, claimID string) error {
	if claimID == "" {
		return status.Error(codes.InvalidArgument, "belief_id is required")
	}
	ok, err := runscope.ClaimAllowed(ctx, s.connFor(ctx), claimID, allowedRunsFromContext(ctx))
	if err != nil {
		return status.Errorf(codes.Internal, "check run scope: %v", err)
	}
	if !ok {
		return status.Error(codes.NotFound, "belief not found")
	}
	return nil
}

func expectationError(op string, err error) error {
	switch {
	case errors.Is(err, govwrite.ErrExpectationsUnsupported):
		return status.Error(codes.Unimplemented, "expectations are not supported by this store")
	case errors.Is(err, govwrite.ErrExpectationNotFound):
		return status.Error(codes.NotFound, "no expectation for belief")
	case errors.Is(err, govwrite.ErrBeliefNotFound):
		return status.Error(codes.NotFound, "belief not found")
	case errors.Is(err, govwrite.ErrBeliefIDRequired):
		return status.Error(codes.InvalidArgument, "belief_id is required")
	}
	return status.Errorf(codes.Internal, "%s: %v", op, err)
}

func expectationToProto(e domain.Expectation) *mnemosv1.Expectation {
	out := &mnemosv1.Expectation{
		BeliefId: e.ClaimID, Predicted: e.Predicted, Tolerance: e.Tolerance,
		Observed: e.Observed, HasObservation: e.HasObservation, Resolved: e.Resolved,
	}
	if !e.Horizon.IsZero() {
		out.Horizon = timestamppb.New(e.Horizon)
	}
	if !e.CreatedAt.IsZero() {
		out.CreatedAt = timestamppb.New(e.CreatedAt)
	}
	return out
}
