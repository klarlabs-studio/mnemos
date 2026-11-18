package client

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
)

// Skill-loop records and incidents over HTTP. Go names use machine terms
// (Lesson, Playbook, Claim); JSON keys are the server's brain-native wire
// names (schema, reflex, belief). Times are RFC 3339 strings; parse them with
// [ParseTime]. Lists are paged: Limit defaults to 50 server-side and is capped
// at 200; Total counts every match.

// Scope is the {service, env, team} context of a lesson, playbook or
// promoted schema.
type Scope struct {
	Service string `json:"service,omitempty"`
	Env     string `json:"env,omitempty"`
	Team    string `json:"team,omitempty"`
}

// Action is a recorded operational action.
type Action struct {
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

// ActionQuery selects actions for [Client.ListActions].
type ActionQuery struct {
	Subject string
	RunID   string
	Limit   int
	Offset  int
}

// ActionList is one page of actions.
type ActionList struct {
	Actions []Action `json:"actions"`
	Total   int      `json:"total"`
	Limit   int      `json:"limit"`
	Offset  int      `json:"offset"`
}

// Outcome is the recorded result of an action.
type Outcome struct {
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

// OutcomeQuery selects outcomes for [Client.ListOutcomes].
type OutcomeQuery struct {
	ActionID string
	Limit    int
	Offset   int
}

// OutcomeList is one page of outcomes.
type OutcomeList struct {
	Outcomes []Outcome `json:"outcomes"`
	Total    int       `json:"total"`
	Limit    int       `json:"limit"`
	Offset   int       `json:"offset"`
}

// Lesson is this tenant's generalisation from action→outcome chains (a
// "schema" on the wire).
type Lesson struct {
	ID           string   `json:"id"`
	Statement    string   `json:"statement"`
	Scope        Scope    `json:"context,omitempty"`
	Trigger      string   `json:"trigger,omitempty"`
	Kind         string   `json:"kind,omitempty"`
	Evidence     []string `json:"evidence"` // action ids; at least one is required to append
	Confidence   float64  `json:"confidence"`
	Polarity     string   `json:"polarity,omitempty"`
	DerivedAt    string   `json:"derived_at,omitempty"`
	LastVerified string   `json:"last_verified,omitempty"`
	Source       string   `json:"source,omitempty"`
	CreatedBy    string   `json:"created_by,omitempty"`
}

// LessonQuery selects lessons for [Client.ListLessons].
type LessonQuery struct {
	Service string
	Trigger string
	Limit   int
	Offset  int
}

// LessonList is one page of lessons.
type LessonList struct {
	Lessons []Lesson `json:"schemas"`
	Total   int      `json:"total"`
	Limit   int      `json:"limit"`
	Offset  int      `json:"offset"`
}

// PlaybookStep is one ordered step of a [Playbook].
type PlaybookStep struct {
	Order       int    `json:"order"`
	Action      string `json:"action"`
	Description string `json:"description,omitempty"`
	Condition   string `json:"condition,omitempty"`
}

// Playbook is a trigger → steps response (a "reflex" on the wire).
type Playbook struct {
	ID                 string         `json:"id"`
	Trigger            string         `json:"trigger"`
	Statement          string         `json:"statement"`
	Scope              Scope          `json:"context,omitempty"`
	Steps              []PlaybookStep `json:"steps"`
	DerivedFromLessons []string       `json:"derived_from_schemas,omitempty"`
	Confidence         float64        `json:"confidence"`
	DerivedAt          string         `json:"derived_at,omitempty"`
	LastVerified       string         `json:"last_verified,omitempty"`
	Source             string         `json:"source,omitempty"`
	CreatedBy          string         `json:"created_by,omitempty"`
}

// PlaybookQuery selects playbooks for [Client.ListPlaybooks].
type PlaybookQuery struct {
	Trigger string
	Service string
	Limit   int
	Offset  int
}

// PlaybookList is one page of playbooks.
type PlaybookList struct {
	Playbooks []Playbook `json:"reflexes"`
	Total     int        `json:"total"`
	Limit     int        `json:"limit"`
	Offset    int        `json:"offset"`
}

// GlobalSchema is a de-identified generalisation promoted to the shared
// global tier. Its provenance is corroboration counts, never tenant ids.
type GlobalSchema struct {
	ID              string  `json:"id"`
	Statement       string  `json:"statement"`
	Scope           Scope   `json:"context,omitempty"`
	Polarity        string  `json:"polarity,omitempty"`
	DistinctTenants int     `json:"distinct_tenants"`
	EvidenceCount   int     `json:"evidence_count"`
	Confidence      float64 `json:"confidence"`
	Surprise        float64 `json:"surprise,omitempty"`
	HasSurprise     bool    `json:"has_surprise,omitempty"`
	Status          string  `json:"status"`
	PromotedAt      string  `json:"promoted_at"`
	CreatedBy       string  `json:"created_by,omitempty"`
}

// GlobalSchemaQuery selects promoted schemas; Status is "", "pending" or
// "active".
type GlobalSchemaQuery struct {
	Status string
	Limit  int
	Offset int
}

// GlobalSchemaList is one page of promoted schemas.
type GlobalSchemaList struct {
	Schemas []GlobalSchema `json:"schemas"`
	Total   int            `json:"total"`
	Limit   int            `json:"limit"`
	Offset  int            `json:"offset"`
}

// Incident is a recorded failure. The REST incident document keeps its
// original machine-term keys (root_cause_claim_id, timeline_event_ids).
type Incident struct {
	ID               string   `json:"id"`
	Title            string   `json:"title"`
	Summary          string   `json:"summary"`
	Severity         string   `json:"severity"`
	Status           string   `json:"status"`
	TimelineEventIDs []string `json:"timeline_event_ids,omitempty"`
	RootCauseClaimID string   `json:"root_cause_claim_id,omitempty"`
	DecisionIDs      []string `json:"decision_ids,omitempty"`
	OutcomeIDs       []string `json:"outcome_ids,omitempty"`
	PlaybookID       string   `json:"playbook_id,omitempty"`
	OpenedAt         string   `json:"opened_at"`
	ResolvedAt       string   `json:"resolved_at,omitempty"`
	CreatedBy        string   `json:"created_by,omitempty"`
}

// IncidentItem opens an incident. Title is required; ID is generated when
// empty; Severity (critical | high | medium | low) defaults to medium.
type IncidentItem struct {
	ID               string   `json:"id,omitempty"`
	Title            string   `json:"title"`
	Summary          string   `json:"summary,omitempty"`
	Severity         string   `json:"severity,omitempty"`
	RootCauseClaimID string   `json:"root_cause_claim_id,omitempty"`
	DecisionIDs      []string `json:"decision_ids,omitempty"`
	OutcomeIDs       []string `json:"outcome_ids,omitempty"`
	TimelineEventIDs []string `json:"timeline_event_ids,omitempty"`
}

// IncidentQuery selects incidents for [Client.ListIncidents].
type IncidentQuery struct {
	Severity string
	Status   string
	Limit    int
	Offset   int
}

// IncidentList is one page of incidents.
type IncidentList struct {
	Incidents []Incident `json:"incidents"`
	Total     int        `json:"total"`
	Limit     int        `json:"limit"`
	Offset    int        `json:"offset"`
}

// ProvenanceSignal is one weighted component of a claim's credibility score.
type ProvenanceSignal struct {
	Name         string  `json:"name"`
	Value        float64 `json:"value"`
	Weight       float64 `json:"weight"`
	Contribution float64 `json:"contribution"`
	Detail       string  `json:"detail,omitempty"`
}

// ClaimProvenance is a claim's credibility breakdown.
type ClaimProvenance struct {
	ClaimID        string             `json:"belief_id"`
	Statement      string             `json:"statement"`
	Score          float64            `json:"score"`
	Signals        []ProvenanceSignal `json:"signals,omitempty"`
	Rationale      string             `json:"rationale,omitempty"`
	ProseRationale string             `json:"prose_rationale,omitempty"`
	SourceDocument string             `json:"source_document,omitempty"`
	Liveness       string             `json:"liveness,omitempty"`
}

// IncidentAnalysis is the "why were we wrong" post-mortem of an incident.
type IncidentAnalysis struct {
	Incident Incident `json:"incident"`
	// RootClaim is nil when the incident names no root cause or it is not stored.
	RootClaim         *ClaimProvenance `json:"root_belief,omitempty"`
	AffectedDecisions []Decision       `json:"affected_decisions"`
	Explanation       string           `json:"explanation"`
}

func pageValues(limit, offset int) url.Values {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if offset > 0 {
		q.Set("offset", strconv.Itoa(offset))
	}
	return q
}

func setIf(q url.Values, key, value string) {
	if value != "" {
		q.Set(key, value)
	}
}

// ListActions lists recorded actions. GET /v1/actions. A run-scoped token
// sees only its runs; naming another run is a 403 *[APIError].
func (c *Client) ListActions(ctx context.Context, query ActionQuery) (*ActionList, error) {
	q := pageValues(query.Limit, query.Offset)
	setIf(q, "subject", query.Subject)
	setIf(q, "run_id", query.RunID)
	var out ActionList
	if err := c.do(ctx, http.MethodGet, withQuery("/v1/actions", q), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListOutcomes lists recorded outcomes. GET /v1/outcomes.
func (c *Client) ListOutcomes(ctx context.Context, query OutcomeQuery) (*OutcomeList, error) {
	q := pageValues(query.Limit, query.Offset)
	setIf(q, "action_id", query.ActionID)
	var out OutcomeList
	if err := c.do(ctx, http.MethodGet, withQuery("/v1/outcomes", q), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListLessons lists this tenant's lessons. GET /v1/tenant-schemas.
func (c *Client) ListLessons(ctx context.Context, query LessonQuery) (*LessonList, error) {
	q := pageValues(query.Limit, query.Offset)
	setIf(q, "service", query.Service)
	setIf(q, "trigger", query.Trigger)
	var out LessonList
	if err := c.do(ctx, http.MethodGet, withQuery("/v1/tenant-schemas", q), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AppendLessons writes lessons authored elsewhere and returns how many were
// accepted. POST /v1/tenant-schemas; needs claims:write. The batch is
// validated first and an invalid lesson is a 400 *[APIError] with nothing
// written.
func (c *Client) AppendLessons(ctx context.Context, lessons []Lesson) (int, error) {
	var out struct {
		Accepted int `json:"accepted"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/tenant-schemas", map[string]any{"schemas": lessons}, &out); err != nil {
		return 0, err
	}
	return out.Accepted, nil
}

// ListPlaybooks lists this tenant's playbooks. GET /v1/reflexes.
func (c *Client) ListPlaybooks(ctx context.Context, query PlaybookQuery) (*PlaybookList, error) {
	q := pageValues(query.Limit, query.Offset)
	setIf(q, "trigger", query.Trigger)
	setIf(q, "service", query.Service)
	var out PlaybookList
	if err := c.do(ctx, http.MethodGet, withQuery("/v1/reflexes", q), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AppendPlaybooks writes playbooks authored elsewhere and returns how many
// were accepted. POST /v1/reflexes; needs claims:write; validated like
// [Client.AppendLessons].
func (c *Client) AppendPlaybooks(ctx context.Context, playbooks []Playbook) (int, error) {
	var out struct {
		Accepted int `json:"accepted"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/reflexes", map[string]any{"reflexes": playbooks}, &out); err != nil {
		return 0, err
	}
	return out.Accepted, nil
}

// ListGlobalSchemas lists the schemas promoted to the shared global tier.
// GET /v1/schemas.
func (c *Client) ListGlobalSchemas(ctx context.Context, query GlobalSchemaQuery) (*GlobalSchemaList, error) {
	q := pageValues(query.Limit, query.Offset)
	setIf(q, "status", query.Status)
	var out GlobalSchemaList
	if err := c.do(ctx, http.MethodGet, withQuery("/v1/schemas", q), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// OpenIncident records an incident and returns it as stored. POST
// /v1/incidents; needs claims:write. The author is the token's subject.
func (c *Client) OpenIncident(ctx context.Context, item IncidentItem) (*Incident, error) {
	var out Incident
	if err := c.do(ctx, http.MethodPost, "/v1/incidents", item, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListIncidents lists incidents. GET /v1/incidents.
func (c *Client) ListIncidents(ctx context.Context, query IncidentQuery) (*IncidentList, error) {
	q := pageValues(query.Limit, query.Offset)
	setIf(q, "severity", query.Severity)
	setIf(q, "status", query.Status)
	var out IncidentList
	if err := c.do(ctx, http.MethodGet, withQuery("/v1/incidents", q), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetIncident fetches one incident, or nil on 404. GET /v1/incidents/{id}.
func (c *Client) GetIncident(ctx context.Context, id string) (*Incident, error) {
	var out Incident
	if err := c.do(ctx, http.MethodGet, "/v1/incidents/"+url.PathEscape(id), nil, &out); err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &out, nil
}

// ResolveIncident marks an incident resolved. POST
// /v1/incidents/{id}/resolve; needs claims:write. An unknown id is a 404
// *[APIError] and nothing is written.
func (c *Client) ResolveIncident(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/v1/incidents/"+url.PathEscape(id)+"/resolve", nil, nil)
}

// WhyWereWeWrong returns the post-incident analysis. GET
// /v1/incidents/{id}/why-wrong. An unknown id is a 404 *[APIError].
func (c *Client) WhyWereWeWrong(ctx context.Context, incidentID string) (*IncidentAnalysis, error) {
	var out IncidentAnalysis
	if err := c.do(ctx, http.MethodGet, "/v1/incidents/"+url.PathEscape(incidentID)+"/why-wrong", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
