package main

import (
	"context"
	"errors"

	mcp "go.klarlabs.de/mcp"
	"go.klarlabs.de/mcp/protocol"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/incident"
	"go.klarlabs.de/mnemos/internal/skills"
)

// Skill-record listings and incident tools. Each handler translates its input
// and calls internal/skills or internal/incident — the implementation REST,
// gRPC and the Go library share — and returns the same wire DTOs REST does.

type mcpPage struct {
	Limit  int `json:"limit,omitempty" jsonschema:"description=Page size (default 50, max 200)"`
	Offset int `json:"offset,omitempty" jsonschema:"description=Records to skip"`
}

type mcpListActionsInput struct {
	Subject string `json:"subject,omitempty" jsonschema:"description=Only actions on this subject (service, component, channel)"`
	RunID   string `json:"run_id,omitempty" jsonschema:"description=Only actions recorded under this run"`
	mcpPage
}

type mcpListActionsOutput struct {
	Actions []actionDTO `json:"actions"`
	Total   int         `json:"total"`
	Limit   int         `json:"limit"`
	Offset  int         `json:"offset"`
}

type mcpListOutcomesInput struct {
	ActionID string `json:"action_id,omitempty" jsonschema:"description=Only outcomes of this action"`
	mcpPage
}

type mcpListOutcomesOutput struct {
	Outcomes []outcomeDTO `json:"outcomes"`
	Total    int          `json:"total"`
	Limit    int          `json:"limit"`
	Offset   int          `json:"offset"`
}

type mcpListGlobalSchemasInput struct {
	Status string `json:"status,omitempty" jsonschema:"description=pending or active; empty lists both"`
	mcpPage
}

type mcpListGlobalSchemasOutput struct {
	Schemas []globalSchemaDTO `json:"schemas"`
	Total   int               `json:"total"`
	Limit   int               `json:"limit"`
	Offset  int               `json:"offset"`
}

// mcpIncidentDTO is the brain-native wire form of an incident on MCP.
type mcpIncidentDTO struct {
	ID                 string   `json:"id"`
	Title              string   `json:"title"`
	Summary            string   `json:"summary,omitempty"`
	Severity           string   `json:"severity"`
	Status             string   `json:"status"`
	RootCauseBeliefID  string   `json:"root_cause_belief_id,omitempty"`
	DecisionIDs        []string `json:"decision_ids,omitempty"`
	OutcomeIDs         []string `json:"outcome_ids,omitempty"`
	TimelineEpisodeIDs []string `json:"timeline_episode_ids,omitempty"`
	ReflexID           string   `json:"reflex_id,omitempty"`
	OpenedAt           string   `json:"opened_at,omitempty"`
	ResolvedAt         string   `json:"resolved_at,omitempty"`
	CreatedBy          string   `json:"created_by,omitempty"`
}

func mcpIncidentToDTO(i domain.Incident) mcpIncidentDTO {
	return mcpIncidentDTO{
		ID: i.ID, Title: i.Title, Summary: i.Summary, Severity: string(i.Severity), Status: string(i.Status),
		RootCauseBeliefID: i.RootCauseClaimID, DecisionIDs: i.DecisionIDs, OutcomeIDs: i.OutcomeIDs,
		TimelineEpisodeIDs: i.TimelineEventIDs, ReflexID: i.PlaybookID,
		OpenedAt: rfc3339(i.OpenedAt), ResolvedAt: rfc3339(i.ResolvedAt), CreatedBy: i.CreatedBy,
	}
}

type mcpOpenIncidentInput struct {
	ID                 string   `json:"id,omitempty" jsonschema:"description=Stable id; generated when empty"`
	Title              string   `json:"title" jsonschema:"required,description=Short label for the incident"`
	Summary            string   `json:"summary,omitempty" jsonschema:"description=What happened and what was affected"`
	Severity           string   `json:"severity,omitempty" jsonschema:"description=critical, high, medium (default) or low"`
	RootCauseBeliefID  string   `json:"root_cause_belief_id,omitempty" jsonschema:"description=The belief that turned out to be wrong"`
	DecisionIDs        []string `json:"decision_ids,omitempty" jsonschema:"description=Decisions made on that belief"`
	OutcomeIDs         []string `json:"outcome_ids,omitempty" jsonschema:"description=Outcomes that proved it wrong"`
	TimelineEpisodeIDs []string `json:"timeline_episode_ids,omitempty" jsonschema:"description=Episodes forming the incident timeline"`
}

