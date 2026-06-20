// Package grpc implements the gRPC API surface for Mnemos. It mirrors
// the HTTP REST API (serve.go) using protobuf-generated types and
// gRPC interceptors for auth, logging, and panic recovery.
package grpc

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"

	"go.klarlabs.de/bolt"
	"go.klarlabs.de/mnemos/internal/auth"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/store"
	mnemosv1 "go.klarlabs.de/mnemos/proto/gen/mnemos/v1"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Server implements mnemosv1.MnemosServiceServer.
type Server struct {
	mnemosv1.UnimplementedMnemosServiceServer

	conn     *store.Conn
	writer   *govwrite.Writer
	verifier *auth.Verifier
	logger   *bolt.Logger
	version  string
}

// NewServer returns a gRPC server backed by the given store Conn.
// If verifier is non-nil, write RPCs require a valid bearer token in
// the "authorization" metadata.
//
// Every durable write the server performs routes through the governed
// govwrite.Writer (built over the borrowed conn) so the spec
// non-negotiable holds: no delivery adapter reaches a repository
// directly. The Writer borrows the conn — the caller keeps ownership and
// its existing close discipline.
func NewServer(conn *store.Conn, verifier *auth.Verifier, logger *bolt.Logger, version string) *Server {
	w, err := govwrite.Wrap(conn, logger)
	if err != nil {
		// Wrap fails only on a nil conn or a static plugin-registration
		// bug — both programming errors that must surface loudly rather
		// than silently degrade to an ungoverned write path.
		panic(fmt.Sprintf("grpc: build governed writer: %v", err))
	}
	return &Server{conn: conn, writer: w, verifier: verifier, logger: logger, version: version}
}

// Register registers the Mnemos service on the provided gRPC server.
func (s *Server) Register(gs *grpc.Server) {
	mnemosv1.RegisterMnemosServiceServer(gs, s)
}

// ---------------------------------------------------------------------------
// Interceptors
// ---------------------------------------------------------------------------

// UnaryInterceptor returns a grpc.UnaryServerInterceptor chain that
// handles auth, panic recovery, and access logging.
func (s *Server) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		start := time.Now()

		// Panic recovery
		defer func() {
			if rec := recover(); rec != nil {
				s.logger.Error().
					Str("method", info.FullMethod).
					Str("panic", fmt.Sprintf("%v", rec)).
					Str("stack", string(debug.Stack())).
					Msg("grpc_panic")
				resp = nil
				err = status.Errorf(codes.Internal, "internal error: panic")
			}
		}()

		// Auth
		ctx, authErr := s.authenticate(ctx, info.FullMethod)
		if authErr != nil {
			return nil, authErr
		}

		resp, err = handler(ctx, req)

		actor := actorFromContext(ctx)
		s.logger.Info().
			Str("method", info.FullMethod).
			Str("code", status.Code(err).String()).
			Dur("duration", time.Since(start)).
			Str("user_id", actor).
			Msg("grpc_request")

		return resp, err
	}
}

// authenticate extracts the bearer token from gRPC metadata, validates
// it when a verifier is configured, and attaches actor/scopes/runs to
// the context. Read methods (List*, Metrics, Health) skip auth when no
// token is present.
func (s *Server) authenticate(ctx context.Context, method string) (context.Context, error) {
	if s.verifier == nil {
		return ctx, nil
	}

	// Read methods allow anonymous access.
	if isReadMethod(method) {
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return ctx, nil
		}
		vals := md.Get("authorization")
		if len(vals) == 0 {
			return ctx, nil
		}
		return s.validateToken(ctx, vals[0])
	}

	// Write methods require a token.
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, status.Errorf(codes.Unauthenticated, "missing authorization metadata")
	}
	vals := md.Get("authorization")
	if len(vals) == 0 {
		return nil, status.Errorf(codes.Unauthenticated, "missing bearer token")
	}
	return s.validateToken(ctx, vals[0])
}

