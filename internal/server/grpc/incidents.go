package grpc

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/incident"
	"go.klarlabs.de/mnemos/internal/query"
	"go.klarlabs.de/mnemos/internal/skills"
	mnemosv1 "go.klarlabs.de/mnemos/proto/gen/mnemos/v1"
)

// Skill-record and incident RPCs. Each one translates its request, calls the
// implementation REST, MCP and the Go library share (internal/skills,
// internal/incident), and maps that package's sentinel errors to codes.

// runScope is the bearer's run whitelist as an internal/skills predicate, nil
// when the token is unrestricted.
func (s *Server) runScope(ctx context.Context) skills.RunScope {
	allowed := allowedRunsFromContext(ctx)
	if len(allowed) == 0 {
		return nil
	}
	return func(runID string) bool { return runAllowed(allowed, runID) }
}

func skillsStatus(op string, err error) error {
	switch {
	case errors.Is(err, skills.ErrInvalidRecord), errors.Is(err, skills.ErrInvalidFilter):
		return status.Errorf(codes.InvalidArgument, "%s: %v", op, err)
	case errors.Is(err, skills.ErrRunNotAllowed):
		return status.Errorf(codes.PermissionDenied, "%s: %v", op, err)
	case errors.Is(err, skills.ErrUnsupported):
		return status.Errorf(codes.Unimplemented, "%s: %v", op, err)
	}
	return status.Errorf(codes.Internal, "%s: %v", op, err)
}

func incidentStatus(op string, err error) error {
	switch {
	case errors.Is(err, incident.ErrInvalid):
		return status.Errorf(codes.InvalidArgument, "%s: %v", op, err)
	case errors.Is(err, incident.ErrNotFound):
		return status.Errorf(codes.NotFound, "%s: %v", op, err)
	}
	return status.Errorf(codes.Internal, "%s: %v", op, err)
}

// ListGlobalSchemas returns the schemas promoted to the shared global tier.
// The promoted store is deliberately cross-tenant (ADR 0011/0012), so it is
// read from the base connection, never a tenant-scoped one — as REST does.
func (s *Server) ListGlobalSchemas(ctx context.Context, req *mnemosv1.ListGlobalSchemasRequest) (*mnemosv1.ListGlobalSchemasResponse, error) {
	limit, offset := normalizePagination(req.Pagination)
	got, err := skills.GlobalSchemas(ctx, s.conn.GlobalSchemas, req.GetStatus())
	if err != nil {
		return nil, skillsStatus("list global schemas", err)
	}
	page := paginate(got, limit, offset)
	out := make([]*mnemosv1.GlobalSchema, 0, len(page))
	for _, g := range page {
		out = append(out, globalSchemaToProto(g))
	}
	return &mnemosv1.ListGlobalSchemasResponse{GlobalSchemas: out, Total: int32(len(got)), Limit: int32(limit), Offset: int32(offset)}, nil
}

// OpenIncident records an incident through the request's tenant writer. The
// author is the token's subject, never a request field.
func (s *Server) OpenIncident(ctx context.Context, req *mnemosv1.OpenIncidentRequest) (*mnemosv1.Incident, error) {
	if err := s.requireScope(ctx, domain.ScopeClaimsWrite); err != nil {
		return nil, err
	}
	w := s.writerFor(ctx)
	if w == nil {
		return nil, status.Error(codes.Unavailable, "writer unavailable")
	}
	inc, err := incident.Open(ctx, s.connFor(ctx).Incidents, w.Incident, incident.OpenRequest{
		ID: req.GetId(), Title: req.GetTitle(), Summary: req.GetSummary(), Severity: req.GetSeverity(),
		RootCauseClaimID: req.GetRootCauseBeliefId(), DecisionIDs: req.GetDecisionIds(),
		OutcomeIDs: req.GetOutcomeIds(), TimelineEventIDs: req.GetTimelineEpisodeIds(),
		Actor: actorFromContext(ctx),
	})
	if err != nil {
		return nil, incidentStatus("open incident", err)
	}
	return incidentToProto(inc), nil
}