type mcpListIncidentsInput struct {
	Severity string `json:"severity,omitempty" jsonschema:"description=critical, high, medium or low"`
	Status   string `json:"status,omitempty" jsonschema:"description=open, resolved or postmortem"`
	mcpPage
}

type mcpListIncidentsOutput struct {
	Incidents []mcpIncidentDTO `json:"incidents"`
	Total     int              `json:"total"`
	Limit     int              `json:"limit"`
	Offset    int              `json:"offset"`
}

type mcpIncidentIDInput struct {
	ID string `json:"id" jsonschema:"required,description=Incident id"`
}

type mcpWhyWereWeWrongInput struct {
	IncidentID string `json:"incident_id" jsonschema:"required,description=Incident to analyse"`
}

type mcpWhyWereWeWrongOutput struct {
	Incident          mcpIncidentDTO       `json:"incident"`
	RootBelief        *beliefProvenanceDTO `json:"root_belief,omitempty"`
	AffectedDecisions []decisionDTO        `json:"affected_decisions"`
	Explanation       string               `json:"explanation"`
}

// mcpRunScope is the token's run allowlist as an internal/skills predicate,
// nil for stdio and unrestricted tokens.
func mcpRunScope(ctx context.Context) skills.RunScope {
	if !mcpRunRestricted(ctx) {
		return nil
	}
	return func(runID string) bool { return mcpRunAllowed(ctx, runID) }
}

// mcpSkillsError turns a caller-correctable internal/skills error into an
// invalid-params error rather than an opaque internal one.
func mcpSkillsError(err error) error {
	if errors.Is(err, skills.ErrInvalidRecord) || errors.Is(err, skills.ErrInvalidFilter) || errors.Is(err, skills.ErrRunNotAllowed) {
		return userInputError(err)
	}
	return err
}

func mcpIncidentError(err error) error {
	switch {
	case errors.Is(err, incident.ErrNotFound):
		return protocol.NewNotFound(err.Error())
	case errors.Is(err, incident.ErrInvalid):
		return userInputError(err)
	}
	return err
}

func mcpRunListActions(ctx context.Context, in mcpListActionsInput) (mcpListActionsOutput, error) {
	conn, err := openConn(ctx)
	if err != nil {
		return mcpListActionsOutput{}, err
	}
	defer closeConn(conn)
	got, err := skills.Actions(ctx, conn.Actions, skills.ActionFilter{Subject: in.Subject, RunID: in.RunID, Runs: mcpRunScope(ctx)})
	if err != nil {
		return mcpListActionsOutput{}, mcpSkillsError(err)
	}
	limit, offset := skills.Page(in.Limit, in.Offset)
	return mcpListActionsOutput{
		Actions: mapDTO(skills.Window(got, limit, offset), actionToDTO), Total: len(got), Limit: limit, Offset: offset,
	}, nil
}

func mcpRunListOutcomes(ctx context.Context, in mcpListOutcomesInput) (mcpListOutcomesOutput, error) {
	conn, err := openConn(ctx)
	if err != nil {
		return mcpListOutcomesOutput{}, err
	}
	defer closeConn(conn)
	got, err := skills.Outcomes(ctx, conn.Outcomes, conn.Actions, skills.OutcomeFilter{ActionID: in.ActionID, Runs: mcpRunScope(ctx)})
	if err != nil {
		return mcpListOutcomesOutput{}, mcpSkillsError(err)
	}
	limit, offset := skills.Page(in.Limit, in.Offset)
	return mcpListOutcomesOutput{
		Outcomes: mapDTO(skills.Window(got, limit, offset), outcomeToDTO), Total: len(got), Limit: limit, Offset: offset,
	}, nil
}

