package grpc

import (
	"context"
	"errors"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/retraction"
	mnemosv1 "go.klarlabs.de/mnemos/proto/gen/mnemos/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// RetractEpisode withdraws an episode as evidence (ADR 0037). It runs the same
// retraction.Run as the CLI, MCP, REST and the Go library, through the
// request's tenant writer, and confines a run-scoped token to its runs: an
// episode outside them is NotFound, exactly like one that does not exist.
func (s *Server) RetractEpisode(ctx context.Context, req *mnemosv1.RetractEpisodeRequest) (*mnemosv1.RetractEpisodeResponse, error) {
	if err := s.requireScope(ctx, domain.ScopeClaimsWrite); err != nil {
		return nil, err
	}
	w := s.writerFor(ctx)
	if w == nil {
		return nil, status.Error(codes.Unavailable, "writer unavailable")
	}
	allowed := allowedRunsFromContext(ctx)
	rep, err := retraction.Run(ctx, w.Conn().Events, w.RetractEvent, retraction.Request{
		EventID: req.GetEpisodeId(), Reason: req.GetReason(), Actor: actorFromContext(ctx),
		RunAllowed: func(runID string) bool { return runAllowed(allowed, runID) },
	})
	switch {
	case errors.Is(err, retraction.ErrEventIDRequired):
		return nil, status.Error(codes.InvalidArgument, "episode_id is required")
	case errors.Is(err, retraction.ErrEventNotFound):
		return nil, status.Error(codes.NotFound, "episode not found")
	case err != nil:
		return nil, status.Errorf(codes.Internal, "retract episode: %v", err)
	}
	return retractionToProto(rep), nil
}

func retractionToProto(rep domain.EventRetraction) *mnemosv1.RetractEpisodeResponse {
	im := rep.Impact
	out := &mnemosv1.RetractEpisodeResponse{
		EpisodeId:        rep.EventID,
		RetractedAt:      timestamppb.New(rep.RetractedAt),
		RetractedBy:      rep.RetractedBy,
		Reason:           rep.Reason,
		AlreadyRetracted: rep.AlreadyRetracted,
		Impact: &mnemosv1.RetractionImpact{
			RetractedBeliefs:  im.RetractedBeliefs,
			AlreadyDeprecated: im.AlreadyDeprecated,
			Associations:      im.Associations,
			OutcomeEdges:      im.OutcomeEdges,
			Uncertainty:       im.Uncertainty,
		},
	}
	for _, r := range im.Rescored {
		out.Impact.RescoredBeliefs = append(out.Impact.RescoredBeliefs, &mnemosv1.RescoredBelief{
			Id: r.ID, TrustBefore: r.TrustBefore, TrustAfter: r.TrustAfter, RemainingEvidence: int32(r.RemainingEvidence),
		})
	}
	for _, d := range im.Decisions {
		out.Impact.Decisions = append(out.Impact.Decisions, &mnemosv1.RetractionDecisionExposure{DecisionId: d.DecisionID, Beliefs: d.Beliefs})
	}
	for _, h := range im.DependentEpisodes {
		out.Impact.DependentEpisodes = append(out.Impact.DependentEpisodes, &mnemosv1.EpisodeDependencyHop{
			EpisodeId: h.EventID, DependsOn: h.DependsOn, Kind: string(h.Kind), Signal: string(h.Signal), Reference: h.Reference,
		})
	}
	for _, p := range im.BeliefPaths {
		out.Impact.BeliefPaths = append(out.Impact.BeliefPaths, &mnemosv1.RetractionBeliefPath{BeliefId: p.BeliefID, Outcome: p.Outcome, Via: p.Via})
	}
	return out
}