func (s *Server) validateToken(ctx context.Context, raw string) (context.Context, error) {
	const prefix = "Bearer "
	if !hasPrefix(raw, prefix) {
		return nil, status.Errorf(codes.Unauthenticated, "invalid authorization format")
	}
	tokenStr := raw[len(prefix):]
	if tokenStr == "" {
		return nil, status.Errorf(codes.Unauthenticated, "missing bearer token")
	}

	claims, err := s.verifier.ParseAndValidate(ctx, tokenStr)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "invalid or revoked token")
	}

	ctx = withActor(ctx, claims.UserID)
	ctx = withScopes(ctx, claims.Scopes)
	ctx = withAllowedRuns(ctx, claims.Runs)
	return ctx, nil
}

func isReadMethod(method string) bool {
	switch method {
	case "/mnemos.v1.MnemosService/Health",
		"/mnemos.v1.MnemosService/ListEvents",
		"/mnemos.v1.MnemosService/ListClaims",
		"/mnemos.v1.MnemosService/ListRelationships",
		"/mnemos.v1.MnemosService/ListEmbeddings",
		"/mnemos.v1.MnemosService/Metrics":
		return true
	}
	return false
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// ---------------------------------------------------------------------------
// Auth context helpers (mirror serve_auth.go)
// ---------------------------------------------------------------------------

type actorContextKey struct{}
type scopesContextKey struct{}
type runsContextKey struct{}

func withActor(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, actorContextKey{}, userID)
}

func withScopes(ctx context.Context, scopes []string) context.Context {
	return context.WithValue(ctx, scopesContextKey{}, scopes)
}

func withAllowedRuns(ctx context.Context, runs []string) context.Context {
	return context.WithValue(ctx, runsContextKey{}, runs)
}

func actorFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(actorContextKey{}).(string); ok && v != "" {
		return v
	}
	return domain.SystemUser
}

func scopesFromContext(ctx context.Context) []string {
	if v, ok := ctx.Value(scopesContextKey{}).([]string); ok {
		return v
	}
	return nil
}

func allowedRunsFromContext(ctx context.Context) []string {
	if v, ok := ctx.Value(runsContextKey{}).([]string); ok {
		return v
	}
	return nil
}

func (s *Server) requireScope(ctx context.Context, want string) error {
	// When no verifier is configured, auth is disabled — allow all.
	if s.verifier == nil {
		return nil
	}
	for _, s := range scopesFromContext(ctx) {
		if s == domain.ScopeWildcard || s == want {
			return nil
		}
	}
	return status.Errorf(codes.PermissionDenied, "missing required scope: %s", want)
}

// ---------------------------------------------------------------------------
// Health
// ---------------------------------------------------------------------------

// Health is the gRPC health probe. Returns the running version and a human-readable status string.
func (s *Server) Health(ctx context.Context, req *mnemosv1.HealthRequest) (*mnemosv1.HealthResponse, error) {
	if !req.Detailed {
		return &mnemosv1.HealthResponse{Status: "ok", Version: s.version}, nil
	}
	return &mnemosv1.HealthResponse{Status: "ok", Version: s.version, Healthy: true}, nil
}

// ---------------------------------------------------------------------------
// Events
// ---------------------------------------------------------------------------

// ListEvents returns events ordered by timestamp ascending. Pagination via Limit/PageToken (cursor = last event id).
func (s *Server) ListEvents(ctx context.Context, req *mnemosv1.ListEventsRequest) (*mnemosv1.ListEventsResponse, error) {
	limit, offset := normalizePagination(req.Pagination)

	all, err := s.conn.Events.ListAll(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list events: %v", err)
	}

	reversed := make([]domain.Event, len(all))
	for i, e := range all {
		reversed[len(all)-1-i] = e
	}
	total := len(reversed)
	page := paginate(reversed, limit, offset)

	events := make([]*mnemosv1.Event, 0, len(page))
	for _, e := range page {
		events = append(events, eventToProto(e))
	}
	return &mnemosv1.ListEventsResponse{Events: events, Total: int32(total), Limit: int32(limit), Offset: int32(offset)}, nil
}