// mcpRunListGlobalSchemas reads the promoted store from the BASE connection:
// it is deliberately cross-tenant (ADR 0011/0012), exactly as REST /v1/schemas
// and gRPC ListGlobalSchemas read it.
func mcpRunListGlobalSchemas(ctx context.Context, in mcpListGlobalSchemasInput) (mcpListGlobalSchemasOutput, error) {
	conn, err := openBaseConn(ctx)
	if err != nil {
		return mcpListGlobalSchemasOutput{}, err
	}
	defer func() { _ = conn.Close() }()
	got, err := skills.GlobalSchemas(ctx, conn.GlobalSchemas, in.Status)
	if err != nil {
		return mcpListGlobalSchemasOutput{}, mcpSkillsError(err)
	}
	limit, offset := skills.Page(in.Limit, in.Offset)
	return mcpListGlobalSchemasOutput{
		Schemas: mapDTO(skills.Window(got, limit, offset), globalSchemaToDTO), Total: len(got), Limit: limit, Offset: offset,
	}, nil
}

func mcpRunOpenIncident(ctx context.Context, actor string, in mcpOpenIncidentInput) (mcpIncidentDTO, error) {
	w, err := openWriter(ctx)
	if err != nil {
		return mcpIncidentDTO{}, err
	}
	defer closeWriter(w)
	inc, err := incident.Open(ctx, w.Conn().Incidents, w.Incident, incident.OpenRequest{
		ID: in.ID, Title: in.Title, Summary: in.Summary, Severity: in.Severity,
		RootCauseClaimID: in.RootCauseBeliefID, DecisionIDs: in.DecisionIDs,
		OutcomeIDs: in.OutcomeIDs, TimelineEventIDs: in.TimelineEpisodeIDs, Actor: actor,
	})
	if err != nil {
		return mcpIncidentDTO{}, mcpIncidentError(err)
	}
	return mcpIncidentToDTO(inc), nil
}

func mcpRunListIncidents(ctx context.Context, in mcpListIncidentsInput) (mcpListIncidentsOutput, error) {
	conn, err := openConn(ctx)
	if err != nil {
		return mcpListIncidentsOutput{}, err
	}
	defer closeConn(conn)
	got, err := incident.List(ctx, conn.Incidents, incident.Filter{Severity: in.Severity, Status: in.Status})
	if err != nil {
		return mcpListIncidentsOutput{}, mcpIncidentError(err)
	}
	limit, offset := skills.Page(in.Limit, in.Offset)
	return mcpListIncidentsOutput{
		Incidents: mapDTO(skills.Window(got, limit, offset), mcpIncidentToDTO), Total: len(got), Limit: limit, Offset: offset,
	}, nil
}

func mcpRunGetIncident(ctx context.Context, in mcpIncidentIDInput) (mcpIncidentDTO, error) {
	conn, err := openConn(ctx)
	if err != nil {
		return mcpIncidentDTO{}, err
	}
	defer closeConn(conn)
	inc, err := incident.Get(ctx, conn.Incidents, in.ID)
	if err != nil {
		return mcpIncidentDTO{}, mcpIncidentError(err)
	}
	return mcpIncidentToDTO(inc), nil
}

func mcpRunResolveIncident(ctx context.Context, in mcpIncidentIDInput) (mcpIncidentDTO, error) {
	w, err := openWriter(ctx)
	if err != nil {
		return mcpIncidentDTO{}, err
	}
	defer closeWriter(w)
	inc, err := incident.Resolve(ctx, w.Conn().Incidents, w.ResolveIncident, in.ID)
	if err != nil {
		return mcpIncidentDTO{}, mcpIncidentError(err)
	}
	return mcpIncidentToDTO(inc), nil
}

func mcpRunWhyWereWeWrong(ctx context.Context, in mcpWhyWereWeWrongInput) (mcpWhyWereWeWrongOutput, error) {
	conn, err := openConn(ctx)
	if err != nil {
		return mcpWhyWereWeWrongOutput{}, err
	}
	defer closeConn(conn)
	rep, err := incident.WhyWereWeWrong(ctx, conn, in.IncidentID)
	if err != nil {
		return mcpWhyWereWeWrongOutput{}, mcpIncidentError(err)
	}
	dto := whyWrongToDTO(rep)
	return mcpWhyWereWeWrongOutput{
		Incident: mcpIncidentToDTO(rep.Incident), RootBelief: dto.RootBelief,
		AffectedDecisions: dto.AffectedDecisions, Explanation: dto.Explanation,
	}, nil
}

