// Phase 2-7 gRPC service methods. Lives in a sibling file to keep
// the original server.go (events/claims/relationships/embeddings)
// readable while letting the new entity surfaces share the same
// helpers (paginate, normalizePagination, actorFromContext, scope
// guards).

package grpc

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"go.klarlabs.de/mnemos/internal/beliefops"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/skills"
	mnemosv1 "go.klarlabs.de/mnemos/proto/gen/mnemos/v1"
)

// ---------------------------------------------------------------------------
// Actions
// ---------------------------------------------------------------------------

// ListActions returns recorded operational actions with optional
// subject/run filters and cursor pagination.
//
// F.4.b on the read side, matching ListBeliefs. run_id here was an OPTIONAL
// filter the caller chose, never a boundary the token imposed: a run-scoped
// bearer that named someone else's run — or simply asked by subject, or asked
// for nothing at all — read every run's actions. domain.Action carries its
// RunID, so the whitelist can decide which actions are legible directly.
func (s *Server) ListActions(ctx context.Context, req *mnemosv1.ListActionsRequest) (*mnemosv1.ListActionsResponse, error) {
	limit, offset := normalizePagination(req.Pagination)

	// Naming a run outside the whitelist is refused rather than silently
	// answered with an empty page — a caller that cannot tell "no actions"
	// from "not yours" will read the first as fact. Checked before the read.
	// Narrowing to the whitelist covers the subject branch (which spans runs)
	// and the default branch (which lists the lot). Actions with no run at all
	// are dropped too: a run-scoped token was granted named runs, and
	// "unassigned" is not one of them — the same reading ListEpisodes takes.
	actions, err := skills.Actions(ctx, s.connFor(ctx).Actions, skills.ActionFilter{
		Subject: req.Subject, RunID: req.RunId, Runs: s.runScope(ctx),
	})
	if errors.Is(err, skills.ErrRunNotAllowed) {
		return nil, status.Errorf(codes.PermissionDenied, "run_id %q not in token whitelist", req.RunId)
	}
	if err != nil {
		return nil, skillsStatus("list actions", err)
	}

	total := len(actions)
	page := paginate(actions, limit, offset)
	out := make([]*mnemosv1.Action, 0, len(page))
	for _, a := range page {
		out = append(out, actionToProto(a))
	}
	return &mnemosv1.ListActionsResponse{Actions: out, Total: int32(total), Limit: int32(limit), Offset: int32(offset)}, nil
}

// AppendActions writes operational actions idempotently.
func (s *Server) AppendActions(ctx context.Context, req *mnemosv1.AppendActionsRequest) (*mnemosv1.AppendResponse, error) {
	if err := s.requireScope(ctx, domain.ScopeClaimsWrite); err != nil {
		return nil, err
	}
	if len(req.Actions) == 0 {
		return nil, status.Errorf(codes.InvalidArgument, "actions array is empty")
	}
	if len(req.Actions) > maxBatchRecords {
		return nil, status.Errorf(codes.InvalidArgument, "actions batch size %d exceeds max %d", len(req.Actions), maxBatchRecords)
	}
	actor := actorFromContext(ctx)
	accepted := 0
	for i, a := range req.Actions {
		if a.Id == "" {
			return nil, status.Errorf(codes.InvalidArgument, "actions[%d].id is required", i)
		}
		at := time.Now().UTC()
		if a.At != nil {
			at = a.At.AsTime()
		}
		action := domain.Action{
			ID:        a.Id,
			RunID:     a.RunId,
			Kind:      domain.ActionKind(a.Kind),
			Subject:   a.Subject,
			Actor:     a.Actor,
			At:        at,
			Metadata:  a.Metadata,
			CreatedBy: firstNonEmptyStr(a.CreatedBy, actor),
		}
		if _, err := s.writerFor(ctx).Action(ctx, action); err != nil {
			return nil, status.Errorf(codes.Internal, "append action %s: %v", action.ID, err)
		}
		accepted++
	}
	return &mnemosv1.AppendResponse{Accepted: int32(accepted)}, nil
}

// ---------------------------------------------------------------------------
// Outcomes
// ---------------------------------------------------------------------------

