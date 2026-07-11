package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/store"
)

// ADR 0011 Phase D — brain-native REST API v2.
//
// v2 speaks the ubiquitous language on the wire (episode ← event, belief ←
// claim, association ← relationship, dissonance ← contradiction). It is a
// purely ADDITIVE surface: v1 stays byte-identical. Every v2 route reuses the
// exact v1 handler — same business logic, same auth/tenant-scoped request
// context — and differs ONLY in DTO field names and resource path. The
// translation is done by delegateV2, which runs the v1 handler against an
// in-memory capture writer and remaps the JSON body v1→v2 (reads) or v2→v1
// (writes). Because it literally calls v1, a v2 response is guaranteed to carry
// the same data v1 would, and there is zero risk of v1 drift.
//
// Deliberately kept terms (ADR 0011: "the machine term beats the brain"):
// evidence and embedding are NOT renamed. Resources with no v1 REST surface to
// alias (schema ← lesson, reflex ← playbook) are follow-ups — see the PR body.
//
// The route registrations live in THIS file, not serve.go, so the v1
// TestAPISurfaceParity fitness function (which scans serve.go) stays untouched
// and green; TestAPISurfaceParity_V2 scans this file instead.

// registerV2Routes mounts the brain-native v2 resources on the shared mux. It is
// called from newServerMuxWithMemory after the v1 routes, so v2 inherits the
// identical auth + tenant-scoping middleware chain (no security regression).
func registerV2Routes(mux *http.ServeMux, conn *store.Conn, gw *govwrite.Writer) {
	mux.HandleFunc("/v2/episodes", makeEpisodesHandlerV2(conn, gw))
	mux.HandleFunc("/v2/beliefs", makeBeliefsHandlerV2(conn, gw))
	mux.HandleFunc("/v2/associations", makeAssociationsHandlerV2(conn, gw))
	mux.HandleFunc("/v2/metrics", makeMetricsHandlerV2(conn))
	mux.HandleFunc("/v2/schemas", makeSchemasHandlerV2(conn))
}

// ---------------------------------------------------------------------------
// Delegation machinery
// ---------------------------------------------------------------------------

// v2CaptureWriter buffers a v1 handler's response so a v2 handler can remap the
// body to brain vocabulary before writing it to the real client.
type v2CaptureWriter struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func newV2CaptureWriter() *v2CaptureWriter {
	return &v2CaptureWriter{header: make(http.Header), status: http.StatusOK}
}

func (c *v2CaptureWriter) Header() http.Header         { return c.header }
func (c *v2CaptureWriter) WriteHeader(s int)           { c.status = s }
func (c *v2CaptureWriter) Write(b []byte) (int, error) { return c.body.Write(b) }

// delegateV2 runs a v1 handler and translates between brain-named v2 JSON and
// the v1 wire. reqRemap (POST) rewrites the request body v2→v1 before the v1
// handler sees it; respRemap rewrites a 2xx response body v1→v2. Either may be
// nil: a nil reqRemap passes the request body through unchanged; a nil respRemap
// passes the v1 response through unchanged (used for append responses, which
// carry only brain-neutral counts). Non-2xx v1 responses (validation errors,
// 404, 405) pass through byte-for-byte so v2 clients see identical failure
// semantics to v1.
func delegateV2(w http.ResponseWriter, r *http.Request, v1 http.HandlerFunc,
	reqRemap func([]byte) ([]byte, error), respRemap func([]byte) (any, error)) {

	if reqRemap != nil && r.Body != nil {
		raw, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, maxRequestBytes))
		if err != nil {
			writeError(w, http.StatusBadRequest, "read body: "+err.Error())
			return
		}
		mapped, err := reqRemap(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(mapped))
		r.ContentLength = int64(len(mapped))
	}

	rec := newV2CaptureWriter()
	v1(rec, r)

	// Errors and other non-2xx responses pass through untouched — the error
	// envelope carries no renamed field, and reproducing v1's exact status is a
	// contract the v2 client relies on.
	if rec.status < 200 || rec.status >= 300 || respRemap == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rec.status)
		_, _ = w.Write(rec.body.Bytes())
		return
	}

	out, err := respRemap(rec.body.Bytes())
	if err != nil {
		writeInternalError(w, "v2 response map", err)
		return
	}
	writeJSON(w, rec.status, out)
}

