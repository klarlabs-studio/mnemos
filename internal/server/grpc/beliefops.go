package grpc

import (
	"context"
	"errors"
	"strings"
	"time"

	mnemos "go.klarlabs.de/mnemos"
	"go.klarlabs.de/mnemos/internal/beliefops"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
	mnemosv1 "go.klarlabs.de/mnemos/proto/gen/mnemos/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Belief writes, governance and per-belief reads. Each RPC is a thin adapter
// over the implementation REST, MCP and the Go library share: internal/
// beliefops through the request's tenant conn and writer, or the Memory
// facade (RememberFact, ScanBeliefs).

// beliefOpStatus maps a beliefops error onto a gRPC status.
func beliefOpStatus(label string, err error) error {
	switch {
	case errors.Is(err, beliefops.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, beliefops.ErrInvalid):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, beliefops.ErrUnsupported):
		return status.Error(codes.Unimplemented, err.Error())
	default:
		return status.Errorf(codes.Internal, "%s: %v", label, err)
	}
}

// govern resolves the request's conn and writer for a governance write after
// checking scope.
func (s *Server) govern(ctx context.Context, scope string) (*govwrite.Writer, error) {
	if err := s.requireScope(ctx, scope); err != nil {
		return nil, err
	}
	w := s.writerFor(ctx)
	if w == nil {
		return nil, status.Error(codes.Unavailable, "writer unavailable")
	}
	return w, nil
}

// refuseRunScoped denies a run-scoped token an operation that spans or writes
// outside every run.
func refuseRunScoped(ctx context.Context, what string) error {
	if len(allowedRunsFromContext(ctx)) > 0 {
		return status.Errorf(codes.PermissionDenied, "token is run-scoped: %s spans every run", what)
	}
	return nil
}

// RememberFact stores one fact with its evidence through
// Memory.RememberClaimWithEvidence.
func (s *Server) RememberFact(ctx context.Context, req *mnemosv1.RememberFactRequest) (*mnemosv1.RememberFactResponse, error) {
	if err := s.requireScope(ctx, domain.ScopeClaimsWrite); err != nil {
		return nil, err
	}
	if err := refuseRunScoped(ctx, "a fact's evidence"); err != nil {
		return nil, err
	}
	mem := s.memFor(ctx)
	if mem == nil {
		return nil, s.brainUnavailable()
	}
	hasEvidence := false
	for _, e := range req.GetEvidence() {
		if strings.TrimSpace(e) != "" {
			hasEvidence = true
		}
	}
	if strings.TrimSpace(req.GetText()) == "" || !hasEvidence {
		return nil, status.Error(codes.InvalidArgument, "text and at least one non-blank evidence reference are required")
	}
	var validFrom time.Time
	if req.GetValidFrom() != nil {
		validFrom = req.GetValidFrom().AsTime()
	}
	got, err := mem.RememberClaimWithEvidenceDetailed(ctx, req.GetText(), req.GetEvidence(), validFrom)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "remember fact: %v", err)
	}
	return &mnemosv1.RememberFactResponse{BeliefId: got.ID, Known: got.Known}, nil
}

// ScanBeliefs returns beliefs valid within a window through Memory.Scan.
func (s *Server) ScanBeliefs(ctx context.Context, req *mnemosv1.ScanBeliefsRequest) (*mnemosv1.ScanBeliefsResponse, error) {
	if err := refuseRunScoped(ctx, "a scan"); err != nil {
		return nil, err
	}
	mem := s.memFor(ctx)
	if mem == nil {
		return nil, s.brainUnavailable()
	}
	q := mnemos.ScanQuery{Limit: int(req.GetLimit())}
	if req.GetValidFrom() != nil {
		q.ValidFrom = req.GetValidFrom().AsTime()
	}
	if req.GetValidUntil() != nil {
		q.ValidUntil = req.GetValidUntil().AsTime()
	}
	claims, err := mem.Scan(ctx, q)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "scan: %v", err)
	}
	out := &mnemosv1.ScanBeliefsResponse{Beliefs: make([]*mnemosv1.BeliefDetail, 0, len(claims))}
	for _, c := range claims {
		out.Beliefs = append(out.Beliefs, beliefDetailToProto(c))
	}
	return out, nil
}

