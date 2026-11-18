package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	mnemos "go.klarlabs.de/mnemos"
	"go.klarlabs.de/mnemos/internal/beliefops"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/store"
)

// REST bindings for belief governance (belief.govern), single-fact writes
// (belief.remember_fact), valid-time scans (belief.scan) and entity
// associations. Every handler is a thin adapter: it decodes the request,
// checks scope, and calls the implementation the other transports share —
// internal/beliefops for governance and entity edges, the Memory facade for
// remember_fact and scan.

// --- POST /v1/beliefs/{id}/{deprecate,update,resolve,escalate,verify} -------

// beliefEditRequest is the body of the governance routes. Each route reads
// the fields it needs; all are optional except where noted.
type beliefEditRequest struct {
	// Reason is recorded on the status transition (deprecate, update, resolve)
	// or carried by the escalation.
	Reason string `json:"reason,omitempty"`
	// Text is the replacement text (update; required).
	Text string `json:"text,omitempty"`
	// Confidence replaces the confidence when > 0 (update).
	Confidence float64 `json:"confidence,omitempty"`
	// LoserID is the contradicting belief that loses (resolve; required). The
	// belief in the path wins.
	LoserID string `json:"loser_id,omitempty"`
}

// makeBeliefEditHandler serves one governance edit on claimID. op is the
// subresource name; scope is what the matching MCP tool requires.
func makeBeliefEditHandler(conn *store.Conn, gw *govwrite.Writer, claimID, op string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn := scopedConn(r.Context(), conn)
		gw := scopedWriter(r.Context(), gw)
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		scope := domain.ScopeClaimsWrite
		if op == "verify" {
			// memory_promote's scope: an explicit verification is the curator
			// act ADR 0012 gates behind promote:global.
			scope = domain.ScopePromoteGlobal
		}
		if !requireScope(w, r, scope) {
			return
		}
		var req beliefEditRequest
		if r.ContentLength != 0 {
			if err := decodeJSON(r, &req); err != nil {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("decode body: %v", err))
				return
			}
		}
		ctx := r.Context()
		edit := beliefops.Edit{ClaimID: claimID, Reason: req.Reason, Actor: actorFromContext(ctx)}
		var (
			out any
			err error
		)
		switch op {
		case "deprecate":
			out, err = beliefops.Deprecate(ctx, conn, gw, edit, beliefops.ReasonDeprecate)
		case "update":
			out, err = beliefops.Update(ctx, conn, gw, beliefops.UpdateRequest{Edit: edit, Text: req.Text, Confidence: req.Confidence})
		case "resolve":
			out, err = beliefops.Resolve(ctx, conn, gw, beliefops.ResolveRequest{
				WinnerID: claimID, LoserID: req.LoserID, Reason: req.Reason, Actor: edit.Actor,
			})
		case "escalate":
			out, err = beliefops.Escalate(ctx, conn, claimID, req.Reason)
		case "verify":
			out, err = beliefops.Verify(ctx, conn, gw, claimID, time.Now())
		default:
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		if err != nil {
			writeBeliefOpError(w, op+" belief", err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// --- POST /v1/facts ---------------------------------------------------------

// factRequest is the body of POST /v1/facts: one statement and the references
// backing it. Each non-blank evidence string becomes an observation episode
// the belief links to.
type factRequest struct {
	Text     string   `json:"text"`
	Evidence []string `json:"evidence"`
	// ValidFrom is when the fact became true (RFC 3339 or YYYY-MM-DD); empty
	// means now.
	ValidFrom string `json:"valid_from,omitempty"`
}

type factResponse struct {
	// BeliefID is the belief that holds the fact: the new one, or the one the
	// brain already held when the fact restated it.
	BeliefID string `json:"belief_id"`
	// Known is true when the brain already held the belief, so the fact's
	// evidence was linked to it instead of a new belief being written.
	Known bool `json:"known"`
}

// makeFactsHandler serves POST /v1/facts through Memory.RememberClaimWithEvidence,
// the implementation the Go library exposes. The episodes it records carry no
// run, so a run-scoped token is refused rather than allowed to write outside
// its runs.
func makeFactsHandler(mem mnemos.Memory) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mem := scopedMem(r.Context(), mem)
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if memUnavailable(w, mem) || !requireScope(w, r, domain.ScopeClaimsWrite) || !requireRunScope(w, r, "") {
			return
		}
		var req factRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("decode body: %v", err))
			return
		}
		if strings.TrimSpace(req.Text) == "" || !hasNonBlank(req.Evidence) {
			writeError(w, http.StatusBadRequest, "text and at least one non-blank evidence reference are required")
			return
		}
		var validFrom time.Time
		if req.ValidFrom != "" {
			t, err := parseInstant(req.ValidFrom)
			if err != nil {
				writeError(w, http.StatusBadRequest, "valid_from: "+err.Error())
				return
			}
			validFrom = t
		}
		got, err := mem.RememberClaimWithEvidenceDetailed(r.Context(), req.Text, req.Evidence, validFrom)
		if err != nil {
			writeInternalError(w, "remember fact", err)
			return
		}
		// 201 when a belief was created; 200 when the fact restated one the
		// brain already held and only its evidence was added.
		code := http.StatusCreated
		if got.Known {
			code = http.StatusOK
		}
		writeJSON(w, code, factResponse{BeliefID: got.ID, Known: got.Known})
	}
}