// AppendEvents writes events idempotently. Re-appending the same id is a no-op (mirrors REST semantics).
func (s *Server) AppendEvents(ctx context.Context, req *mnemosv1.AppendEventsRequest) (*mnemosv1.AppendResponse, error) {
	if err := s.requireScope(ctx, domain.ScopeEventsWrite); err != nil {
		return nil, err
	}
	if len(req.Events) == 0 {
		return nil, status.Errorf(codes.InvalidArgument, "events array is empty")
	}
	if len(req.Events) > maxBatchRecords {
		return nil, status.Errorf(codes.InvalidArgument, "events batch size %d exceeds max %d", len(req.Events), maxBatchRecords)
	}

	events := make([]domain.Event, 0, len(req.Events))
	now := time.Now().UTC()
	actor := actorFromContext(ctx)
	for i, e := range req.Events {
		if e.Id == "" {
			return nil, status.Errorf(codes.InvalidArgument, "events[%d].id is required", i)
		}
		ts := now
		if e.Timestamp != nil {
			ts = e.Timestamp.AsTime()
		}
		ingested := now
		if e.IngestedAt != nil {
			ingested = e.IngestedAt.AsTime()
		}
		events = append(events, domain.Event{
			ID:            e.Id,
			RunID:         e.RunId,
			SchemaVersion: e.SchemaVersion,
			Content:       e.Content,
			SourceInputID: e.SourceInputId,
			Timestamp:     ts,
			Metadata:      e.Metadata,
			IngestedAt:    ingested,
			CreatedBy:     actor,
		})
	}

	accepted, err := s.writer.Events(ctx, events)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "append events: %v", err)
	}
	return &mnemosv1.AppendResponse{Accepted: int32(accepted)}, nil
}

// ---------------------------------------------------------------------------
// Claims
// ---------------------------------------------------------------------------

// ListClaims returns claims with optional type/status/run_id filters and
// cursor-based pagination. The run_id filter is the load-bearing tenant
// boundary for integrators: claims whose evidence cannot be traced to an
// event with the matching RunID are dropped (fail-closed).
func (s *Server) ListClaims(ctx context.Context, req *mnemosv1.ListClaimsRequest) (*mnemosv1.ListClaimsResponse, error) {
	limit, offset := normalizePagination(req.Pagination)
	typeFilter := req.TypeFilter
	statusFilter := req.StatusFilter
	runIDFilter := req.RunId

	if typeFilter != "" && !validClaimType(typeFilter) {
		return nil, status.Errorf(codes.InvalidArgument, "invalid type %q", typeFilter)
	}
	if statusFilter != "" && !validClaimStatus(statusFilter) {
		return nil, status.Errorf(codes.InvalidArgument, "invalid status %q", statusFilter)
	}

	// Build the allowed-event set when run_id is requested. Empty set →
	// no claims belong to this run; return early to avoid leaking
	// unfiltered claims (ADR-002 / Thor's safety audit).
	var allowedEventIDs map[string]struct{}
	if runIDFilter != "" {
		events, err := s.conn.Events.ListByRunID(ctx, runIDFilter)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "list events by run id: %v", err)
		}
		allowedEventIDs = make(map[string]struct{}, len(events))
		for _, e := range events {
			allowedEventIDs[e.ID] = struct{}{}
		}
		if len(allowedEventIDs) == 0 {
			return &mnemosv1.ListClaimsResponse{Limit: int32(limit), Offset: int32(offset)}, nil
		}
	}

	all, err := s.conn.Claims.ListAll(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list claims: %v", err)
	}
	var asOf, recordedAsOf time.Time
	if req.AsOf != nil {
		asOf = req.AsOf.AsTime()
	}
	if req.RecordedAsOf != nil {
		recordedAsOf = req.RecordedAsOf.AsTime()
	}
	filtered := all[:0]
	for _, c := range all {
		if typeFilter != "" && string(c.Type) != typeFilter {
			continue
		}
		if statusFilter != "" && string(c.Status) != statusFilter {
			continue
		}
		// Validity-time filter: claim must have been valid at as_of.
		// IsValidAt treats zero ValidFrom as "valid since forever".
		if !asOf.IsZero() && !c.IsValidAt(asOf) {
			continue
		}
		// Ingestion-time filter: drop rows recorded after the query
		// timestamp so the response is reproducible from the snapshot
		// of the store as it stood then.
		if !recordedAsOf.IsZero() && c.CreatedAt.After(recordedAsOf) {
			continue
		}
		filtered = append(filtered, c)
	}

	// run_id post-filter: drop claims whose evidence does not link to
	// an allowed event. Done after the cheaper type/status filters so
	// evidence is loaded only for surviving candidates.
	if allowedEventIDs != nil && len(filtered) > 0 {
		candidateIDs := make([]string, 0, len(filtered))
		for _, c := range filtered {
			candidateIDs = append(candidateIDs, c.ID)
		}
		evLinks, err := s.conn.Claims.ListEvidenceByClaimIDs(ctx, candidateIDs)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "load evidence for run_id filter: %v", err)
		}
		eventsByClaim := make(map[string][]string, len(evLinks))
		for _, link := range evLinks {
			eventsByClaim[link.ClaimID] = append(eventsByClaim[link.ClaimID], link.EventID)
		}
		kept := filtered[:0]
		for _, c := range filtered {
			matched := false
			for _, eid := range eventsByClaim[c.ID] {
				if _, ok := allowedEventIDs[eid]; ok {
					matched = true
					break
				}
			}
			if matched {
				kept = append(kept, c)
			}
		}
		filtered = kept
	}
	reversed := make([]domain.Claim, len(filtered))
	for i, c := range filtered {
		reversed[len(filtered)-1-i] = c
	}
	total := len(reversed)
	page := paginate(reversed, limit, offset)

	claims := make([]*mnemosv1.Claim, 0, len(page))
	ids := make([]string, 0, len(page))
	for _, c := range page {
		claims = append(claims, claimToProto(c))
		ids = append(ids, c.ID)
	}

	var evidence []*mnemosv1.ClaimEvidence
	if req.IncludeEvidence && len(ids) > 0 {
		links, err := s.conn.Claims.ListEvidenceByClaimIDs(ctx, ids)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "load evidence: %v", err)
		}
		for _, l := range links {
			evidence = append(evidence, &mnemosv1.ClaimEvidence{ClaimId: l.ClaimID, EventId: l.EventID})
		}
	}

	return &mnemosv1.ListClaimsResponse{Claims: claims, Evidence: evidence, Total: int32(total), Limit: int32(limit), Offset: int32(offset)}, nil
}