// DeprecateBelief marks a belief deprecated (beliefops.Deprecate).
func (s *Server) DeprecateBelief(ctx context.Context, req *mnemosv1.DeprecateBeliefRequest) (*mnemosv1.BeliefStatusChange, error) {
	w, err := s.govern(ctx, domain.ScopeClaimsWrite)
	if err != nil {
		return nil, err
	}
	ch, err := beliefops.Deprecate(ctx, w.Conn(), w, beliefops.Edit{
		ClaimID: req.GetBeliefId(), Reason: req.GetReason(), Actor: actorFromContext(ctx),
	}, beliefops.ReasonDeprecate)
	if err != nil {
		return nil, beliefOpStatus("deprecate belief", err)
	}
	return &mnemosv1.BeliefStatusChange{BeliefId: ch.ClaimID, OldStatus: ch.OldStatus, NewStatus: ch.NewStatus}, nil
}

// UpdateBelief rewrites a belief's text (beliefops.Update).
func (s *Server) UpdateBelief(ctx context.Context, req *mnemosv1.UpdateBeliefRequest) (*mnemosv1.BeliefRevision, error) {
	w, err := s.govern(ctx, domain.ScopeClaimsWrite)
	if err != nil {
		return nil, err
	}
	rev, err := beliefops.Update(ctx, w.Conn(), w, beliefops.UpdateRequest{
		Edit: beliefops.Edit{ClaimID: req.GetBeliefId(), Reason: req.GetReason(), Actor: actorFromContext(ctx)},
		Text: req.GetText(), Confidence: req.GetConfidence(),
	})
	if err != nil {
		return nil, beliefOpStatus("update belief", err)
	}
	return &mnemosv1.BeliefRevision{BeliefId: rev.ClaimID, OldText: rev.OldText, NewText: rev.NewText, Confidence: rev.Confidence}, nil
}

// ResolveDissonance settles two contradicting beliefs (beliefops.Resolve).
func (s *Server) ResolveDissonance(ctx context.Context, req *mnemosv1.ResolveDissonanceRequest) (*mnemosv1.ResolveDissonanceResponse, error) {
	w, err := s.govern(ctx, domain.ScopeClaimsWrite)
	if err != nil {
		return nil, err
	}
	res, err := beliefops.Resolve(ctx, w.Conn(), w, beliefops.ResolveRequest{
		WinnerID: req.GetWinnerId(), LoserID: req.GetLoserId(), Reason: req.GetReason(), Actor: actorFromContext(ctx),
	})
	if err != nil {
		return nil, beliefOpStatus("resolve dissonance", err)
	}
	return &mnemosv1.ResolveDissonanceResponse{WinnerId: res.WinnerID, LoserId: res.LoserID}, nil
}

// EscalateBelief returns an escalation verdict (beliefops.Escalate).
func (s *Server) EscalateBelief(ctx context.Context, req *mnemosv1.EscalateBeliefRequest) (*mnemosv1.BeliefEscalation, error) {
	if err := s.requireScope(ctx, domain.ScopeClaimsWrite); err != nil {
		return nil, err
	}
	esc, err := beliefops.Escalate(ctx, s.connFor(ctx), req.GetBeliefId(), req.GetReason())
	if err != nil {
		return nil, beliefOpStatus("escalate belief", err)
	}
	return &mnemosv1.BeliefEscalation{
		BeliefId: esc.ClaimID, Action: esc.Action, EscalationReason: esc.EscalationReason, Rationale: esc.Rationale,
	}, nil
}

