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

// UnretractEpisode clears an episode's retraction (ADR 0037 §8). It runs the
// same retraction.Unretract as the CLI, MCP, REST and the Go library, through
// the request's tenant writer, and confines a run-scoped token to its runs. An
// episode that was never retracted is FailedPrecondition.
func (s *Server) UnretractEpisode(ctx context.Context, req *mnemosv1.UnretractEpisodeRequest) (*mnemosv1.UnretractEpisodeResponse, error) {
	if err := s.requireScope(ctx, domain.ScopeClaimsWrite); err != nil {
		return nil, err
	}
	w := s.writerFor(ctx)
	if w == nil {
		return nil, status.Error(codes.Unavailable, "writer unavailable")
	}
	allowed := allowedRunsFromContext(ctx)
	conn := w.Conn()
	rep, err := retraction.Unretract(ctx, conn.Events, conn.Journal, w.UnretractEvent, retraction.Request{
		EventID: req.GetEpisodeId(), Reason: req.GetReason(), Actor: actorFromContext(ctx),
		RunAllowed: func(runID string) bool { return runAllowed(allowed, runID) },
	})
	switch {
	case errors.Is(err, retraction.ErrEventIDRequired):
		return nil, status.Error(codes.InvalidArgument, "episode_id is required")
	case errors.Is(err, retraction.ErrEventNotFound):
		return nil, status.Error(codes.NotFound, "episode not found")
	case errors.Is(err, retraction.ErrNotRetracted):
		return nil, status.Error(codes.FailedPrecondition, "episode was never retracted")
	case err != nil:
		return nil, status.Errorf(codes.Internal, "un-retract episode: %v", err)
	}
	return unretractionToProto(rep), nil
}

func unretractionToProto(rep domain.EventUnretraction) *mnemosv1.UnretractEpisodeResponse {
	im := rep.Impact
	out := &mnemosv1.UnretractEpisodeResponse{
		EpisodeId:          rep.EventID,
		UnretractedAt:      timestamppb.New(rep.UnretractedAt),
		UnretractedBy:      rep.UnretractedBy,
		Reason:             rep.Reason,
		Generation:         int32(rep.Generation),
		RetractedAt:        timestamppb.New(rep.RetractedAt),
		RetractedBy:        rep.RetractedBy,
		AlreadyUnretracted: rep.AlreadyUnretracted,
		Impact:             &mnemosv1.UnretractionImpact{Uncertainty: im.Uncertainty},
	}
	for _, r := range im.Restored {
		out.Impact.RestoredBeliefs = append(out.Impact.RestoredBeliefs, &mnemosv1.RestoredBelief{Id: r.ID, Status: string(r.Status)})
	}
	for _, s := range im.Skipped {
		out.Impact.SkippedBeliefs = append(out.Impact.SkippedBeliefs, &mnemosv1.SkippedBelief{Id: s.ID, Reason: s.Reason})
	}
	for _, r := range im.Relinked {
		out.Impact.RelinkedBeliefs = append(out.Impact.RelinkedBeliefs, &mnemosv1.RelinkedBelief{
			Id: r.ID, Status: string(r.Status), TrustBefore: r.TrustBefore, TrustAfter: r.TrustAfter,
		})
	}
	return out
}
