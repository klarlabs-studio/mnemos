package grpc

import (
	"context"

	mnemosv1 "go.klarlabs.de/mnemos/proto/gen/mnemos/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Connected-brain RPCs (gRPC parity with the HTTP /v1/who-knows etc.). Each
// delegates to the library Memory facade; when it's absent they return
// codes.Unavailable rather than a silent empty result.

func (s *Server) brainUnavailable() error {
	return status.Error(codes.Unavailable, "cognitive layer unavailable (no memory facade)")
}

// WhoKnows ranks the workers whose memory best matches a query.
func (s *Server) WhoKnows(ctx context.Context, req *mnemosv1.WhoKnowsRequest) (*mnemosv1.WhoKnowsResponse, error) {
	if s.memFor(ctx) == nil {
		return nil, s.brainUnavailable()
	}
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 10
	}
	experts, err := s.memFor(ctx).WhoKnows(ctx, req.GetQuery(), limit)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	out := &mnemosv1.WhoKnowsResponse{Query: req.GetQuery()}
	for _, e := range experts {
		out.Experts = append(out.Experts, &mnemosv1.Expert{
			Worker: e.Worker, Affinity: e.Affinity, Reliability: e.Reliability, ClaimCount: int32(e.ClaimCount),
		})
	}
	return out, nil
}

// KnowledgeGaps lists the store's highest-value open questions.
func (s *Server) KnowledgeGaps(ctx context.Context, req *mnemosv1.KnowledgeGapsRequest) (*mnemosv1.KnowledgeGapsResponse, error) {
	if s.memFor(ctx) == nil {
		return nil, s.brainUnavailable()
	}
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 20
	}
	gaps, err := s.memFor(ctx).KnowledgeGaps(ctx, limit)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	out := &mnemosv1.KnowledgeGapsResponse{}
	for _, g := range gaps {
		out.Gaps = append(out.Gaps, &mnemosv1.Gap{ClaimId: g.ClaimID, Text: g.Text, Kind: g.Kind, Score: g.Score})
	}
	return out, nil
}

// Calibration reports how well stated confidence tracks reality across the store.
func (s *Server) Calibration(ctx context.Context, _ *mnemosv1.CalibrationRequest) (*mnemosv1.CalibrationResponse, error) {
	if s.memFor(ctx) == nil {
		return nil, s.brainUnavailable()
	}
	cal, err := s.memFor(ctx).Calibration(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	out := &mnemosv1.CalibrationResponse{Samples: int32(cal.Samples), Ece: cal.ECE, Brier: cal.Brier}
	for _, b := range cal.Buckets {
		out.Buckets = append(out.Buckets, &mnemosv1.CalibrationBucket{
			Lower: b.Lower, Upper: b.Upper, Count: int32(b.Count), MeanConfidence: b.MeanConfidence, Accuracy: b.Accuracy,
		})
	}
	for _, sc := range cal.Sources {
		out.Sources = append(out.Sources, &mnemosv1.CalibrationSource{
			Source: sc.Source, Samples: int32(sc.Samples), MeanConfidence: sc.MeanConfidence, Accuracy: sc.Accuracy, Gap: sc.Gap, Brier: sc.Brier,
		})
	}
	return out, nil
}

// Hypercorrections lists established beliefs a newer claim now contradicts.
func (s *Server) Hypercorrections(ctx context.Context, _ *mnemosv1.HypercorrectionsRequest) (*mnemosv1.HypercorrectionsResponse, error) {
	if s.memFor(ctx) == nil {
		return nil, s.brainUnavailable()
	}
	hcs, err := s.memFor(ctx).Hypercorrections(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	out := &mnemosv1.HypercorrectionsResponse{}
	for _, h := range hcs {
		hc := &mnemosv1.Hypercorrection{
			ContradictedClaimId:  h.ContradictedClaimID,
			ContradictedText:     h.ContradictedText,
			ContradictedTrust:    h.ContradictedTrust,
			ContradictedPromoted: h.ContradictedPromoted,
			ChallengingClaimId:   h.ChallengingClaimID,
			ChallengingText:      h.ChallengingText,
		}
		if !h.DetectedAt.IsZero() {
			hc.DetectedAt = timestamppb.New(h.DetectedAt)
		}
		out.Hypercorrections = append(out.Hypercorrections, hc)
	}
	return out, nil
}

// Recombinations lists topically-similar-but-unlinked claim pairs.
func (s *Server) Recombinations(ctx context.Context, req *mnemosv1.RecombinationsRequest) (*mnemosv1.RecombinationsResponse, error) {
	if s.memFor(ctx) == nil {
		return nil, s.brainUnavailable()
	}
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 20
	}
	recs, err := s.memFor(ctx).Recombinations(ctx, limit)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	out := &mnemosv1.RecombinationsResponse{}
	for _, r := range recs {
		out.Recombinations = append(out.Recombinations, &mnemosv1.Recombination{
			ClaimA: r.ClaimA, TextA: r.TextA, ClaimB: r.ClaimB, TextB: r.TextB, Similarity: r.Similarity,
		})
	}
	return out, nil
}

// AnalogousClaims returns the claims most structurally analogous to a given one.
func (s *Server) AnalogousClaims(ctx context.Context, req *mnemosv1.AnalogousClaimsRequest) (*mnemosv1.AnalogousClaimsResponse, error) {
	if s.memFor(ctx) == nil {
		return nil, s.brainUnavailable()
	}
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 10
	}
	analogies, err := s.memFor(ctx).AnalogousClaims(ctx, req.GetClaimId(), limit)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	out := &mnemosv1.AnalogousClaimsResponse{ClaimId: req.GetClaimId()}
	for _, a := range analogies {
		out.Analogous = append(out.Analogous, &mnemosv1.Analogy{ClaimId: a.ClaimID, Text: a.Text, Similarity: a.Similarity})
	}
	return out, nil
}