// AppendClaims upserts claims and their evidence links in a single batched call.
func (s *Server) AppendClaims(ctx context.Context, req *mnemosv1.AppendClaimsRequest) (*mnemosv1.AppendResponse, error) {
	if err := s.requireScope(ctx, domain.ScopeClaimsWrite); err != nil {
		return nil, err
	}
	if len(req.Claims) == 0 {
		return nil, status.Errorf(codes.InvalidArgument, "claims array is empty")
	}
	if len(req.Claims) > maxBatchRecords {
		return nil, status.Errorf(codes.InvalidArgument, "claims batch size %d exceeds max %d", len(req.Claims), maxBatchRecords)
	}
	if len(req.Evidence) > maxBatchRecords {
		return nil, status.Errorf(codes.InvalidArgument, "evidence batch size %d exceeds max %d", len(req.Evidence), maxBatchRecords)
	}

	claims := make([]domain.Claim, 0, len(req.Claims))
	now := time.Now().UTC()
	actor := actorFromContext(ctx)
	for i, c := range req.Claims {
		if c.Type != "" && !validClaimType(c.Type) {
			return nil, status.Errorf(codes.InvalidArgument, "claims[%d].type %q invalid", i, c.Type)
		}
		if c.Status != "" && !validClaimStatus(c.Status) {
			return nil, status.Errorf(codes.InvalidArgument, "claims[%d].status %q invalid", i, c.Status)
		}
		if c.Visibility != "" && !validClaimVisibility(c.Visibility) {
			return nil, status.Errorf(codes.InvalidArgument, "claims[%d].visibility %q invalid; must be personal, team, or org", i, c.Visibility)
		}
		created := now
		if c.CreatedAt != nil {
			created = c.CreatedAt.AsTime()
		}
		claims = append(claims, domain.Claim{
			ID:         c.Id,
			Text:       c.Text,
			Type:       domain.ClaimType(c.Type),
			Confidence: c.Confidence,
			Status:     domain.ClaimStatus(c.Status),
			CreatedAt:  created,
			CreatedBy:  actor,
			Visibility: domain.Visibility(c.Visibility),
		})
	}

	if allowed := allowedRunsFromContext(ctx); len(allowed) > 0 && len(req.Evidence) > 0 {
		return nil, status.Errorf(codes.Unimplemented, "run-scoped evidence validation not yet implemented for gRPC")
	}

	if _, err := s.writer.Claims(ctx, claims, govwrite.ClaimReason{}); err != nil {
		return nil, status.Errorf(codes.Internal, "upsert claims: %v", err)
	}

	if len(req.Evidence) > 0 {
		links := make([]domain.ClaimEvidence, 0, len(req.Evidence))
		for _, e := range req.Evidence {
			links = append(links, domain.ClaimEvidence{ClaimID: e.ClaimId, EventID: e.EventId})
		}
		if _, err := s.writer.EvidenceLinks(ctx, links); err != nil {
			return nil, status.Errorf(codes.Internal, "upsert evidence: %v", err)
		}
	}
	return &mnemosv1.AppendResponse{Accepted: int32(len(claims))}, nil
}

