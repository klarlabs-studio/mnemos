package mnemos

import (
	"context"
	"fmt"
	"time"

	axidomain "go.klarlabs.de/axi/domain"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/incident"
	"go.klarlabs.de/mnemos/internal/query"
	"go.klarlabs.de/mnemos/internal/skills"
)

// ErrIncidentNotFound is returned (wrapped) when an incident id does not exist.
var ErrIncidentNotFound = incident.ErrNotFound

// ErrInvalidIncident is returned (wrapped) for an incident request that fails
// validation: a blank title, an unknown severity or status.
var ErrInvalidIncident = incident.ErrInvalid

// actionOpenIncident / actionResolveIncident map to the axi actions
// mnemos-open-incident / mnemos-resolve-incident.
const (
	actionOpenIncident    = "open_incident"
	actionResolveIncident = "resolve_incident"
)

// Incident is a recorded failure: what happened, the claim that turned out to
// be wrong, and the decisions and outcomes linked to it.
type Incident struct {
	ID               string
	Title            string
	Summary          string
	Severity         string // critical | high | medium | low
	Status           string // open | resolved | postmortem
	RootCauseClaimID string
	DecisionIDs      []string
	OutcomeIDs       []string
	TimelineEventIDs []string
	PlaybookID       string
	OpenedAt         time.Time
	ResolvedAt       time.Time
	CreatedBy        string
}

// IncidentItem opens an incident via [Memory.OpenIncident]. Title is
// required; ID is generated when empty; Severity defaults to "medium".
type IncidentItem struct {
	ID               string
	Title            string
	Summary          string
	Severity         string
	RootCauseClaimID string
	DecisionIDs      []string
	OutcomeIDs       []string
	TimelineEventIDs []string
}

// IncidentQuery selects incidents for [Memory.ListIncidents]. Severity and
// Status are optional and must both match when set. Limit 0 means no cap.
type IncidentQuery struct {
	Severity string
	Status   string
	Limit    int
	Offset   int
}

// ProvenanceSignal is one weighted component of a claim's credibility score.
type ProvenanceSignal struct {
	Name         string
	Value        float64
	Weight       float64
	Contribution float64
	Detail       string
}

// ClaimProvenance is a claim's credibility breakdown.
type ClaimProvenance struct {
	ClaimID        string
	ClaimText      string
	Score          float64
	Signals        []ProvenanceSignal
	Rationale      string
	ProseRationale string
	SourceDocument string
	Liveness       string
}

// IncidentAnalysis is the "why were we wrong" post-mortem of an incident.
type IncidentAnalysis struct {
	Incident Incident
	// RootClaim is the root-cause claim's provenance; nil when the incident
	// names none or the claim is not in the store.
	RootClaim *ClaimProvenance
	// AffectedDecisions are the decisions linked to the incident.
	AffectedDecisions []Decision
	Explanation       string
}

type openIncidentInput struct{ Incident domain.Incident }

type resolveIncidentInput struct {
	IncidentID string
	ResolvedAt time.Time
}

// OpenIncident implements [Memory.OpenIncident].
func (m *memory) OpenIncident(ctx context.Context, item IncidentItem) (Incident, error) {
	write := func(ctx context.Context, inc domain.Incident) (string, error) {
		return dispatchWrite[string](ctx, m, actionOpenIncident, openIncidentInput{Incident: inc})
	}
	inc, err := incident.Open(ctx, m.conn.Incidents, write, incident.OpenRequest{
		ID: item.ID, Title: item.Title, Summary: item.Summary, Severity: item.Severity,
		RootCauseClaimID: item.RootCauseClaimID, DecisionIDs: item.DecisionIDs,
		OutcomeIDs: item.OutcomeIDs, TimelineEventIDs: item.TimelineEventIDs, Actor: m.actorID,
	})
	if err != nil {
		return Incident{}, fmt.Errorf("mnemos: OpenIncident: %w", err)
	}
	return toPublicIncident(inc), nil
}

// ListIncidents implements [Memory.ListIncidents].
func (m *memory) ListIncidents(ctx context.Context, q IncidentQuery) ([]Incident, error) {
	got, err := incident.List(ctx, m.conn.Incidents, incident.Filter{Severity: q.Severity, Status: q.Status})
	if err != nil {
		return nil, fmt.Errorf("mnemos: ListIncidents: %w", err)
	}
	return mapSlice(skills.Window(got, q.Limit, q.Offset), toPublicIncident), nil
}

// GetIncident implements [Memory.GetIncident].
func (m *memory) GetIncident(ctx context.Context, id string) (Incident, error) {
	inc, err := incident.Get(ctx, m.conn.Incidents, id)
	if err != nil {
		return Incident{}, fmt.Errorf("mnemos: GetIncident: %w", err)
	}
	return toPublicIncident(inc), nil
}

