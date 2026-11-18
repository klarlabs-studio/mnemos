package grpc

import (
	"context"
	"errors"
	"strings"

	mnemos "go.klarlabs.de/mnemos"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/query"
	"go.klarlabs.de/mnemos/internal/store"
	mnemosv1 "go.klarlabs.de/mnemos/proto/gen/mnemos/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TextProcessor runs raw text through the ingest pipeline for ProcessText.
// tenant is the request's effective tenant ("" outside multi-tenant mode) and
// actor the token subject. `mnemos serve` supplies the same implementation REST
// POST /v1/process and the process_text MCP tool run; the pipeline lives with
// the daemon, not in this package, so it is injected rather than restated.
type TextProcessor func(ctx context.Context, tenant, actor string, req *mnemosv1.ProcessTextRequest) (*mnemosv1.ProcessTextResponse, error)

// Searcher runs a belief search against conn, the request's tenant-scoped
// connection. `mnemos serve` supplies the REST /v1/search implementation, which
// recalls through query.Engine.Recall like every recall surface. An error
// wrapping query.ErrInvalidRecall is the caller's mistake.
type Searcher func(ctx context.Context, conn *store.Conn, req *mnemosv1.SearchRequest) (*mnemosv1.SearchResponse, error)

// WithTextProcessor enables the ProcessText RPC. Without it the RPC answers
// Unimplemented. Returns the server for chaining.
func (s *Server) WithTextProcessor(p TextProcessor) *Server {
	s.processText = p
	return s
}

// WithSearcher enables the Search RPC. Without it the RPC answers
// Unimplemented. Returns the server for chaining.
func (s *Server) WithSearcher(f Searcher) *Server {
	s.search = f
	return s
}

// requireRunScope is REST's requireRunScope for gRPC: a run-scoped token must
// name one of its runs, so an unscoped read cannot reach every run.
func requireRunScope(ctx context.Context, runID string) error {
	allowed := allowedRunsFromContext(ctx)
	if len(allowed) == 0 {
		return nil
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return status.Error(codes.PermissionDenied, "token is run-scoped: run_id is required")
	}
	if !runAllowed(allowed, runID) {
		return status.Errorf(codes.PermissionDenied, "run_id %q not in token whitelist", runID)
	}
	return nil
}

// ProcessText extracts beliefs from free text (ingest.text). Requires
// claims:write, as REST /v1/process does.
func (s *Server) ProcessText(ctx context.Context, req *mnemosv1.ProcessTextRequest) (*mnemosv1.ProcessTextResponse, error) {
	if err := s.requireScope(ctx, domain.ScopeClaimsWrite); err != nil {
		return nil, err
	}
	if s.processText == nil {
		return nil, status.Error(codes.Unimplemented, "text processing is not configured on this server")
	}
	if strings.TrimSpace(req.GetText()) == "" {
		return nil, status.Error(codes.InvalidArgument, "text is required")
	}
	tenant, _ := tenantFromContext(ctx)
	out, err := s.processText(ctx, tenant, actorFromContext(ctx), req)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "process text: %v", err)
	}
	return out, nil
}

// Search is hybrid belief search (recall.search) through the injected Searcher.
func (s *Server) Search(ctx context.Context, req *mnemosv1.SearchRequest) (*mnemosv1.SearchResponse, error) {
	if s.search == nil {
		return nil, status.Error(codes.Unimplemented, "search is not configured on this server")
	}
	if strings.TrimSpace(req.GetQuery()) == "" {
		return nil, status.Error(codes.InvalidArgument, "query is required")
	}
	if err := requireRunScope(ctx, req.GetRunId()); err != nil {
		return nil, err
	}
	out, err := s.search(ctx, s.connFor(ctx), req)
	switch {
	case errors.Is(err, query.ErrInvalidRecall):
		return nil, status.Error(codes.InvalidArgument, err.Error())
	case err != nil:
		return nil, status.Errorf(codes.Internal, "search: %v", err)
	}
	return out, nil
}

// Passages returns the stored episode passages that best match a query
// (recall.passages) through Memory.Passages, as REST /v1/passages does.
// Passages are raw episode text, so a run-scoped token must name one of its runs.
func (s *Server) Passages(ctx context.Context, req *mnemosv1.PassagesRequest) (*mnemosv1.PassagesResponse, error) {
	mem := s.memFor(ctx)
	if mem == nil {
		return nil, s.brainUnavailable()
	}
	text := strings.TrimSpace(req.GetQuery())
	if text == "" {
		return nil, status.Error(codes.InvalidArgument, "query is required")
	}
	runID := strings.TrimSpace(req.GetRunId())
	if err := requireRunScope(ctx, runID); err != nil {
		return nil, err
	}
	if req.GetLimit() < 0 {
		return nil, status.Error(codes.InvalidArgument, "limit must be non-negative")
	}
	passages, err := mem.Passages(ctx, mnemos.PassageQuery{Text: text, Limit: int(req.GetLimit()), RunID: runID})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "passages: %v", err)
	}
	out := &mnemosv1.PassagesResponse{Passages: make([]*mnemosv1.Passage, 0, len(passages))}
	for _, p := range passages {
		pb := &mnemosv1.Passage{EpisodeId: p.EpisodeID, Text: p.Text, Metadata: p.Metadata, Score: p.Score}
		if !p.At.IsZero() {
			pb.At = timestamppb.New(p.At)
		}
		out.Passages = append(out.Passages, pb)
	}
	return out, nil
}

// ContextBlock renders a run's context block (recall.context_block) with
// query.Engine.BuildContextBlock over the request's tenant connection, exactly
// as REST /v1/context and Memory.ContextBlock do. The block digests a whole
// run, so a run-scoped token may render only its own runs.
func (s *Server) ContextBlock(ctx context.Context, req *mnemosv1.ContextBlockRequest) (*mnemosv1.ContextBlockResponse, error) {
	runID := strings.TrimSpace(req.GetRunId())
	if runID == "" {
		return nil, status.Error(codes.InvalidArgument, "run_id is required")
	}
	if req.GetMaxTokens() < 0 {
		return nil, status.Error(codes.InvalidArgument, "max_tokens must be non-negative")
	}
	if err := requireRunScope(ctx, runID); err != nil {
		return nil, err
	}
	conn := s.connFor(ctx)
	engine := query.NewEngine(conn.Events, conn.Claims, conn.Relationships)
	block, err := engine.BuildContextBlock(ctx, query.ContextBlockOptions{
		RunID: runID, Query: req.GetQuery(), MaxTokens: int(req.GetMaxTokens()),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "build context block: %v", err)
	}
	return &mnemosv1.ContextBlockResponse{RunId: runID, Context: block}, nil
}
