package main

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/incident"
	"go.klarlabs.de/mnemos/internal/query"
	"go.klarlabs.de/mnemos/internal/skills"
	"go.klarlabs.de/mnemos/internal/store"
)

// Skill-record listings and appends over REST: GET /v1/actions, GET
// /v1/outcomes, GET|POST /v1/tenant-schemas and GET|POST /v1/reflexes. Each
// handler translates its query or body and calls internal/skills — the
// implementation gRPC, MCP and the Go library share — against the request's
// tenant connection (reads) or tenant writer (writes).
//
// /v1/tenant-schemas is this tenant's schemas (lessons); /v1/schemas stays the
// schemas promoted to the shared global tier. Both are "schemas" in the brain
// vocabulary, so the tenant collection names its tier rather than reusing the
// promoted store's path.

// pageQuery reads ?limit= and ?offset= through skills.Page, the bounds every
// paged listing shares.
func pageQuery(r *http.Request) (int, int) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	return skills.Page(limit, offset)
}

// requestRunScope is the bearer's run allowlist as an internal/skills
// predicate, nil when the bearer is unrestricted.
func requestRunScope(r *http.Request) skills.RunScope {
	if len(allowedRunsFromContext(r.Context())) == 0 {
		return nil
	}
	return func(runID string) bool { return runAllowedForRequest(r, runID) }
}

// writeSkillsError maps internal/skills sentinels to HTTP statuses.
func writeSkillsError(w http.ResponseWriter, label string, err error) {
	switch {
	case errors.Is(err, skills.ErrInvalidRecord), errors.Is(err, skills.ErrInvalidFilter):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, skills.ErrRunNotAllowed):
		writeError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, skills.ErrUnsupported):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	default:
		writeInternalError(w, label, err)
	}
}

func writeList[T, D any](w http.ResponseWriter, key string, all []T, limit, offset int, dto func(T) D) {
	page := skills.Window(all, limit, offset)
	out := make([]D, 0, len(page))
	for _, x := range page {
		out = append(out, dto(x))
	}
	writeJSON(w, http.StatusOK, map[string]any{key: out, "total": len(all), "limit": limit, "offset": offset})
}

func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseRFC3339(field, v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return time.Time{}, errors.New(field + " must be RFC3339")
	}
	return t, nil
}

// --- actions and outcomes ----------------------------------------------------