// ListOutcomes returns observed action outcomes.
func (s *Server) ListOutcomes(ctx context.Context, req *mnemosv1.ListOutcomesRequest) (*mnemosv1.ListOutcomesResponse, error) {
	limit, offset := normalizePagination(req.Pagination)
	// An outcome belongs to its action's run, so a run-scoped token sees only
	// the outcomes of actions inside its whitelist.
	conn := s.connFor(ctx)
	outcomes, err := skills.Outcomes(ctx, conn.Outcomes, conn.Actions, skills.OutcomeFilter{
		ActionID: req.ActionId, Runs: s.runScope(ctx),
	})
	if err != nil {
		return nil, skillsStatus("list outcomes", err)
	}
	total := len(outcomes)
	page := paginate(outcomes, limit, offset)
	out := make([]*mnemosv1.Outcome, 0, len(page))
	for _, o := range page {
		out = append(out, outcomeToProto(o))
	}
	return &mnemosv1.ListOutcomesResponse{Outcomes: out, Total: int32(total), Limit: int32(limit), Offset: int32(offset)}, nil
}

// AppendOutcomes writes outcomes idempotently. The grpc surface does
// NOT auto-fire action_of/outcome_of edges; that wiring lives in the
// CLI/MCP layer for now.
func (s *Server) AppendOutcomes(ctx context.Context, req *mnemosv1.AppendOutcomesRequest) (*mnemosv1.AppendResponse, error) {
	if err := s.requireScope(ctx, domain.ScopeClaimsWrite); err != nil {
		return nil, err
	}
	if len(req.Outcomes) == 0 {
		return nil, status.Errorf(codes.InvalidArgument, "outcomes array is empty")
	}
	if len(req.Outcomes) > maxBatchRecords {
		return nil, status.Errorf(codes.InvalidArgument, "outcomes batch size %d exceeds max %d", len(req.Outcomes), maxBatchRecords)
	}
	actor := actorFromContext(ctx)
	accepted := 0
	for i, o := range req.Outcomes {
		if o.Id == "" {
			return nil, status.Errorf(codes.InvalidArgument, "outcomes[%d].id is required", i)
		}
		observed := time.Now().UTC()
		if o.ObservedAt != nil {
			observed = o.ObservedAt.AsTime()
		}
		outcome := domain.Outcome{
			ID:         o.Id,
			ActionID:   o.ActionId,
			Result:     domain.OutcomeResult(o.Result),
			Metrics:    o.Metrics,
			Notes:      o.Notes,
			ObservedAt: observed,
			Source:     o.Source,
			CreatedBy:  firstNonEmptyStr(o.CreatedBy, actor),
		}
		// autoEdge=false: the grpc surface intentionally does NOT fire
		// action_of/outcome_of edges (see the godoc above); that wiring
		// lives in the CLI/MCP layer.
		if _, err := s.writerFor(ctx).Outcome(ctx, outcome, false); err != nil {
			return nil, status.Errorf(codes.Internal, "append outcome %s: %v", outcome.ID, err)
		}
		accepted++
	}
	return &mnemosv1.AppendResponse{Accepted: int32(accepted)}, nil
}

// ---------------------------------------------------------------------------
// Schemas
// ---------------------------------------------------------------------------

// ListSchemas returns synthesised schemas with optional service or
// trigger filter.
func (s *Server) ListSchemas(ctx context.Context, req *mnemosv1.ListSchemasRequest) (*mnemosv1.ListSchemasResponse, error) {
	limit, offset := normalizePagination(req.Pagination)
	lessons, err := skills.Schemas(ctx, s.connFor(ctx).Lessons, skills.SchemaFilter{Service: req.Service, Trigger: req.Trigger})
	if err != nil {
		return nil, skillsStatus("list schemas", err)
	}
	total := len(lessons)
	page := paginate(lessons, limit, offset)
	out := make([]*mnemosv1.Schema, 0, len(page))
	for _, l := range page {
		out = append(out, schemaToProto(l))
	}
	return &mnemosv1.ListSchemasResponse{Schemas: out, Total: int32(total), Limit: int32(limit), Offset: int32(offset)}, nil
}