func hasNonBlank(xs []string) bool {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return true
		}
	}
	return false
}

// --- GET /v1/scan -----------------------------------------------------------

type scanResponse struct {
	Beliefs []fullClaimDTO `json:"beliefs"`
}

// makeScanHandler serves GET /v1/scan?valid_from=&valid_until=&limit= through
// Memory.Scan: beliefs whose valid-time interval overlaps the window, ordered
// by valid_from, capped at mnemos.ScanMaxResults. A scan spans every run, so a
// run-scoped token is refused.
func makeScanHandler(mem mnemos.Memory) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mem := scopedMem(r.Context(), mem)
		if !requireGET(w, r) || memUnavailable(w, mem) || !requireRunScope(w, r, "") {
			return
		}
		q := mnemos.ScanQuery{Limit: parseLimitQuery(r, 0)}
		for name, dst := range map[string]*time.Time{"valid_from": &q.ValidFrom, "valid_until": &q.ValidUntil} {
			if raw := r.URL.Query().Get(name); raw != "" {
				t, err := parseInstant(raw)
				if err != nil {
					writeError(w, http.StatusBadRequest, name+": "+err.Error())
					return
				}
				*dst = t
			}
		}
		claims, err := mem.Scan(r.Context(), q)
		if err != nil {
			writeInternalError(w, "scan", err)
			return
		}
		out := scanResponse{Beliefs: make([]fullClaimDTO, 0, len(claims))}
		for _, c := range claims {
			out.Beliefs = append(out.Beliefs, publicClaimToDTO(c))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// publicClaimToDTO renders a library Claim as the single-belief wire shape
// GET /v1/beliefs/{id} returns.
func publicClaimToDTO(c mnemos.Claim) fullClaimDTO {
	dto := fullClaimDTO{
		ID: c.ID, Statement: c.Statement, Type: c.Type,
		Confidence: c.Confidence, TrustScore: c.TrustScore, Lifecycle: string(c.Lifecycle),
	}
	if !c.ValidFrom.IsZero() {
		dto.ValidFrom = c.ValidFrom.UTC().Format(time.RFC3339)
	}
	if c.ValidUntil != nil && !c.ValidUntil.IsZero() {
		dto.ValidUntil = c.ValidUntil.UTC().Format(time.RFC3339)
	}
	if !c.RecordedAt.IsZero() {
		dto.RecordedAt = c.RecordedAt.UTC().Format(time.RFC3339)
	}
	return dto
}

// --- /v1/entity-associations ------------------------------------------------

// entityAssociationDTO is the wire shape of an entity-to-entity edge, the
// same fields as the gRPC EntityAssociation message.
type entityAssociationDTO struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	FromID    string `json:"from_id"`
	FromType  string `json:"from_type"`
	ToID      string `json:"to_id"`
	ToType    string `json:"to_type"`
	CreatedAt string `json:"created_at,omitempty"`
	CreatedBy string `json:"created_by,omitempty"`
}

