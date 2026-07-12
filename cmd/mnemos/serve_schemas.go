package main

import (
	"net/http"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
)

// ADR 0011 — the brain-native /v1/schemas read surface (neocortex).
//
// A schema is a de-identified generalization promoted to the shared neocortex
// tier by the consolidation pass. It has no pre-brain wire equivalent, so there
// is nothing to alias — it is simply part of the brain-native v1 contract. Its
// provenance is corroboration COUNTS (distinct_tenants / evidence_count), never
// tenant ids or raw evidence.

// contextDTO is the brain-native (Scope → Context) operational context on a
// promoted schema.
type contextDTO struct {
	Service string `json:"service,omitempty"`
	Env     string `json:"env,omitempty"`
	Team    string `json:"team,omitempty"`
}

// globalSchemaDTO is the wire form of a domain.GlobalSchema.
type globalSchemaDTO struct {
	ID              string     `json:"id"`
	Statement       string     `json:"statement"`
	Context         contextDTO `json:"context,omitempty"`
	Polarity        string     `json:"polarity,omitempty"`
	DistinctTenants int        `json:"distinct_tenants"`
	EvidenceCount   int        `json:"evidence_count"`
	Confidence      float64    `json:"confidence"`
	Surprise        float64    `json:"surprise,omitempty"`
	HasSurprise     bool       `json:"has_surprise,omitempty"`
	Status          string     `json:"status"`
	PromotedAt      string     `json:"promoted_at"`
	CreatedBy       string     `json:"created_by,omitempty"`
}

type schemasResponse struct {
	Schemas []globalSchemaDTO `json:"schemas"`
	Total   int               `json:"total"`
}

// makeSchemasHandler serves GET /v1/schemas — the neocortex read surface.
// Optional ?status=pending|active filter. The promoted-schema store is
// deliberately cross-tenant shared (the inverse of ADR-0007 isolation), so it is
// read from the base conn, not a tenant-scoped one. Returns 503 when the backend
// has not implemented the store (nil repo), matching the cognitive endpoints'
// graceful-degradation posture.
func makeSchemasHandler(conn *store.Conn) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if conn.GlobalSchemas == nil {
			writeError(w, http.StatusServiceUnavailable, "promoted-schema store not available on this backend")
			return
		}
		ctx := r.Context()
		statusRaw := r.URL.Query().Get("status")
		var (
			schemas []domain.GlobalSchema
			err     error
		)
		switch statusRaw {
		case "":
			schemas, err = conn.GlobalSchemas.ListAll(ctx)
		case string(domain.GlobalSchemaStatusPending), string(domain.GlobalSchemaStatusActive):
			schemas, err = conn.GlobalSchemas.ListByStatus(ctx, domain.GlobalSchemaStatus(statusRaw))
		default:
			writeError(w, http.StatusBadRequest, "status must be pending or active")
			return
		}
		if err != nil {
			writeInternalError(w, "list global schemas", err)
			return
		}
		out := schemasResponse{Schemas: make([]globalSchemaDTO, len(schemas)), Total: len(schemas)}
		for i, s := range schemas {
			out.Schemas[i] = globalSchemaToDTO(s)
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func globalSchemaToDTO(s domain.GlobalSchema) globalSchemaDTO {
	return globalSchemaDTO{
		ID:              s.ID,
		Statement:       s.Statement,
		Context:         contextDTO{Service: s.Scope.Service, Env: s.Scope.Env, Team: s.Scope.Team},
		Polarity:        string(s.Polarity),
		DistinctTenants: s.DistinctTenants,
		EvidenceCount:   s.EvidenceCount,
		Confidence:      s.Confidence,
		Surprise:        s.Surprise,
		HasSurprise:     s.HasSurprise,
		Status:          string(s.Status),
		PromotedAt:      s.PromotedAt.UTC().Format(time.RFC3339),
		CreatedBy:       s.CreatedBy,
	}
}