// AppendSchemas writes schemas idempotently.
func (s *Server) AppendSchemas(ctx context.Context, req *mnemosv1.AppendSchemasRequest) (*mnemosv1.AppendResponse, error) {
	if err := s.requireScope(ctx, domain.ScopeClaimsWrite); err != nil {
		return nil, err
	}
	lessons := make([]domain.Lesson, 0, len(req.Schemas))
	for _, l := range req.Schemas {
		lessons = append(lessons, domain.Lesson{
			ID:           l.Id,
			Statement:    l.Statement,
			Scope:        contextFromProto(l.Context),
			Trigger:      l.Trigger,
			Kind:         l.Kind,
			Evidence:     l.Evidence,
			Confidence:   l.Confidence,
			DerivedAt:    timestampOrZero(l.DerivedAt),
			LastVerified: timestampOrZero(l.LastVerified),
			Source:       l.Source,
			CreatedBy:    l.CreatedBy,
		})
	}
	accepted, err := skills.AppendSchemas(ctx, s.writerFor(ctx).Conn().Actions, s.writerFor(ctx).Lesson, actorFromContext(ctx), lessons)
	if err != nil {
		return nil, skillsStatus("append schemas", err)
	}
	return &mnemosv1.AppendResponse{Accepted: int32(accepted)}, nil
}

// ---------------------------------------------------------------------------
// Decisions
// ---------------------------------------------------------------------------

// ListDecisions returns recorded decisions.
func (s *Server) ListDecisions(ctx context.Context, req *mnemosv1.ListDecisionsRequest) (*mnemosv1.ListDecisionsResponse, error) {
	limit, offset := normalizePagination(req.Pagination)
	var decisions []domain.Decision
	var err error
	if req.RiskLevel != "" {
		decisions, err = s.connFor(ctx).Decisions.ListByRiskLevel(ctx, req.RiskLevel)
	} else {
		decisions, err = s.connFor(ctx).Decisions.ListAll(ctx)
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list decisions: %v", err)
	}
	total := len(decisions)
	page := paginate(decisions, limit, offset)
	out := make([]*mnemosv1.Decision, 0, len(page))
	for _, d := range page {
		out = append(out, decisionToProto(d))
	}
	return &mnemosv1.ListDecisionsResponse{Decisions: out, Total: int32(total), Limit: int32(limit), Offset: int32(offset)}, nil
}

// AppendDecisions writes decisions idempotently.
func (s *Server) AppendDecisions(ctx context.Context, req *mnemosv1.AppendDecisionsRequest) (*mnemosv1.AppendResponse, error) {
	if err := s.requireScope(ctx, domain.ScopeClaimsWrite); err != nil {
		return nil, err
	}
	if len(req.Decisions) == 0 {
		return nil, status.Errorf(codes.InvalidArgument, "decisions array is empty")
	}
	if len(req.Decisions) > maxBatchRecords {
		return nil, status.Errorf(codes.InvalidArgument, "decisions batch size %d exceeds max %d", len(req.Decisions), maxBatchRecords)
	}
	actor := actorFromContext(ctx)
	accepted := 0
	for i, d := range req.Decisions {
		if d.Id == "" {
			return nil, status.Errorf(codes.InvalidArgument, "decisions[%d].id is required", i)
		}
		chosen := time.Now().UTC()
		if d.ChosenAt != nil {
			chosen = d.ChosenAt.AsTime()
		}
		decision := domain.Decision{
			ID:           d.Id,
			Statement:    d.Statement,
			Plan:         d.Plan,
			Reasoning:    d.Reasoning,
			RiskLevel:    domain.RiskLevel(d.RiskLevel),
			Beliefs:      d.Beliefs,
			Alternatives: d.Alternatives,
			OutcomeID:    d.OutcomeId,
			Scope:        contextFromProto(d.Context),
			ChosenAt:     chosen,
			CreatedBy:    firstNonEmptyStr(d.CreatedBy, actor),
		}
		if _, err := s.writerFor(ctx).Decision(ctx, decision); err != nil {
			return nil, status.Errorf(codes.Internal, "append decision %s: %v", decision.ID, err)
		}
		accepted++
	}
	return &mnemosv1.AppendResponse{Accepted: int32(accepted)}, nil
}

// ---------------------------------------------------------------------------
// Reflexes
// ---------------------------------------------------------------------------

// ListReflexes returns synthesised or hand-authored reflexes.
func (s *Server) ListReflexes(ctx context.Context, req *mnemosv1.ListReflexesRequest) (*mnemosv1.ListReflexesResponse, error) {
	limit, offset := normalizePagination(req.Pagination)
	playbooks, err := skills.Reflexes(ctx, s.connFor(ctx).Playbooks, skills.ReflexFilter{Trigger: req.Trigger, Service: req.Service})
	if err != nil {
		return nil, skillsStatus("list reflexes", err)
	}
	total := len(playbooks)
	page := paginate(playbooks, limit, offset)
	out := make([]*mnemosv1.Reflex, 0, len(page))
	for _, p := range page {
		out = append(out, reflexToProto(p))
	}
	return &mnemosv1.ListReflexesResponse{Reflexes: out, Total: int32(total), Limit: int32(limit), Offset: int32(offset)}, nil
}

