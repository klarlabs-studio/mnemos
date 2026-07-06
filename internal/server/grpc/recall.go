package grpc

import (
	"context"
	"strings"

	mnemos "go.klarlabs.de/mnemos"
	mnemosv1 "go.klarlabs.de/mnemos/proto/gen/mnemos/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func grpcRecallResults(rs []mnemos.Result) []*mnemosv1.RecallResult {
	out := make([]*mnemosv1.RecallResult, 0, len(rs))
	for _, r := range rs {
		out = append(out, &mnemosv1.RecallResult{
			ClaimId: r.ClaimID, Text: r.Text, Type: r.Type, Confidence: r.Confidence,
			TrustScore: r.TrustScore, HopDistance: int32(r.HopDistance), Provenance: r.Provenance,
		})
	}
	return out
}

func grpcSufficiency(s mnemos.Sufficiency) *mnemosv1.RecallSufficiency {
	return &mnemosv1.RecallSufficiency{
		Confidence: s.Confidence, ClaimCount: int32(s.ClaimCount), Sufficient: s.Sufficient, Floor: s.Floor,
	}
}

// Recall runs advanced retrieval; mode selects the epistemic-honesty variant.
func (s *Server) Recall(ctx context.Context, req *mnemosv1.RecallRequest) (*mnemosv1.RecallResponse, error) {
	if s.mem == nil {
		return nil, s.brainUnavailable()
	}
	if strings.TrimSpace(req.GetQuery()) == "" {
		return nil, status.Error(codes.InvalidArgument, "query is required")
	}
	query := mnemos.Query{
		Text: req.GetQuery(), RunID: req.GetRunId(), Limit: int(req.GetLimit()), Hops: int(req.GetHops()),
	}
	mode := req.GetMode()
	if mode == "" {
		mode = "sufficiency"
	}
	resp := &mnemosv1.RecallResponse{Mode: mode}

	switch mode {
	case "sufficiency":
		res, suf, err := s.mem.RecallWithSufficiency(ctx, query)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		resp.Results, resp.Sufficiency = grpcRecallResults(res), grpcSufficiency(suf)
	case "effort":
		res, suf, eff, err := s.mem.RecallWithEffort(ctx, query, req.GetStakes())
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		resp.Results, resp.Sufficiency = grpcRecallResults(res), grpcSufficiency(suf)
		resp.Effort = &mnemosv1.RecallEffort{Budget: eff.Budget, Passes: int32(eff.Passes), Hops: int32(eff.Hops), Breadth: int32(eff.Breadth)}
	case "context":
		res, err := s.mem.RecallWithContext(ctx, query, req.GetContext())
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		resp.Results = grpcRecallResults(res)
	case "conflicts":
		res, conflicts, err := s.mem.RecallWithConflicts(ctx, query)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		resp.Results = grpcRecallResults(res)
		for _, c := range conflicts {
			resp.Conflicts = append(resp.Conflicts, &mnemosv1.RecallConflict{
				ClaimId: c.ClaimID, ClaimText: c.ClaimText, ContradictingId: c.ContradictingID,
				ContradictingText: c.ContradictingText, ContradictingTrust: c.ContradictingTrust,
			})
		}
	case "iterative":
		maxRounds := int(req.GetMaxRounds())
		if maxRounds <= 0 {
			maxRounds = 3
		}
		res, rounds, err := s.mem.RecallIterative(ctx, query, maxRounds)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		resp.Results, resp.Rounds = grpcRecallResults(res), int32(rounds)
	default:
		return nil, status.Error(codes.InvalidArgument, "mode must be sufficiency, effort, context, conflicts, or iterative")
	}
	return resp, nil
}
