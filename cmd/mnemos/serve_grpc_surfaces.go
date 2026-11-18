package main

import (
	"context"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/query"
	mnemosgrpc "go.klarlabs.de/mnemos/internal/server/grpc"
	"go.klarlabs.de/mnemos/internal/store"
	mnemosv1 "go.klarlabs.de/mnemos/proto/gen/mnemos/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// withDaemonSurfaces attaches the daemon-owned implementations the gRPC server
// cannot import: the text-processing pipeline and belief search. Both are the
// functions REST runs (runProcessRequest, runSearch), so gRPC ProcessText and
// Search are adapters over the same code rather than restatements of it.
func withDaemonSurfaces(srv *mnemosgrpc.Server) *mnemosgrpc.Server {
	return srv.WithTextProcessor(grpcProcessText).WithSearcher(grpcSearch)
}

// grpcProcessText adapts gRPC ProcessText to runProcessRequest. The tenant the
// gRPC interceptor resolved is re-stamped in this package's context key, which
// is what openWriter (inside mcpRunProcessText) scopes the write by.
func grpcProcessText(ctx context.Context, tenant, actor string, req *mnemosv1.ProcessTextRequest) (*mnemosv1.ProcessTextResponse, error) {
	if tenant != "" {
		ctx = withTenant(ctx, tenant)
	}
	in := processRequestDTO{Text: req.GetText()}
	if req.UseLlm != nil {
		v := req.GetUseLlm()
		in.UseLLM = &v
	}
	if req.UseEmbeddings != nil {
		v := req.GetUseEmbeddings()
		in.UseEmbeddings = &v
	}
	out, err := runProcessRequest(ctx, actor, in)
	if err != nil {
		return nil, err
	}
	return &mnemosv1.ProcessTextResponse{
		RunId: out.RunID, Episodes: int32(out.Events), Beliefs: int32(out.Claims),
		Associations: int32(out.Relationships), Embeddings: int32(out.Embeddings),
		UsedLlm: out.UsedLLM, UsedEmbeddings: out.UsedEmbeddings,
	}, nil
}

// grpcSearch adapts gRPC Search to runSearch over the request's tenant conn.
func grpcSearch(ctx context.Context, conn *store.Conn, req *mnemosv1.SearchRequest) (*mnemosv1.SearchResponse, error) {
	in := searchRequest{
		Query:      req.GetQuery(),
		RunID:      req.GetRunId(),
		TopK:       int(req.GetTopK()),
		MinTrust:   req.GetMinTrust(),
		Consumer:   req.GetConsumer(),
		Visibility: req.GetVisibility(),
	}
	if in.TopK < 0 {
		return nil, fmt.Errorf("%w: top_k must be non-negative", query.ErrInvalidRecall)
	}
	if in.MinTrust < 0 || in.MinTrust > 1 {
		return nil, fmt.Errorf("%w: min_trust must be in [0,1]", query.ErrInvalidRecall)
	}
	if req.GetAsOf() != nil {
		in.AsOf = req.GetAsOf().AsTime().UTC().Format(time.RFC3339Nano)
	}
	if req.GetRecordedAsOf() != nil {
		in.RecordedAsOf = req.GetRecordedAsOf().AsTime().UTC().Format(time.RFC3339Nano)
	}
	if sc := req.GetScope(); sc != nil {
		in.Scope = &searchScope{Service: sc.GetService(), Env: sc.GetEnv(), Team: sc.GetTeam()}
	}
	if req.GetTypeFilter() != "" || req.GetStatusFilter() != "" {
		in.Filters = &searchFilters{ClaimType: req.GetTypeFilter(), Status: req.GetStatusFilter()}
	}
	res, err := runSearch(ctx, conn, in)
	if err != nil {
		return nil, err
	}
	out := &mnemosv1.SearchResponse{
		Query: in.Query, Total: int32(res.total), TopK: int32(res.req.TopK),
		AutoResolved: res.answer.AutoResolved, DissonanceExplanation: res.answer.ContradictionExplanation,
	}
	for _, c := range res.claims {
		out.Beliefs = append(out.Beliefs, &mnemosv1.SearchBelief{
			Id: c.ID, Text: c.Text, Type: string(c.Type), Status: string(c.Status),
			Confidence: c.Confidence, TrustScore: c.TrustScore,
		})
	}
	for _, rel := range res.answer.Contradictions {
		out.Dissonances = append(out.Dissonances, &mnemosv1.Association{
			Id: rel.ID, Type: string(rel.Type), FromBeliefId: rel.FromClaimID, ToBeliefId: rel.ToClaimID,
			CreatedAt: timestamppb.New(rel.CreatedAt),
		})
	}
	for _, v := range res.answer.Verdicts {
		out.Verdicts = append(out.Verdicts, &mnemosv1.SearchVerdict{
			WinnerBeliefId: v.WinnerClaimID, LoserBeliefId: v.LoserClaimID, Confidence: v.Confidence,
			Rationale: v.Rationale, Action: string(v.Action), EscalationReason: v.EscalationReason,
		})
	}
	return out, nil
}