// AppendReflexes writes reflexes idempotently.
func (s *Server) AppendReflexes(ctx context.Context, req *mnemosv1.AppendReflexesRequest) (*mnemosv1.AppendResponse, error) {
	if err := s.requireScope(ctx, domain.ScopeClaimsWrite); err != nil {
		return nil, err
	}
	playbooks := make([]domain.Playbook, 0, len(req.Reflexes))
	for _, p := range req.Reflexes {
		steps := make([]domain.PlaybookStep, 0, len(p.Steps))
		for _, st := range p.Steps {
			steps = append(steps, domain.PlaybookStep{
				Order:       int(st.Order),
				Action:      st.Action,
				Description: st.Description,
				Condition:   st.Condition,
			})
		}
		playbooks = append(playbooks, domain.Playbook{
			ID:                 p.Id,
			Trigger:            p.Trigger,
			Statement:          p.Statement,
			Scope:              contextFromProto(p.Context),
			Steps:              steps,
			DerivedFromLessons: p.DerivedFromSchemas,
			Confidence:         p.Confidence,
			DerivedAt:          timestampOrZero(p.DerivedAt),
			LastVerified:       timestampOrZero(p.LastVerified),
			Source:             p.Source,
			CreatedBy:          p.CreatedBy,
		})
	}
	accepted, err := skills.AppendReflexes(ctx, s.writerFor(ctx).Conn().Lessons, s.writerFor(ctx).Playbook, actorFromContext(ctx), playbooks)
	if err != nil {
		return nil, skillsStatus("append reflexes", err)
	}
	return &mnemosv1.AppendResponse{Accepted: int32(accepted)}, nil
}

// ---------------------------------------------------------------------------
// EntityAssociations (polymorphic edges)
// ---------------------------------------------------------------------------

// ListEntityAssociations returns polymorphic cross-entity edges
// (beliefops.ListEntityRelationships, shared with REST
// GET /v1/entity-associations and the list_entity_associations MCP tool).
func (s *Server) ListEntityAssociations(ctx context.Context, req *mnemosv1.ListEntityAssociationsRequest) (*mnemosv1.ListEntityAssociationsResponse, error) {
	limit, offset := normalizePagination(req.Pagination)
	edges, err := beliefops.ListEntityRelationships(ctx, s.connFor(ctx), beliefops.EntityFilter{
		EntityID: req.EntityId, EntityType: req.EntityType, Kind: req.Kind,
	})
	if err != nil {
		return nil, beliefOpStatus("list entity_associations", err)
	}
	total := len(edges)
	page := paginate(edges, limit, offset)
	out := make([]*mnemosv1.EntityAssociation, 0, len(page))
	for _, e := range page {
		out = append(out, entityAssociationToProto(e))
	}
	return &mnemosv1.ListEntityAssociationsResponse{Edges: out, Total: int32(total), Limit: int32(limit), Offset: int32(offset)}, nil
}

