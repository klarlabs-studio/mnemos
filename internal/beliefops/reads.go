package beliefops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/query"
	"go.klarlabs.de/mnemos/internal/store"
)

// Provenance explains where a belief's trust came from
// (query.Engine.WhyTrustClaim). An unknown belief is ErrNotFound.
func Provenance(ctx context.Context, conn *store.Conn, claimID string) (domain.ProvenanceReport, error) {
	id := strings.TrimSpace(claimID)
	if _, err := loadClaims(ctx, conn, id); err != nil {
		return domain.ProvenanceReport{}, err
	}
	return query.NewEngine(conn.Events, conn.Claims, conn.Relationships).WhyTrustClaim(ctx, id)
}

// History returns a belief's version chain newest first, so a caller diffing
// can compare versions[0] with versions[1] without re-sorting. A belief with
// no recorded versions (or an unknown id) has an empty history.
func History(ctx context.Context, conn *store.Conn, claimID string) ([]domain.ClaimVersion, error) {
	id := strings.TrimSpace(claimID)
	if id == "" {
		return nil, invalid("belief id is required")
	}
	if conn.ClaimVersions == nil {
		return nil, fmt.Errorf("version history: %w", ErrUnsupported)
	}
	versions, err := conn.ClaimVersions.ListByClaim(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("list belief versions: %w", err)
	}
	if versions == nil {
		versions = []domain.ClaimVersion{}
	}
	return versions, nil
}

// EntityFilter narrows an entity-association listing. EntityID and EntityType
// together select the edges touching one entity; otherwise Kind selects one
// edge kind; with neither, every edge is returned.
type EntityFilter struct {
	EntityID   string
	EntityType string
	Kind       string
}

// ListEntityRelationships returns the entity-to-entity edges the filter
// selects.
func ListEntityRelationships(ctx context.Context, conn *store.Conn, f EntityFilter) ([]domain.EntityRelationship, error) {
	if conn.EntityRels == nil {
		return nil, fmt.Errorf("entity associations: %w", ErrUnsupported)
	}
	if (f.EntityID == "") != (f.EntityType == "") {
		return nil, invalid("entity_id and entity_type must be given together")
	}
	var (
		edges []domain.EntityRelationship
		err   error
	)
	switch {
	case f.EntityID != "":
		edges, err = conn.EntityRels.ListByEntity(ctx, f.EntityID, f.EntityType)
	case f.Kind != "":
		edges, err = conn.EntityRels.ListByKind(ctx, f.Kind)
	default:
		edges, err = conn.EntityRels.ListAll(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("list entity associations: %w", err)
	}
	if edges == nil {
		edges = []domain.EntityRelationship{}
	}
	return edges, nil
}

// EntityWriter is the governed write the append needs.
type EntityWriter interface {
	EntityRels(ctx context.Context, edges []domain.EntityRelationship) (int, error)
}

// AppendEntityRelationships validates and writes entity-to-entity edges,
// idempotently by id. A zero CreatedAt becomes now and an empty CreatedBy
// becomes actor. Nothing is written when any edge is invalid.
func AppendEntityRelationships(ctx context.Context, w EntityWriter, edges []domain.EntityRelationship, actor string, now time.Time) (int, error) {
	if len(edges) == 0 {
		return 0, invalid("edges array is empty")
	}
	out := make([]domain.EntityRelationship, len(edges))
	for i, e := range edges {
		if e.CreatedAt.IsZero() {
			e.CreatedAt = now.UTC()
		}
		if strings.TrimSpace(e.CreatedBy) == "" {
			e.CreatedBy = actor
		}
		if err := e.Validate(); err != nil {
			return 0, invalid("edges[%d]: %v", i, err)
		}
		out[i] = e
	}
	n, err := w.EntityRels(ctx, out)
	if err != nil {
		return 0, fmt.Errorf("write entity associations: %w", err)
	}
	return n, nil
}
