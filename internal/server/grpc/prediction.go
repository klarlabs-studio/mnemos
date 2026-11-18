package grpc

import (
	"context"
	"errors"
	"strings"
	"time"

	mnemos "go.klarlabs.de/mnemos"
	"go.klarlabs.de/mnemos/internal/domain"
	mnemosv1 "go.klarlabs.de/mnemos/proto/gen/mnemos/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Prediction and brain-health RPCs. Each delegates to the library Memory
// facade, the one implementation the Go API, REST and MCP use, so the
// transports cannot disagree about what a prediction error or a verdict is.

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
	out := &mnemosv1.PredictiveErrorResponse{Total: pe.Total, Hotspot: pe.Hotspot, LevelsMeasured: int32(pe.LevelsMeasured)}
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

// SetExpectation attaches (or replaces) a belief's forward prediction through
// Memory.Expect and returns the expectation as stored.
func (s *Server) SetExpectation(ctx context.Context, req *mnemosv1.SetExpectationRequest) (*mnemosv1.Expectation, error) {
	if err := s.requireScope(ctx, domain.ScopeClaimsWrite); err != nil {
		return nil, err
	}
	mem := s.memFor(ctx)
	if mem == nil {
		return nil, s.brainUnavailable()
	}
	id := strings.TrimSpace(req.GetBeliefId())
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "belief_id is required")
	}
	var horizon time.Time // unset: no horizon
	if req.GetHorizon() != nil {
		horizon = req.GetHorizon().AsTime()
	}
	if err := mem.Expect(ctx, id, req.GetPredicted(), req.GetTolerance(), horizon); err != nil {
		return nil, expectationError("set expectation", err)
	}
	return s.storedExpectation(ctx, id)
}

// GetExpectation reads a belief's expectation; NotFound when it has none.
func (s *Server) GetExpectation(ctx context.Context, req *mnemosv1.GetExpectationRequest) (*mnemosv1.Expectation, error) {
	id := strings.TrimSpace(req.GetBeliefId())
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "belief_id is required")
	}
	return s.storedExpectation(ctx, id)
}

// RecordObservation records the observed value for a belief's expectation
// through Memory.RecordObservation and returns the expectation as stored.
func (s *Server) RecordObservation(ctx context.Context, req *mnemosv1.RecordObservationRequest) (*mnemosv1.Expectation, error) {
	if err := s.requireScope(ctx, domain.ScopeClaimsWrite); err != nil {
		return nil, err
	}
	mem := s.memFor(ctx)
	if mem == nil {
		return nil, s.brainUnavailable()
	}
	id := strings.TrimSpace(req.GetBeliefId())
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "belief_id is required")
	}
	if err := mem.RecordObservation(ctx, id, req.GetObserved()); err != nil {
		return nil, expectationError("record observation", err)
	}
	return s.storedExpectation(ctx, id)
}

// ReconcileExpectations closes every observed prediction into a
// validates/refutes verdict through Memory.ReconcileExpectations.
func (s *Server) ReconcileExpectations(ctx context.Context, _ *mnemosv1.ReconcileExpectationsRequest) (*mnemosv1.ReconcileExpectationsResponse, error) {
	if err := s.requireScope(ctx, domain.ScopeClaimsWrite); err != nil {
		return nil, err
	}
	mem := s.memFor(ctx)
	if mem == nil {
		return nil, s.brainUnavailable()
	}
	recs, err := mem.ReconcileExpectations(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "reconcile expectations: %v", err)
	}
	out := &mnemosv1.ReconcileExpectationsResponse{Reconciliations: make([]*mnemosv1.Reconciliation, 0, len(recs))}
	for _, r := range recs {
		out.Reconciliations = append(out.Reconciliations, &mnemosv1.Reconciliation{
			BeliefId: r.ClaimID, Predicted: r.Predicted, Observed: r.Observed, Surprise: r.Surprise, Confirmed: r.Confirmed,
		})
	}
	return out, nil
}

// storedExpectation reads the expectation back from the request's store, so
// the response is what persisted rather than what the request carried.
func (s *Server) storedExpectation(ctx context.Context, claimID string) (*mnemosv1.Expectation, error) {
	repo := s.connFor(ctx).Expectations
	if repo == nil {
		return nil, status.Error(codes.Unimplemented, "expectations are not supported by this store")
	}
	exp, ok, err := repo.Get(ctx, claimID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get expectation: %v", err)
	}
	if !ok {
		return nil, status.Error(codes.NotFound, "no expectation for belief")
	}
	return expectationToProto(exp), nil
}

func expectationError(op string, err error) error {
	switch {
	case errors.Is(err, mnemos.ErrExpectationsUnsupported):
		return status.Error(codes.Unimplemented, "expectations are not supported by this store")
	case errors.Is(err, mnemos.ErrExpectationNotFound):
		return status.Error(codes.NotFound, "no expectation for belief")
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