// ---------------------------------------------------------------------------
// Relationships
// ---------------------------------------------------------------------------

// ListRelationships returns relationships filtered by type/from/to with cursor pagination.
func (s *Server) ListRelationships(ctx context.Context, req *mnemosv1.ListRelationshipsRequest) (*mnemosv1.ListRelationshipsResponse, error) {
	limit, offset := normalizePagination(req.Pagination)
	typeFilter := req.TypeFilter
	if typeFilter != "" && typeFilter != "supports" && typeFilter != "contradicts" {
		return nil, status.Errorf(codes.InvalidArgument, "invalid type %q (want supports or contradicts)", typeFilter)
	}

	allClaims, err := s.conn.Claims.ListAll(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list claims for relationships: %v", err)
	}
	claimIDs := make([]string, 0, len(allClaims))
	for _, c := range allClaims {
		claimIDs = append(claimIDs, c.ID)
	}
	rels, err := s.conn.Relationships.ListByClaimIDs(ctx, claimIDs)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list relationships: %v", err)
	}
	filtered := rels[:0]
	for _, rel := range rels {
		if typeFilter != "" && string(rel.Type) != typeFilter {
			continue
		}
		filtered = append(filtered, rel)
	}
	reversed := make([]domain.Relationship, len(filtered))
	for i, rel := range filtered {
		reversed[len(filtered)-1-i] = rel
	}
	total := len(reversed)
	page := paginate(reversed, limit, offset)

	out := make([]*mnemosv1.Relationship, 0, len(page))
	for _, rel := range page {
		out = append(out, relationshipToProto(rel))
	}
	return &mnemosv1.ListRelationshipsResponse{Relationships: out, Total: int32(total), Limit: int32(limit), Offset: int32(offset)}, nil
}

// AppendRelationships upserts relationship rows. Idempotent on the (type, from, to) unique edge.
func (s *Server) AppendRelationships(ctx context.Context, req *mnemosv1.AppendRelationshipsRequest) (*mnemosv1.AppendResponse, error) {
	if err := s.requireScope(ctx, domain.ScopeRelationshipsWrite); err != nil {
		return nil, err
	}
	if len(req.Relationships) == 0 {
		return nil, status.Errorf(codes.InvalidArgument, "relationships array is empty")
	}
	if len(req.Relationships) > maxBatchRecords {
		return nil, status.Errorf(codes.InvalidArgument, "relationships batch size %d exceeds max %d", len(req.Relationships), maxBatchRecords)
	}

	rels := make([]domain.Relationship, 0, len(req.Relationships))
	now := time.Now().UTC()
	actor := actorFromContext(ctx)
	for i, rel := range req.Relationships {
		if rel.Type != "supports" && rel.Type != "contradicts" {
			return nil, status.Errorf(codes.InvalidArgument, "relationships[%d].type %q invalid", i, rel.Type)
		}
		created := now
		if rel.CreatedAt != nil {
			created = rel.CreatedAt.AsTime()
		}
		rels = append(rels, domain.Relationship{
			ID:          rel.Id,
			Type:        domain.RelationshipType(rel.Type),
			FromClaimID: rel.FromClaimId,
			ToClaimID:   rel.ToClaimId,
			CreatedAt:   created,
			CreatedBy:   actor,
		})
	}

	if _, err := s.writer.Relationships(ctx, rels); err != nil {
		return nil, status.Errorf(codes.Internal, "upsert relationships: %v", err)
	}
	return &mnemosv1.AppendResponse{Accepted: int32(len(rels))}, nil
}