// ResolveIncident implements [Memory.ResolveIncident].
func (m *memory) ResolveIncident(ctx context.Context, id string) (Incident, error) {
	resolve := func(ctx context.Context, id string, at time.Time) error {
		_, err := dispatchWrite[string](ctx, m, actionResolveIncident, resolveIncidentInput{IncidentID: id, ResolvedAt: at})
		return err
	}
	inc, err := incident.Resolve(ctx, m.conn.Incidents, resolve, id)
	if err != nil {
		return Incident{}, fmt.Errorf("mnemos: ResolveIncident: %w", err)
	}
	return toPublicIncident(inc), nil
}

// WhyWereWeWrong implements [Memory.WhyWereWeWrong].
func (m *memory) WhyWereWeWrong(ctx context.Context, incidentID string) (IncidentAnalysis, error) {
	rep, err := incident.WhyWereWeWrong(ctx, m.conn, incidentID)
	if err != nil {
		return IncidentAnalysis{}, fmt.Errorf("mnemos: WhyWereWeWrong: %w", err)
	}
	return toPublicIncidentAnalysis(rep), nil
}

type openIncidentExecutor struct{ m *memory }

// Execute upserts the incident through govwrite's shared implementation and
// records it on the evidence chain.
func (e openIncidentExecutor) Execute(ctx context.Context, input any, _ axidomain.CapabilityInvoker) (axidomain.ExecutionResult, []axidomain.EvidenceRecord, error) {
	in, ok := writeInput[openIncidentInput](input)
	if !ok {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("open_incident: unexpected input type %T", input)
	}
	if err := govwrite.ApplyIncident(ctx, e.m.conn, in.Incident); err != nil {
		return axidomain.ExecutionResult{}, nil, err
	}
	return axidomain.ExecutionResult{
		Data:    in.Incident.ID,
		Summary: fmt.Sprintf("opened incident %s", in.Incident.ID),
	}, []axidomain.EvidenceRecord{{Kind: "mnemos.open_incident", Source: evidenceSourceLibrary, Value: map[string]any{
		"incident_id": in.Incident.ID, "severity": string(in.Incident.Severity), "created_by": in.Incident.CreatedBy,
	}}}, nil
}

type resolveIncidentExecutor struct{ m *memory }

// Execute marks the incident resolved through govwrite's shared
// implementation and records it on the evidence chain.
func (e resolveIncidentExecutor) Execute(ctx context.Context, input any, _ axidomain.CapabilityInvoker) (axidomain.ExecutionResult, []axidomain.EvidenceRecord, error) {
	in, ok := writeInput[resolveIncidentInput](input)
	if !ok {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("resolve_incident: unexpected input type %T", input)
	}
	if err := govwrite.ApplyResolveIncident(ctx, e.m.conn, in.IncidentID, in.ResolvedAt); err != nil {
		return axidomain.ExecutionResult{}, nil, err
	}
	return axidomain.ExecutionResult{
		Data:    in.IncidentID,
		Summary: fmt.Sprintf("resolved incident %s", in.IncidentID),
	}, []axidomain.EvidenceRecord{{Kind: "mnemos.resolve_incident", Source: evidenceSourceLibrary, Value: map[string]any{
		"incident_id": in.IncidentID,
	}}}, nil
}

func toPublicIncident(i domain.Incident) Incident {
	return Incident{
		ID: i.ID, Title: i.Title, Summary: i.Summary, Severity: string(i.Severity), Status: string(i.Status),
		RootCauseClaimID: i.RootCauseClaimID, DecisionIDs: i.DecisionIDs, OutcomeIDs: i.OutcomeIDs,
		TimelineEventIDs: i.TimelineEventIDs, PlaybookID: i.PlaybookID,
		OpenedAt: i.OpenedAt, ResolvedAt: i.ResolvedAt, CreatedBy: i.CreatedBy,
	}
}

func toPublicIncidentAnalysis(r query.WhyWereWeWrongReport) IncidentAnalysis {
	out := IncidentAnalysis{
		Incident:          toPublicIncident(r.Incident),
		AffectedDecisions: mapSlice(r.AffectedDecisions, toPublicDecision),
		Explanation:       r.Explanation,
	}
	if p := r.RootClaim; p != nil {
		prov := &ClaimProvenance{
			ClaimID: p.ClaimID, ClaimText: p.ClaimText, Score: p.Score, Rationale: p.Rationale,
			ProseRationale: p.ProseRationale, SourceDocument: p.SourceDocument, Liveness: string(p.Liveness),
		}
		for _, s := range p.Signals {
			prov.Signals = append(prov.Signals, ProvenanceSignal{
				Name: s.Name, Value: s.Value, Weight: s.Weight, Contribution: s.Contribution, Detail: s.Detail,
			})
		}
		out.RootClaim = prov
	}
	return out
}