func mapDTO[T, D any](xs []T, f func(T) D) []D {
	out := make([]D, 0, len(xs))
	for _, x := range xs {
		out = append(out, f(x))
	}
	return out
}

// registerSkillTools registers the skill-record listing and incident tools.
// Every name here needs an mcpToolScopes entry (TestMCPToolScopes_CoversEveryRegisteredTool).
func registerSkillTools(srv *mcp.Server, mcpActor string) {
	srv.Tool("list_actions").
		Description("List recorded operational actions (deploys, rollbacks, ...), newest data as stored, optionally by subject and/or run. A run-scoped token sees only its runs. Paged: limit (default 50, max 200) and offset; total counts every match. Mirrors GET /v1/actions.").
		OutputSchema(mcpListActionsOutput{}).
		Handler(func(ctx context.Context, in mcpListActionsInput) (mcpListActionsOutput, error) {
			return mcpRunListActions(ctx, in)
		})

	srv.Tool("list_outcomes").
		Description("List recorded outcomes of actions, optionally for one action. An outcome belongs to its action's run, so a run-scoped token sees only outcomes of its runs' actions. Paged like list_actions. Mirrors GET /v1/outcomes.").
		OutputSchema(mcpListOutcomesOutput{}).
		Handler(func(ctx context.Context, in mcpListOutcomesInput) (mcpListOutcomesOutput, error) {
			return mcpRunListOutcomes(ctx, in)
		})

	srv.Tool("list_global_schemas").
		Description("List schemas promoted to the shared global tier (de-identified generalisations corroborated across tenants), optionally by status (pending | active). Not this tenant's schemas: query_schemas lists those. Mirrors GET /v1/schemas.").
		OutputSchema(mcpListGlobalSchemasOutput{}).
		Handler(func(ctx context.Context, in mcpListGlobalSchemasInput) (mcpListGlobalSchemasOutput, error) {
			return mcpRunListGlobalSchemas(ctx, in)
		})

	srv.Tool("open_incident").
		Description("Open an incident: what failed, how badly, and optionally the belief that turned out to be wrong and the decisions and outcomes linked to it. Attributed to the caller. Mirrors POST /v1/incidents.").
		OutputSchema(mcpIncidentDTO{}).
		Handler(func(ctx context.Context, in mcpOpenIncidentInput) (mcpIncidentDTO, error) {
			return mcpRunOpenIncident(ctx, mcpActorFor(ctx, mcpActor), in)
		})

	srv.Tool("list_incidents").
		Description("List incidents, optionally by severity and/or status. Paged like list_actions. Mirrors GET /v1/incidents.").
		OutputSchema(mcpListIncidentsOutput{}).
		Handler(func(ctx context.Context, in mcpListIncidentsInput) (mcpListIncidentsOutput, error) {
			return mcpRunListIncidents(ctx, in)
		})

	srv.Tool("get_incident").
		Description("Fetch one incident by id. Mirrors GET /v1/incidents/{id}.").
		OutputSchema(mcpIncidentDTO{}).
		Handler(func(ctx context.Context, in mcpIncidentIDInput) (mcpIncidentDTO, error) {
			return mcpRunGetIncident(ctx, in)
		})

	srv.Tool("resolve_incident").
		Description("Mark an incident resolved. An unknown id is refused and nothing is written. Mirrors POST /v1/incidents/{id}/resolve.").
		OutputSchema(mcpIncidentDTO{}).
		Handler(func(ctx context.Context, in mcpIncidentIDInput) (mcpIncidentDTO, error) {
			return mcpRunResolveIncident(ctx, in)
		})

	srv.Tool("why_were_we_wrong").
		Description("Post-incident analysis: the incident, the provenance of the belief that turned out to be wrong, and the decisions built on it, with a plain-English explanation. Mirrors GET /v1/incidents/{id}/why-wrong.").
		OutputSchema(mcpWhyWereWeWrongOutput{}).
		Handler(func(ctx context.Context, in mcpWhyWereWeWrongInput) (mcpWhyWereWeWrongOutput, error) {
			return mcpRunWhyWereWeWrong(ctx, in)
		})
}