// decodeV2Strict decodes a v2 request body, rejecting unknown fields so v2
// clients get the same strictness v1's decodeJSON enforces.
func decodeV2Strict(body []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

// ---------------------------------------------------------------------------
// Episodes (← events)
// ---------------------------------------------------------------------------

// episodeDTO is the brain-native name for a v1 event. Its fields are already
// brain-neutral (id/content/timestamp/…), so it is a type alias — the rename is
// purely at the resource path and the collection key (events → episodes).
type episodeDTO = eventDTO

type episodesResponseV2 struct {
	Episodes []episodeDTO `json:"episodes"`
	Total    int          `json:"total"`
	Limit    int          `json:"limit"`
	Offset   int          `json:"offset"`
}

type appendEpisodesRequestV2 struct {
	Episodes []episodeDTO `json:"episodes"`
}

func makeEpisodesHandlerV2(conn *store.Conn, gw *govwrite.Writer) http.HandlerFunc {
	v1 := makeEventsHandler(conn, gw)
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			delegateV2(w, r, v1, nil, remapEventsToEpisodes)
		case http.MethodPost:
			delegateV2(w, r, v1, remapEpisodesReqToEvents, nil)
		default:
			delegateV2(w, r, v1, nil, nil)
		}
	}
}

func remapEventsToEpisodes(body []byte) (any, error) {
	var v1 eventsResponse
	if err := json.Unmarshal(body, &v1); err != nil {
		return nil, err
	}
	return episodesResponseV2{
		Episodes: v1.Events,
		Total:    v1.Total,
		Limit:    v1.Limit,
		Offset:   v1.Offset,
	}, nil
}

func remapEpisodesReqToEvents(body []byte) ([]byte, error) {
	var v2 appendEpisodesRequestV2
	if err := decodeV2Strict(body, &v2); err != nil {
		return nil, err
	}
	return json.Marshal(appendEventsRequest{Events: v2.Episodes})
}

// ---------------------------------------------------------------------------
// Beliefs (← claims)
// ---------------------------------------------------------------------------

// beliefDTO is the brain-native name for a v1 claim. Its fields are already
// brain-neutral (id/text/type/confidence/status/…), so it is a type alias; only
// the collection key (claims → beliefs) and the evidence cross-references
// (claim_id → belief_id, event_id → episode_id) are renamed.
type beliefDTO = claimDTO

// beliefEvidenceItem renames the v1 claimEvidenceItem cross-references to brain
// vocabulary. Evidence itself is a deliberately kept term.
type beliefEvidenceItem struct {
	BeliefID  string `json:"belief_id"`
	EpisodeID string `json:"episode_id"`
}

type beliefsResponseV2 struct {
	Beliefs  []beliefDTO          `json:"beliefs"`
	Evidence []beliefEvidenceItem `json:"evidence,omitempty"`
	Total    int                  `json:"total"`
	Limit    int                  `json:"limit"`
	Offset   int                  `json:"offset"`
}

type appendBeliefsRequestV2 struct {
	Beliefs  []beliefDTO          `json:"beliefs"`
	Evidence []beliefEvidenceItem `json:"evidence,omitempty"`
}

// deleteBeliefsResponseV2 is the brain-named form of deleteClaimsResponse
// (DELETE /v2/beliefs?run_id=…, the right-to-be-forgotten cascade).
type deleteBeliefsResponseV2 struct {
	RequestID           string `json:"request_id"`
	RunID               string `json:"run_id"`
	BeliefsDeleted      int    `json:"beliefs_deleted"`
	EpisodesDeleted     int    `json:"episodes_deleted"`
	EmbeddingsDeleted   int    `json:"embeddings_deleted"`
	AssociationsDeleted int    `json:"associations_deleted"`
}