// ListIncidents returns incidents, optionally by severity and/or status.
func (s *Server) ListIncidents(ctx context.Context, req *mnemosv1.ListIncidentsRequest) (*mnemosv1.ListIncidentsResponse, error) {
	limit, offset := normalizePagination(req.Pagination)
	got, err := incident.List(ctx, s.connFor(ctx).Incidents, incident.Filter{Severity: req.GetSeverity(), Status: req.GetStatus()})
	if err != nil {
		return nil, incidentStatus("list incidents", err)
	}
	page := paginate(got, limit, offset)
	out := make([]*mnemosv1.Incident, 0, len(page))
	for _, inc := range page {
		out = append(out, incidentToProto(inc))
	}
	return &mnemosv1.ListIncidentsResponse{Incidents: out, Total: int32(len(got)), Limit: int32(limit), Offset: int32(offset)}, nil
}

// GetIncident returns one incident.
func (s *Server) GetIncident(ctx context.Context, req *mnemosv1.GetIncidentRequest) (*mnemosv1.Incident, error) {
	inc, err := incident.Get(ctx, s.connFor(ctx).Incidents, req.GetId())
	if err != nil {
		return nil, incidentStatus("get incident", err)
	}
	return incidentToProto(inc), nil
}

// ResolveIncident marks an incident resolved through the request's tenant
// writer.
func (s *Server) ResolveIncident(ctx context.Context, req *mnemosv1.ResolveIncidentRequest) (*mnemosv1.Incident, error) {
	if err := s.requireScope(ctx, domain.ScopeClaimsWrite); err != nil {
		return nil, err
	}
	w := s.writerFor(ctx)
	if w == nil {
		return nil, status.Error(codes.Unavailable, "writer unavailable")
	}
	inc, err := incident.Resolve(ctx, s.connFor(ctx).Incidents, w.ResolveIncident, req.GetId())
	if err != nil {
		return nil, incidentStatus("resolve incident", err)
	}
	return incidentToProto(inc), nil
}

// WhyWereWeWrong runs the post-incident analysis.
func (s *Server) WhyWereWeWrong(ctx context.Context, req *mnemosv1.WhyWereWeWrongRequest) (*mnemosv1.WhyWereWeWrongResponse, error) {
	rep, err := incident.WhyWereWeWrong(ctx, s.connFor(ctx), req.GetIncidentId())
	if err != nil {
		return nil, incidentStatus("why were we wrong", err)
	}
	return whyWrongToProto(rep), nil
}

func globalSchemaToProto(g domain.GlobalSchema) *mnemosv1.GlobalSchema {
	return &mnemosv1.GlobalSchema{
		Id:              g.ID,
		Statement:       g.Statement,
		Context:         contextToProto(g.Scope),
		Polarity:        string(g.Polarity),
		DistinctTenants: int32(g.DistinctTenants),
		EvidenceCount:   int32(g.EvidenceCount),
		Confidence:      g.Confidence,
		Surprise:        g.Surprise,
		HasSurprise:     g.HasSurprise,
		Status:          string(g.Status),
		PromotedAt:      timestampOrNil(g.PromotedAt),
		CreatedBy:       g.CreatedBy,
	}
}

func incidentToProto(i domain.Incident) *mnemosv1.Incident {
	return &mnemosv1.Incident{
		Id:                 i.ID,
		Title:              i.Title,
		Summary:            i.Summary,
		Severity:           string(i.Severity),
		Status:             string(i.Status),
		RootCauseBeliefId:  i.RootCauseClaimID,
		DecisionIds:        i.DecisionIDs,
		OutcomeIds:         i.OutcomeIDs,
		TimelineEpisodeIds: i.TimelineEventIDs,
		ReflexId:           i.PlaybookID,
		OpenedAt:           timestampOrNil(i.OpenedAt),
		ResolvedAt:         timestampOrNil(i.ResolvedAt),
		CreatedBy:          i.CreatedBy,
	}
}

func whyWrongToProto(r query.WhyWereWeWrongReport) *mnemosv1.WhyWereWeWrongResponse {
	out := &mnemosv1.WhyWereWeWrongResponse{
		Incident:    incidentToProto(r.Incident),
		Explanation: r.Explanation,
	}
	for _, d := range r.AffectedDecisions {
		out.AffectedDecisions = append(out.AffectedDecisions, decisionToProto(d))
	}
	if p := r.RootClaim; p != nil {
		out.RootBelief = provenanceToProto(*p)
	}
	return out
}