type entityAssociationsResponse struct {
	Edges  []entityAssociationDTO `json:"edges"`
	Total  int                    `json:"total"`
	Limit  int                    `json:"limit"`
	Offset int                    `json:"offset"`
}

type appendEntityAssociationsRequest struct {
	Edges []entityAssociationDTO `json:"edges"`
}

// makeEntityAssociationsHandler serves GET (list; ?entity_id=&entity_type=,
// ?kind=, limit/offset) and POST (append) on /v1/entity-associations. Both
// call internal/beliefops, as the gRPC ListEntityAssociations and
// AppendEntityAssociations RPCs do; the append requires relationships:write,
// the scope those edges share with associations.
func makeEntityAssociationsHandler(conn *store.Conn, gw *govwrite.Writer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn := scopedConn(r.Context(), conn)
		gw := scopedWriter(r.Context(), gw)
		switch r.Method {
		case http.MethodGet:
			listEntityAssociations(conn, w, r)
		case http.MethodPost:
			appendEntityAssociations(gw, w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

func listEntityAssociations(conn *store.Conn, w http.ResponseWriter, r *http.Request) {
	limit, offset := parsePaginationFromQuery(r)
	q := r.URL.Query()
	edges, err := beliefops.ListEntityRelationships(r.Context(), conn, beliefops.EntityFilter{
		EntityID: q.Get("entity_id"), EntityType: q.Get("entity_type"), Kind: q.Get("kind"),
	})
	if err != nil {
		writeBeliefOpError(w, "list entity associations", err)
		return
	}
	out := entityAssociationsResponse{Total: len(edges), Limit: limit, Offset: offset}
	page := paginate(edges, limit, offset)
	out.Edges = make([]entityAssociationDTO, 0, len(page))
	for _, e := range page {
		out.Edges = append(out.Edges, entityAssociationDTO{
			ID: e.ID, Kind: string(e.Kind), FromID: e.FromID, FromType: e.FromType, ToID: e.ToID, ToType: e.ToType,
			CreatedAt: e.CreatedAt.UTC().Format(time.RFC3339), CreatedBy: e.CreatedBy,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func appendEntityAssociations(gw *govwrite.Writer, w http.ResponseWriter, r *http.Request) {
	if !requireScope(w, r, domain.ScopeRelationshipsWrite) {
		return
	}
	var req appendEntityAssociationsRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("decode body: %v", err))
		return
	}
	if rejectOversizedBatch(w, "edges", len(req.Edges)) {
		return
	}
	edges := make([]domain.EntityRelationship, 0, len(req.Edges))
	for i, e := range req.Edges {
		var createdAt time.Time
		if e.CreatedAt != "" {
			t, err := parseInstant(e.CreatedAt)
			if err != nil {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("edges[%d].created_at: %v", i, err))
				return
			}
			createdAt = t
		}
		edges = append(edges, domain.EntityRelationship{
			ID: e.ID, Kind: domain.RelationshipType(e.Kind), FromID: e.FromID, FromType: e.FromType,
			ToID: e.ToID, ToType: e.ToType, CreatedAt: createdAt, CreatedBy: e.CreatedBy,
		})
	}
	n, err := beliefops.AppendEntityRelationships(r.Context(), gw, edges, actorFromContext(r.Context()), time.Now())
	if err != nil {
		writeBeliefOpError(w, "append entity associations", err)
		return
	}
	writeJSON(w, http.StatusOK, appendResponse{Accepted: n})
}