type actionDTO struct {
	ID        string            `json:"id"`
	RunID     string            `json:"run_id,omitempty"`
	Kind      string            `json:"kind"`
	Subject   string            `json:"subject"`
	Actor     string            `json:"actor,omitempty"`
	At        string            `json:"at,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedBy string            `json:"created_by,omitempty"`
	CreatedAt string            `json:"created_at,omitempty"`
}

func actionToDTO(a domain.Action) actionDTO {
	return actionDTO{
		ID: a.ID, RunID: a.RunID, Kind: string(a.Kind), Subject: a.Subject, Actor: a.Actor,
		At: rfc3339(a.At), Metadata: a.Metadata, CreatedBy: a.CreatedBy, CreatedAt: rfc3339(a.CreatedAt),
	}
}

// listActionsHandler serves GET /v1/actions?subject=&run_id=&limit=&offset=.
// A run-scoped bearer sees only its runs' actions, and naming another run is
// a 403, matching gRPC ListActions.
func listActionsHandler(conn *store.Conn, w http.ResponseWriter, r *http.Request) {
	conn = scopedConn(r.Context(), conn)
	q := r.URL.Query()
	limit, offset := pageQuery(r)
	got, err := skills.Actions(r.Context(), conn.Actions, skills.ActionFilter{
		Subject: q.Get("subject"), RunID: q.Get("run_id"), Runs: requestRunScope(r),
	})
	if err != nil {
		writeSkillsError(w, "list actions", err)
		return
	}
	writeList(w, "actions", got, limit, offset, actionToDTO)
}

type outcomeDTO struct {
	ID         string             `json:"id"`
	ActionID   string             `json:"action_id"`
	Result     string             `json:"result"`
	Metrics    map[string]float64 `json:"metrics,omitempty"`
	Notes      string             `json:"notes,omitempty"`
	ObservedAt string             `json:"observed_at,omitempty"`
	Source     string             `json:"source,omitempty"`
	CreatedBy  string             `json:"created_by,omitempty"`
	CreatedAt  string             `json:"created_at,omitempty"`
}

func outcomeToDTO(o domain.Outcome) outcomeDTO {
	return outcomeDTO{
		ID: o.ID, ActionID: o.ActionID, Result: string(o.Result), Metrics: o.Metrics, Notes: o.Notes,
		ObservedAt: rfc3339(o.ObservedAt), Source: o.Source, CreatedBy: o.CreatedBy, CreatedAt: rfc3339(o.CreatedAt),
	}
}

// makeOutcomesHandler serves GET /v1/outcomes?action_id=&limit=&offset=. An
// outcome belongs to its action's run, so a run-scoped bearer sees only the
// outcomes of actions in its runs.
func makeOutcomesHandler(conn *store.Conn) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireGET(w, r) {
			return
		}
		conn := scopedConn(r.Context(), conn)
		limit, offset := pageQuery(r)
		got, err := skills.Outcomes(r.Context(), conn.Outcomes, conn.Actions, skills.OutcomeFilter{
			ActionID: r.URL.Query().Get("action_id"), Runs: requestRunScope(r),
		})
		if err != nil {
			writeSkillsError(w, "list outcomes", err)
			return
		}
		writeList(w, "outcomes", got, limit, offset, outcomeToDTO)
	}
}

// --- tenant schemas (lessons) ------------------------------------------------

type tenantSchemaDTO struct {
	ID           string     `json:"id"`
	Statement    string     `json:"statement"`
	Context      contextDTO `json:"context,omitempty"`
	Trigger      string     `json:"trigger,omitempty"`
	Kind         string     `json:"kind,omitempty"`
	Evidence     []string   `json:"evidence"`
	Confidence   float64    `json:"confidence"`
	Polarity     string     `json:"polarity,omitempty"`
	DerivedAt    string     `json:"derived_at,omitempty"`
	LastVerified string     `json:"last_verified,omitempty"`
	Source       string     `json:"source,omitempty"`
	CreatedBy    string     `json:"created_by,omitempty"`
}

func tenantSchemaToDTO(l domain.Lesson) tenantSchemaDTO {
	return tenantSchemaDTO{
		ID: l.ID, Statement: l.Statement, Context: contextDTO(l.Scope), Trigger: l.Trigger, Kind: l.Kind,
		Evidence: l.Evidence, Confidence: l.Confidence, Polarity: string(l.Polarity),
		DerivedAt: rfc3339(l.DerivedAt), LastVerified: rfc3339(l.LastVerified), Source: l.Source, CreatedBy: l.CreatedBy,
	}
}

func (d tenantSchemaDTO) toDomain() (domain.Lesson, error) {
	derived, err := parseRFC3339("derived_at", d.DerivedAt)
	if err != nil {
		return domain.Lesson{}, err
	}
	verified, err := parseRFC3339("last_verified", d.LastVerified)
	if err != nil {
		return domain.Lesson{}, err
	}
	return domain.Lesson{
		ID: d.ID, Statement: d.Statement, Scope: domain.Scope(d.Context), Trigger: d.Trigger, Kind: d.Kind,
		Evidence: d.Evidence, Confidence: d.Confidence, Polarity: domain.SchemaPolarity(d.Polarity),
		DerivedAt: derived, LastVerified: verified, Source: d.Source, CreatedBy: d.CreatedBy,
	}, nil
}

// makeTenantSchemasHandler serves GET /v1/tenant-schemas?service=&trigger=
// (list) and POST /v1/tenant-schemas {"schemas":[...]} (append, claims:write).
func makeTenantSchemasHandler(conn *store.Conn, gw *govwrite.Writer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			conn := scopedConn(r.Context(), conn)
			q := r.URL.Query()
			limit, offset := pageQuery(r)
			got, err := skills.Schemas(r.Context(), conn.Lessons, skills.SchemaFilter{Service: q.Get("service"), Trigger: q.Get("trigger")})
			if err != nil {
				writeSkillsError(w, "list tenant schemas", err)
				return
			}
			writeList(w, "schemas", got, limit, offset, tenantSchemaToDTO)
		case http.MethodPost:
			if !requireScope(w, r, domain.ScopeClaimsWrite) {
				return
			}
			var req struct {
				Schemas []tenantSchemaDTO `json:"schemas"`
			}
			if err := decodeJSON(r, &req); err != nil {
				writeError(w, http.StatusBadRequest, "decode body: "+err.Error())
				return
			}
			lessons := make([]domain.Lesson, 0, len(req.Schemas))
			for _, d := range req.Schemas {
				l, err := d.toDomain()
				if err != nil {
					writeError(w, http.StatusBadRequest, err.Error())
					return
				}
				lessons = append(lessons, l)
			}
			n, err := skills.AppendSchemas(r.Context(), scopedWriter(r.Context(), gw).Conn().Actions, scopedWriter(r.Context(), gw).Lesson, actorFromContext(r.Context()), lessons)
			if err != nil {
				writeSkillsError(w, "append tenant schemas", err)
				return
			}
			writeJSON(w, http.StatusCreated, map[string]int{"accepted": n})
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

// --- reflexes (playbooks) ------------------------------------------------------

type reflexStepDTO struct {
	Order       int    `json:"order"`
	Action      string `json:"action"`
	Description string `json:"description,omitempty"`
	Condition   string `json:"condition,omitempty"`
}

type reflexDTO struct {
	ID                 string          `json:"id"`
	Trigger            string          `json:"trigger"`
	Statement          string          `json:"statement"`
	Context            contextDTO      `json:"context,omitempty"`
	Steps              []reflexStepDTO `json:"steps"`
	DerivedFromSchemas []string        `json:"derived_from_schemas,omitempty"`
	Confidence         float64         `json:"confidence"`
	DerivedAt          string          `json:"derived_at,omitempty"`
	LastVerified       string          `json:"last_verified,omitempty"`
	Source             string          `json:"source,omitempty"`
	CreatedBy          string          `json:"created_by,omitempty"`
}

func reflexToDTO(p domain.Playbook) reflexDTO {
	steps := make([]reflexStepDTO, 0, len(p.Steps))
	for _, s := range p.Steps {
		steps = append(steps, reflexStepDTO(s))
	}
	return reflexDTO{
		ID: p.ID, Trigger: p.Trigger, Statement: p.Statement, Context: contextDTO(p.Scope), Steps: steps,
		DerivedFromSchemas: p.DerivedFromLessons, Confidence: p.Confidence,
		DerivedAt: rfc3339(p.DerivedAt), LastVerified: rfc3339(p.LastVerified), Source: p.Source, CreatedBy: p.CreatedBy,
	}
}

func (d reflexDTO) toDomain() (domain.Playbook, error) {
	derived, err := parseRFC3339("derived_at", d.DerivedAt)
	if err != nil {
		return domain.Playbook{}, err
	}
	verified, err := parseRFC3339("last_verified", d.LastVerified)
	if err != nil {
		return domain.Playbook{}, err
	}
	steps := make([]domain.PlaybookStep, 0, len(d.Steps))
	for _, s := range d.Steps {
		steps = append(steps, domain.PlaybookStep(s))
	}
	return domain.Playbook{
		ID: d.ID, Trigger: d.Trigger, Statement: d.Statement, Scope: domain.Scope(d.Context), Steps: steps,
		DerivedFromLessons: d.DerivedFromSchemas, Confidence: d.Confidence,
		DerivedAt: derived, LastVerified: verified, Source: d.Source, CreatedBy: d.CreatedBy,
	}, nil
}

// makeReflexesHandler serves GET /v1/reflexes?trigger=&service= (list) and
// POST /v1/reflexes {"reflexes":[...]} (append, claims:write).
func makeReflexesHandler(conn *store.Conn, gw *govwrite.Writer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			conn := scopedConn(r.Context(), conn)
			q := r.URL.Query()
			limit, offset := pageQuery(r)
			got, err := skills.Reflexes(r.Context(), conn.Playbooks, skills.ReflexFilter{Trigger: q.Get("trigger"), Service: q.Get("service")})
			if err != nil {
				writeSkillsError(w, "list reflexes", err)
				return
			}
			writeList(w, "reflexes", got, limit, offset, reflexToDTO)
		case http.MethodPost:
			if !requireScope(w, r, domain.ScopeClaimsWrite) {
				return
			}
			var req struct {
				Reflexes []reflexDTO `json:"reflexes"`
			}
			if err := decodeJSON(r, &req); err != nil {
				writeError(w, http.StatusBadRequest, "decode body: "+err.Error())
				return
			}
			playbooks := make([]domain.Playbook, 0, len(req.Reflexes))
			for _, d := range req.Reflexes {
				p, err := d.toDomain()
				if err != nil {
					writeError(w, http.StatusBadRequest, err.Error())
					return
				}
				playbooks = append(playbooks, p)
			}
			n, err := skills.AppendReflexes(r.Context(), scopedWriter(r.Context(), gw).Conn().Lessons, scopedWriter(r.Context(), gw).Playbook, actorFromContext(r.Context()), playbooks)
			if err != nil {
				writeSkillsError(w, "append reflexes", err)
				return
			}
			writeJSON(w, http.StatusCreated, map[string]int{"accepted": n})
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

// --- incidents -----------------------------------------------------------------

// writeIncidentError maps internal/incident sentinels to HTTP statuses.
func writeIncidentError(w http.ResponseWriter, label string, err error) {
	switch {
	case errors.Is(err, incident.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, incident.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, incident.ErrUnsupported):
		writeError(w, http.StatusNotImplemented, err.Error())
	default:
		writeInternalError(w, label, err)
	}
}

type provenanceSignalDTO struct {
	Name         string  `json:"name"`
	Value        float64 `json:"value"`
	Weight       float64 `json:"weight"`
	Contribution float64 `json:"contribution"`
	Detail       string  `json:"detail,omitempty"`
}

type beliefProvenanceDTO struct {
	BeliefID       string                `json:"belief_id"`
	Statement      string                `json:"statement"`
	Score          float64               `json:"score"`
	Signals        []provenanceSignalDTO `json:"signals,omitempty"`
	Rationale      string                `json:"rationale,omitempty"`
	ProseRationale string                `json:"prose_rationale,omitempty"`
	SourceDocument string                `json:"source_document,omitempty"`
	Liveness       string                `json:"liveness,omitempty"`
}

type whyWrongDTO struct {
	Incident          domain.Incident      `json:"incident"`
	RootBelief        *beliefProvenanceDTO `json:"root_belief,omitempty"`
	AffectedDecisions []decisionDTO        `json:"affected_decisions"`
	Explanation       string               `json:"explanation"`
}

func whyWrongToDTO(rep query.WhyWereWeWrongReport) whyWrongDTO {
	out := whyWrongDTO{Incident: rep.Incident, Explanation: rep.Explanation, AffectedDecisions: []decisionDTO{}}
	for _, d := range rep.AffectedDecisions {
		dto := decisionDTO{
			ID: d.ID, Statement: d.Statement, Plan: d.Plan, Reasoning: d.Reasoning, RiskLevel: string(d.RiskLevel),
			Beliefs: d.Beliefs, Alternatives: d.Alternatives, OutcomeID: d.OutcomeID, ChosenAt: rfc3339(d.ChosenAt),
		}
		out.AffectedDecisions = append(out.AffectedDecisions, dto)
	}
	if p := rep.RootClaim; p != nil {
		root := &beliefProvenanceDTO{
			BeliefID: p.ClaimID, Statement: p.ClaimText, Score: p.Score, Rationale: p.Rationale,
			ProseRationale: p.ProseRationale, SourceDocument: p.SourceDocument, Liveness: string(p.Liveness),
		}
		for _, s := range p.Signals {
			root.Signals = append(root.Signals, provenanceSignalDTO(s))
		}
		out.RootBelief = root
	}
	return out
}