func makeBeliefsHandlerV2(conn *store.Conn, gw *govwrite.Writer) http.HandlerFunc {
	v1 := makeClaimsHandler(conn, gw)
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			delegateV2(w, r, v1, nil, remapClaimsToBeliefs)
		case http.MethodPost:
			delegateV2(w, r, v1, remapBeliefsReqToClaims, nil)
		case http.MethodDelete:
			delegateV2(w, r, v1, nil, remapDeleteClaimsToBeliefs)
		default:
			delegateV2(w, r, v1, nil, nil)
		}
	}
}

func remapClaimsToBeliefs(body []byte) (any, error) {
	var v1 claimsResponse
	if err := json.Unmarshal(body, &v1); err != nil {
		return nil, err
	}
	out := beliefsResponseV2{
		Beliefs: v1.Claims,
		Total:   v1.Total,
		Limit:   v1.Limit,
		Offset:  v1.Offset,
	}
	if len(v1.Evidence) > 0 {
		out.Evidence = make([]beliefEvidenceItem, len(v1.Evidence))
		for i, e := range v1.Evidence {
			out.Evidence[i] = beliefEvidenceItem{BeliefID: e.ClaimID, EpisodeID: e.EventID}
		}
	}
	return out, nil
}

func remapBeliefsReqToClaims(body []byte) ([]byte, error) {
	var v2 appendBeliefsRequestV2
	if err := decodeV2Strict(body, &v2); err != nil {
		return nil, err
	}
	v1 := appendClaimsRequest{Claims: v2.Beliefs}
	if len(v2.Evidence) > 0 {
		v1.Evidence = make([]claimEvidenceItem, len(v2.Evidence))
		for i, e := range v2.Evidence {
			v1.Evidence[i] = claimEvidenceItem{ClaimID: e.BeliefID, EventID: e.EpisodeID}
		}
	}
	return json.Marshal(v1)
}

func remapDeleteClaimsToBeliefs(body []byte) (any, error) {
	var v1 deleteClaimsResponse
	if err := json.Unmarshal(body, &v1); err != nil {
		return nil, err
	}
	return deleteBeliefsResponseV2{
		RequestID:           v1.RequestID,
		RunID:               v1.RunID,
		BeliefsDeleted:      v1.ClaimsDeleted,
		EpisodesDeleted:     v1.EventsDeleted,
		EmbeddingsDeleted:   v1.EmbeddingsDeleted,
		AssociationsDeleted: v1.RelationshipsDeleted,
	}, nil
}

// ---------------------------------------------------------------------------
// Associations (← relationships)
// ---------------------------------------------------------------------------

// associationDTO renames a v1 relationship's belief cross-references
// (from_claim_id → from_belief_id, to_claim_id → to_belief_id). Edge type values
// ("supports"/"contradicts") are kept as-is; a contradicts edge is a dissonance.
type associationDTO struct {
	ID           string `json:"id"`
	Type         string `json:"type"`
	FromBeliefID string `json:"from_belief_id"`
	ToBeliefID   string `json:"to_belief_id"`
	CreatedAt    string `json:"created_at"`
}

type associationsResponseV2 struct {
	Associations []associationDTO `json:"associations"`
	Total        int              `json:"total"`
	Limit        int              `json:"limit"`
	Offset       int              `json:"offset"`
}

type appendAssociationsRequestV2 struct {
	Associations []associationDTO `json:"associations"`
}

func makeAssociationsHandlerV2(conn *store.Conn, gw *govwrite.Writer) http.HandlerFunc {
	v1 := makeRelationshipsHandler(conn, gw)
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			delegateV2(w, r, v1, nil, remapRelationshipsToAssociations)
		case http.MethodPost:
			delegateV2(w, r, v1, remapAssociationsReqToRelationships, nil)
		default:
			delegateV2(w, r, v1, nil, nil)
		}
	}
}

func remapRelationshipsToAssociations(body []byte) (any, error) {
	var v1 relationshipsResponse
	if err := json.Unmarshal(body, &v1); err != nil {
		return nil, err
	}
	out := associationsResponseV2{
		Total:  v1.Total,
		Limit:  v1.Limit,
		Offset: v1.Offset,
	}
	out.Associations = make([]associationDTO, len(v1.Relationships))
	for i, rel := range v1.Relationships {
		out.Associations[i] = associationDTO{
			ID:           rel.ID,
			Type:         rel.Type,
			FromBeliefID: rel.FromClaimID,
			ToBeliefID:   rel.ToClaimID,
			CreatedAt:    rel.CreatedAt,
		}
	}
	return out, nil
}