// AppendEntityAssociations writes polymorphic edges idempotently
// (beliefops.AppendEntityRelationships, shared with REST
// POST /v1/entity-associations).
func (s *Server) AppendEntityAssociations(ctx context.Context, req *mnemosv1.AppendEntityAssociationsRequest) (*mnemosv1.AppendResponse, error) {
	// relationships:write, not claims:write: these are edges, the same resource
	// family AppendAssociations already gates on.
	if err := s.requireScope(ctx, domain.ScopeRelationshipsWrite); err != nil {
		return nil, err
	}
	if len(req.Edges) > maxBatchRecords {
		return nil, status.Errorf(codes.InvalidArgument, "edges batch size %d exceeds max %d", len(req.Edges), maxBatchRecords)
	}
	edges := make([]domain.EntityRelationship, 0, len(req.Edges))
	for _, e := range req.Edges {
		var createdAt time.Time
		if e.CreatedAt != nil {
			createdAt = e.CreatedAt.AsTime()
		}
		edges = append(edges, domain.EntityRelationship{
			ID: e.Id, Kind: domain.RelationshipType(e.Kind), FromID: e.FromId, FromType: e.FromType,
			ToID: e.ToId, ToType: e.ToType, CreatedAt: createdAt, CreatedBy: e.CreatedBy,
		})
	}
	n, err := beliefops.AppendEntityRelationships(ctx, s.writerFor(ctx), edges, actorFromContext(ctx), time.Now())
	if err != nil {
		return nil, beliefOpStatus("upsert entity_associations", err)
	}
	return &mnemosv1.AppendResponse{Accepted: int32(n)}, nil //nolint:gosec // bounded by maxBatchRecords
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func actionToProto(a domain.Action) *mnemosv1.Action {
	return &mnemosv1.Action{
		Id:        a.ID,
		RunId:     a.RunID,
		Kind:      string(a.Kind),
		Subject:   a.Subject,
		Actor:     a.Actor,
		At:        timestampOrNil(a.At),
		Metadata:  a.Metadata,
		CreatedBy: a.CreatedBy,
		CreatedAt: timestampOrNil(a.CreatedAt),
	}
}

func outcomeToProto(o domain.Outcome) *mnemosv1.Outcome {
	return &mnemosv1.Outcome{
		Id:         o.ID,
		ActionId:   o.ActionID,
		Result:     string(o.Result),
		Metrics:    o.Metrics,
		Notes:      o.Notes,
		ObservedAt: timestampOrNil(o.ObservedAt),
		Source:     o.Source,
		CreatedBy:  o.CreatedBy,
		CreatedAt:  timestampOrNil(o.CreatedAt),
	}
}

func schemaToProto(l domain.Lesson) *mnemosv1.Schema {
	return &mnemosv1.Schema{
		Id:           l.ID,
		Statement:    l.Statement,
		Context:      contextToProto(l.Scope),
		Trigger:      l.Trigger,
		Kind:         l.Kind,
		Evidence:     l.Evidence,
		Confidence:   l.Confidence,
		DerivedAt:    timestampOrNil(l.DerivedAt),
		LastVerified: timestampOrNil(l.LastVerified),
		Source:       l.Source,
		CreatedBy:    l.CreatedBy,
	}
}

func decisionToProto(d domain.Decision) *mnemosv1.Decision {
	return &mnemosv1.Decision{
		Id:           d.ID,
		Statement:    d.Statement,
		Plan:         d.Plan,
		Reasoning:    d.Reasoning,
		RiskLevel:    string(d.RiskLevel),
		Beliefs:      d.Beliefs,
		Alternatives: d.Alternatives,
		OutcomeId:    d.OutcomeID,
		Context:      contextToProto(d.Scope),
		ChosenAt:     timestampOrNil(d.ChosenAt),
		CreatedBy:    d.CreatedBy,
		CreatedAt:    timestampOrNil(d.CreatedAt),
	}
}

func reflexToProto(p domain.Playbook) *mnemosv1.Reflex {
	steps := make([]*mnemosv1.ReflexStep, 0, len(p.Steps))
	for _, st := range p.Steps {
		steps = append(steps, &mnemosv1.ReflexStep{
			Order:       int32(st.Order),
			Action:      st.Action,
			Description: st.Description,
			Condition:   st.Condition,
		})
	}
	return &mnemosv1.Reflex{
		Id:                 p.ID,
		Trigger:            p.Trigger,
		Statement:          p.Statement,
		Context:            contextToProto(p.Scope),
		Steps:              steps,
		DerivedFromSchemas: p.DerivedFromLessons,
		Confidence:         p.Confidence,
		DerivedAt:          timestampOrNil(p.DerivedAt),
		LastVerified:       timestampOrNil(p.LastVerified),
		Source:             p.Source,
		CreatedBy:          p.CreatedBy,
	}
}

func entityAssociationToProto(e domain.EntityRelationship) *mnemosv1.EntityAssociation {
	return &mnemosv1.EntityAssociation{
		Id:        e.ID,
		Kind:      string(e.Kind),
		FromId:    e.FromID,
		FromType:  e.FromType,
		ToId:      e.ToID,
		ToType:    e.ToType,
		CreatedAt: timestampOrNil(e.CreatedAt),
		CreatedBy: e.CreatedBy,
	}
}

func contextToProto(s domain.Scope) *mnemosv1.Context {
	if s.IsEmpty() {
		return nil
	}
	return &mnemosv1.Context{Service: s.Service, Env: s.Env, Team: s.Team}
}

func contextFromProto(s *mnemosv1.Context) domain.Scope {
	if s == nil {
		return domain.Scope{}
	}
	return domain.Scope{Service: s.Service, Env: s.Env, Team: s.Team}
}

func timestampOrNil(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

func timestampOrZero(t *timestamppb.Timestamp) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.AsTime()
}

func firstNonEmptyStr(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