// VerifyBelief re-verifies a belief (beliefops.Verify). It takes
// promote:global, the scope of memory_promote.
func (s *Server) VerifyBelief(ctx context.Context, req *mnemosv1.VerifyBeliefRequest) (*mnemosv1.VerifyBeliefResponse, error) {
	w, err := s.govern(ctx, domain.ScopePromoteGlobal)
	if err != nil {
		return nil, err
	}
	v, err := beliefops.Verify(ctx, w.Conn(), w, req.GetBeliefId(), time.Now())
	if err != nil {
		return nil, beliefOpStatus("verify belief", err)
	}
	return &mnemosv1.VerifyBeliefResponse{BeliefId: v.ClaimID, VerifiedAt: timestamppb.New(v.VerifiedAt)}, nil
}

// RecordBeliefFeedback applies one helpful/unhelpful vote
// (beliefops.RecordFeedback).
func (s *Server) RecordBeliefFeedback(ctx context.Context, req *mnemosv1.RecordBeliefFeedbackRequest) (*mnemosv1.BeliefFeedback, error) {
	w, err := s.govern(ctx, domain.ScopeClaimsWrite)
	if err != nil {
		return nil, err
	}
	res, err := beliefops.RecordFeedback(ctx, w.Conn(), w, beliefops.FeedbackRequest{
		ClaimID: req.GetBeliefId(), Helpful: req.GetHelpful(), Note: req.GetNote(), Actor: actorFromContext(ctx),
	}, beliefops.FeedbackPolicyFromEnv(), time.Now())
	if err != nil {
		return nil, beliefOpStatus("record feedback", err)
	}
	return &mnemosv1.BeliefFeedback{
		BeliefId: res.ClaimID, Helpful: res.Helpful, NewStatus: res.NewStatus, NewConfidence: res.NewConfidence,
		NegativeFeedbackStreak: int32(res.NegativeFeedbackStreak), HelpfulCount: int32(res.HelpfulCount), //nolint:gosec // counters are small
		AutoContested: res.AutoContested,
	}, nil
}

// BeliefProvenance explains a belief's trust (beliefops.Provenance).
func (s *Server) BeliefProvenance(ctx context.Context, req *mnemosv1.BeliefProvenanceRequest) (*mnemosv1.BeliefProvenance, error) {
	rep, err := beliefops.Provenance(ctx, s.connFor(ctx), req.GetBeliefId())
	if err != nil {
		return nil, beliefOpStatus("provenance", err)
	}
	return provenanceToProto(rep), nil
}

// provenanceToProto is the one wire form of a belief's credibility breakdown,
// shared by BeliefProvenance and WhyWereWeWrong's root belief.
func provenanceToProto(rep domain.ProvenanceReport) *mnemosv1.BeliefProvenance {
	out := &mnemosv1.BeliefProvenance{
		BeliefId: rep.ClaimID, Text: rep.ClaimText, Score: rep.Score, Rationale: rep.Rationale,
		ProseRationale: rep.ProseRationale, SourceDocument: rep.SourceDocument, Liveness: string(rep.Liveness),
	}
	for _, sig := range rep.Signals {
		out.Signals = append(out.Signals, &mnemosv1.ProvenanceSignal{
			Name: sig.Name, Value: sig.Value, Weight: sig.Weight, Contribution: sig.Contribution, Detail: sig.Detail,
		})
	}
	return out
}

// BeliefHistory returns a belief's version chain (beliefops.History).
func (s *Server) BeliefHistory(ctx context.Context, req *mnemosv1.BeliefHistoryRequest) (*mnemosv1.BeliefHistoryResponse, error) {
	versions, err := beliefops.History(ctx, s.connFor(ctx), req.GetBeliefId())
	if err != nil {
		return nil, beliefOpStatus("history", err)
	}
	out := &mnemosv1.BeliefHistoryResponse{BeliefId: req.GetBeliefId(), Versions: make([]*mnemosv1.BeliefVersion, 0, len(versions))}
	for _, v := range versions {
		out.Versions = append(out.Versions, &mnemosv1.BeliefVersion{
			BeliefId: v.ClaimID, Version: int32(v.Version), Text: v.Text, Confidence: v.Confidence, //nolint:gosec // version numbers are small
			Status: string(v.Status), WrittenAt: timestamppb.New(v.WrittenAt), WrittenBy: v.WrittenBy,
		})
	}
	return out, nil
}