func remapAssociationsReqToRelationships(body []byte) ([]byte, error) {
	var v2 appendAssociationsRequestV2
	if err := decodeV2Strict(body, &v2); err != nil {
		return nil, err
	}
	v1 := appendRelationshipsRequest{Relationships: make([]relationshipDTO, len(v2.Associations))}
	for i, a := range v2.Associations {
		v1.Relationships[i] = relationshipDTO{
			ID:          a.ID,
			Type:        a.Type,
			FromClaimID: a.FromBeliefID,
			ToClaimID:   a.ToBeliefID,
			CreatedAt:   a.CreatedAt,
		}
	}
	return json.Marshal(v1)
}

// ---------------------------------------------------------------------------
// Metrics (brain-native counts)
// ---------------------------------------------------------------------------

// brainMetricsResponseV2 renames the aggregate counts to brain vocabulary
// (events → episodes, claims → beliefs, contradictions → dissonances,
// relationships → associations). Embedding is a deliberately kept term.
type brainMetricsResponseV2 struct {
	Runs             int64 `json:"runs"`
	Episodes         int64 `json:"episodes"`
	Beliefs          int64 `json:"beliefs"`
	ContestedBeliefs int64 `json:"contested_beliefs"`
	Associations     int64 `json:"associations"`
	Dissonances      int64 `json:"dissonances"`
	Embeddings       int64 `json:"embeddings"`
}

func makeMetricsHandlerV2(conn *store.Conn) http.HandlerFunc {
	v1 := makeMetricsHandler(conn)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			delegateV2(w, r, v1, nil, nil)
			return
		}
		delegateV2(w, r, v1, nil, remapMetricsToBrain)
	}
}

// ---------------------------------------------------------------------------
// Schemas (neocortex — promoted global schemas; NEW in v2, no v1 alias)
// ---------------------------------------------------------------------------

// contextDTO is the brain-native (Scope ← Context) operational context on a
// promoted schema.
type contextDTO struct {
	Service string `json:"service,omitempty"`
	Env     string `json:"env,omitempty"`
	Team    string `json:"team,omitempty"`
}

// globalSchemaDTO is the brain-native wire form of a domain.GlobalSchema — a
// de-identified generalization promoted to the shared neocortex tier (ADR 0011).
// It is a v2-only resource: it has no v1 equivalent, so it needs no alias. Its
// provenance is corroboration COUNTS (distinct_tenants / evidence_count), never
// tenant ids or raw evidence.
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

type schemasResponseV2 struct {
	Schemas []globalSchemaDTO `json:"schemas"`
	Total   int               `json:"total"`
}

// makeSchemasHandlerV2 serves GET /v2/schemas — the neocortex read surface.
// Optional ?status=pending|active filter. The promoted-schema store is
// deliberately cross-tenant shared (the inverse of ADR-0007 isolation), so it is
// read from the base conn, not a tenant-scoped one. Returns 503 when the backend
// has not implemented the store (nil repo), matching the cognitive endpoints'
// graceful-degradation posture.
func makeSchemasHandlerV2(conn *store.Conn) http.HandlerFunc {
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
		out := schemasResponseV2{Schemas: make([]globalSchemaDTO, len(schemas)), Total: len(schemas)}
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

func remapMetricsToBrain(body []byte) (any, error) {
	var v1 metricsResponse
	if err := json.Unmarshal(body, &v1); err != nil {
		return nil, err
	}
	return brainMetricsResponseV2{
		Runs:             v1.Runs,
		Episodes:         v1.Events,
		Beliefs:          v1.Claims,
		ContestedBeliefs: v1.ContestedClaims,
		Associations:     v1.Relationships,
		Dissonances:      v1.Contradictions,
		Embeddings:       v1.Embeddings,
	}, nil
}
