package grpc

import (
	"context"
	"errors"

	"go.klarlabs.de/mnemos/internal/dependency"
	"go.klarlabs.de/mnemos/internal/domain"
	mnemosv1 "go.klarlabs.de/mnemos/proto/gen/mnemos/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Source dependence between episodes (ADR 0038). Each RPC runs the same
// internal/dependency call as the CLI, MCP, REST and the Go library, through
// the request's tenant connection or writer, and confines a run-scoped token
// to its runs.

// ListEpisodeDependencies lists an episode's dependencies.
func (s *Server) ListEpisodeDependencies(ctx context.Context, req *mnemosv1.ListEpisodeDependenciesRequest) (*mnemosv1.ListEpisodeDependenciesResponse, error) {
	conn := s.connFor(ctx)
	allowed := allowedRunsFromContext(ctx)
	l, err := dependency.List(ctx, conn.Events, conn.Dependencies, dependency.ListRequest{
		EventID: req.GetEpisodeId(), IncludeRemoved: req.GetIncludeRemoved(),
		RunAllowed: func(runID string) bool { return runAllowed(allowed, runID) },
	})
	if err != nil {
		return nil, dependencyStatus(err, "list episode dependencies")
	}
	out := &mnemosv1.ListEpisodeDependenciesResponse{EpisodeId: l.EventID}
	for _, d := range l.DependsOn {
		out.DependsOn = append(out.DependsOn, dependencyToProto(d))
	}
	for _, d := range l.Dependents {
		out.Dependents = append(out.Dependents, dependencyToProto(d))
	}
	for _, p := range l.Pending {
		out.PendingCitations = append(out.PendingCitations, &mnemosv1.PendingCitation{LookupKey: p.LookupKey, CreatedAt: timestamppb.New(p.CreatedAt)})
	}
	return out, nil
}

// DeclareEpisodeDependency declares a dependency by hand.
func (s *Server) DeclareEpisodeDependency(ctx context.Context, req *mnemosv1.DeclareEpisodeDependencyRequest) (*mnemosv1.EpisodeDependencyChange, error) {
	if err := s.requireScope(ctx, domain.ScopeClaimsWrite); err != nil {
		return nil, err
	}
	w := s.writerFor(ctx)
	if w == nil {
		return nil, status.Error(codes.Unavailable, "writer unavailable")
	}
	allowed := allowedRunsFromContext(ctx)
	conn := w.Conn()
	change, err := dependency.Declare(ctx, conn.Events, conn.Dependencies, w.DeclareEventDependency, dependency.DeclareRequest{
		EventID: req.GetEpisodeId(), DependsOn: req.GetDependsOn(), Kind: req.GetKind(), Reference: req.GetNote(),
		Actor: actorFromContext(ctx), RunAllowed: func(runID string) bool { return runAllowed(allowed, runID) },
	})
	if err != nil {
		return nil, dependencyStatus(err, "declare episode dependency")
	}
	return changeToProto(change), nil
}

// RemoveEpisodeDependency removes a dependency, keeping it as a tombstone.
func (s *Server) RemoveEpisodeDependency(ctx context.Context, req *mnemosv1.RemoveEpisodeDependencyRequest) (*mnemosv1.EpisodeDependencyChange, error) {
	if err := s.requireScope(ctx, domain.ScopeClaimsWrite); err != nil {
		return nil, err
	}
	w := s.writerFor(ctx)
	if w == nil {
		return nil, status.Error(codes.Unavailable, "writer unavailable")
	}
	allowed := allowedRunsFromContext(ctx)
	conn := w.Conn()
	change, err := dependency.Remove(ctx, conn.Events, conn.Dependencies, w.RemoveEventDependency, dependency.RemoveRequest{
		EventID: req.GetEpisodeId(), DependsOn: req.GetDependsOn(),
		Actor: actorFromContext(ctx), RunAllowed: func(runID string) bool { return runAllowed(allowed, runID) },
	})
	if err != nil {
		return nil, dependencyStatus(err, "remove episode dependency")
	}
	return changeToProto(change), nil
}

func dependencyStatus(err error, label string) error {
	switch {
	case errors.Is(err, dependency.ErrEventIDRequired):
		return status.Error(codes.InvalidArgument, "episode_id is required")
	case errors.Is(err, dependency.ErrInvalid):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, dependency.ErrEventNotFound):
		return status.Error(codes.NotFound, "episode not found")
	case errors.Is(err, dependency.ErrNotFound):
		return status.Error(codes.NotFound, "episode dependency not found")
	case errors.Is(err, dependency.ErrExists):
		return status.Error(codes.AlreadyExists, "episode dependency already exists")
	default:
		return status.Errorf(codes.Internal, "%s: %v", label, err)
	}
}

func dependencyToProto(d domain.EventDependency) *mnemosv1.EpisodeDependency {
	out := &mnemosv1.EpisodeDependency{
		EpisodeId: d.EventID, DependsOn: d.DependsOn, Kind: string(d.Kind), Signal: string(d.Signal), Reference: d.Reference,
		CreatedAt: timestamppb.New(d.CreatedAt), CreatedBy: d.CreatedBy, RemovedBy: d.RemovedBy,
	}
	if d.RemovedAt != nil {
		out.RemovedAt = timestamppb.New(*d.RemovedAt)
	}
	return out
}

func changeToProto(c domain.DependencyChange) *mnemosv1.EpisodeDependencyChange {
	return &mnemosv1.EpisodeDependencyChange{
		Dependency: dependencyToProto(c.Dependency), RescoredBeliefs: c.RescoredBeliefs, SourceRetracted: c.SourceRetracted,
	}
}