// ---------------------------------------------------------------------------
// Embeddings
// ---------------------------------------------------------------------------

// ListEmbeddings returns embedding rows filtered by entity_type with cursor pagination.
func (s *Server) ListEmbeddings(ctx context.Context, req *mnemosv1.ListEmbeddingsRequest) (*mnemosv1.ListEmbeddingsResponse, error) {
	limit, offset := normalizePagination(req.Pagination)
	typeFilter := req.EntityType
	if typeFilter != "" && typeFilter != "event" && typeFilter != "claim" {
		return nil, status.Errorf(codes.InvalidArgument, "invalid entity_type %q (want event or claim)", typeFilter)
	}

	var records []domain.EmbeddingRecord
	wantedTypes := []string{typeFilter}
	if typeFilter == "" {
		wantedTypes = []string{"event", "claim"}
	}
	for _, t := range wantedTypes {
		recs, err := s.conn.Embeddings.ListByEntityType(ctx, t)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "list embeddings: %v", err)
		}
		records = append(records, recs...)
	}
	total := len(records)
	page := paginate(records, limit, offset)

	out := make([]*mnemosv1.Embedding, 0, len(page))
	for _, rec := range page {
		out = append(out, embeddingToProto(rec))
	}
	return &mnemosv1.ListEmbeddingsResponse{Embeddings: out, Total: int32(total), Limit: int32(limit), Offset: int32(offset)}, nil
}

// AppendEmbeddings upserts vector rows. Re-writing the same (entity_id, entity_type) replaces the vector.
func (s *Server) AppendEmbeddings(ctx context.Context, req *mnemosv1.AppendEmbeddingsRequest) (*mnemosv1.AppendResponse, error) {
	if err := s.requireScope(ctx, domain.ScopeEmbeddingsWrite); err != nil {
		return nil, err
	}
	if len(req.Embeddings) == 0 {
		return nil, status.Errorf(codes.InvalidArgument, "embeddings array is empty")
	}
	if len(req.Embeddings) > maxBatchRecords {
		return nil, status.Errorf(codes.InvalidArgument, "embeddings batch size %d exceeds max %d", len(req.Embeddings), maxBatchRecords)
	}

	actor := actorFromContext(ctx)
	accepted := 0
	for i, e := range req.Embeddings {
		if e.EntityId == "" {
			return nil, status.Errorf(codes.InvalidArgument, "embeddings[%d].entity_id is required", i)
		}
		if e.EntityType != "event" && e.EntityType != "claim" {
			return nil, status.Errorf(codes.InvalidArgument, "embeddings[%d].entity_type %q invalid", i, e.EntityType)
		}
		if len(e.Vector) == 0 {
			return nil, status.Errorf(codes.InvalidArgument, "embeddings[%d].vector is empty", i)
		}
		if e.Dimensions != 0 && int(e.Dimensions) != len(e.Vector) {
			return nil, status.Errorf(codes.InvalidArgument, "embeddings[%d]: dimensions=%d but vector length=%d", i, e.Dimensions, len(e.Vector))
		}
		if err := s.writer.Embedding(ctx, e.EntityId, e.EntityType, e.Vector, e.Model, actor); err != nil {
			return nil, status.Errorf(codes.Internal, "upsert embedding: %v", err)
		}
		accepted++
	}
	return &mnemosv1.AppendResponse{Accepted: int32(accepted)}, nil
}

// ---------------------------------------------------------------------------
// Metrics
// ---------------------------------------------------------------------------

// Metrics returns aggregate counts (events, claims, contradictions, embeddings) and the running version.
func (s *Server) Metrics(ctx context.Context, _ *mnemosv1.MetricsRequest) (*mnemosv1.MetricsResponse, error) {
	events, _ := s.conn.Events.ListAll(ctx)
	claims, _ := s.conn.Claims.ListAll(ctx)
	eventEmbs, _ := s.conn.Embeddings.ListByEntityType(ctx, "event")
	claimEmbs, _ := s.conn.Embeddings.ListByEntityType(ctx, "claim")

	runs := map[string]struct{}{}
	for _, e := range events {
		if e.RunID != "" {
			runs[e.RunID] = struct{}{}
		}
	}
	var contestedClaims int64
	for _, c := range claims {
		if c.Status == domain.ClaimStatusContested {
			contestedClaims++
		}
	}
	ids := make([]string, 0, len(claims))
	for _, c := range claims {
		ids = append(ids, c.ID)
	}
	rels, _ := s.conn.Relationships.ListByClaimIDs(ctx, ids)
	var contradictions int64
	for _, rel := range rels {
		if rel.Type == domain.RelationshipTypeContradicts {
			contradictions++
		}
	}

	return &mnemosv1.MetricsResponse{
		Runs:            int64(len(runs)),
		Events:          int64(len(events)),
		Claims:          int64(len(claims)),
		ContestedClaims: contestedClaims,
		Relationships:   int64(len(rels)),
		Contradictions:  contradictions,
		Embeddings:      int64(len(eventEmbs) + len(claimEmbs)),
	}, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const (
	maxBatchRecords   = 1000
	defaultServeLimit = 50
	maxServePageLimit = 200
)

func normalizePagination(p *mnemosv1.Pagination) (int, int) {
	if p == nil {
		return defaultServeLimit, 0
	}
	limit := int(p.Limit)
	if limit <= 0 {
		limit = defaultServeLimit
	}
	if limit > maxServePageLimit {
		limit = maxServePageLimit
	}
	offset := int(p.Offset)
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

func paginate[T any](xs []T, limit, offset int) []T {
	if offset >= len(xs) {
		return nil
	}
	end := offset + limit
	if end > len(xs) {
		end = len(xs)
	}
	return xs[offset:end]
}

func validClaimType(t string) bool {
	return t == string(domain.ClaimTypeFact) || t == string(domain.ClaimTypeHypothesis) || t == string(domain.ClaimTypeDecision) || t == string(domain.ClaimTypeTestResult)
}

func validClaimStatus(s string) bool {
	return s == string(domain.ClaimStatusActive) || s == string(domain.ClaimStatusContested) || s == string(domain.ClaimStatusResolved) || s == string(domain.ClaimStatusDeprecated)
}

func validClaimVisibility(s string) bool {
	return s == string(domain.VisibilityPersonal) || s == string(domain.VisibilityTeam) || s == string(domain.VisibilityOrg)
}

func eventToProto(e domain.Event) *mnemosv1.Event {
	return &mnemosv1.Event{
		Id:            e.ID,
		RunId:         e.RunID,
		SchemaVersion: e.SchemaVersion,
		Content:       e.Content,
		SourceInputId: e.SourceInputID,
		Timestamp:     timestamppb.New(e.Timestamp.UTC()),
		Metadata:      e.Metadata,
		IngestedAt:    timestamppb.New(e.IngestedAt.UTC()),
	}
}

func claimToProto(c domain.Claim) *mnemosv1.Claim {
	return &mnemosv1.Claim{
		Id:         c.ID,
		Text:       c.Text,
		Type:       string(c.Type),
		Confidence: c.Confidence,
		Status:     string(c.Status),
		CreatedAt:  timestamppb.New(c.CreatedAt.UTC()),
		Visibility: string(c.Visibility),
	}
}

func relationshipToProto(r domain.Relationship) *mnemosv1.Relationship {
	return &mnemosv1.Relationship{
		Id:          r.ID,
		Type:        string(r.Type),
		FromClaimId: r.FromClaimID,
		ToClaimId:   r.ToClaimID,
		CreatedAt:   timestamppb.New(r.CreatedAt.UTC()),
	}
}

func embeddingToProto(e domain.EmbeddingRecord) *mnemosv1.Embedding {
	return &mnemosv1.Embedding{
		EntityId:   e.EntityID,
		EntityType: e.EntityType,
		Vector:     e.Vector,
		Model:      e.Model,
		Dimensions: int32(e.Dimensions),
	}
}
