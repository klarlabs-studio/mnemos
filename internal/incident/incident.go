// Package incident is the one call path from a delivery surface to incident
// management and the "why were we wrong" post-mortem.
//
// The writes themselves are govwrite's (Writer.Incident / ResolveIncident on
// the daemon surfaces, ApplyIncident / ApplyResolveIncident inside the
// library's governed kernel). What every surface also has to agree on — and
// REST used to decide alone — is what happens around them:
//
//   - an opened incident gets a generated id when none is given, defaults to
//     medium severity, starts open, is validated before the write
//     ([ErrInvalid]) and is attributed to the caller's actor;
//   - list filters are checked against the severity and status vocabularies;
//   - reading, resolving or analysing an id that does not exist is
//     [ErrNotFound], never a write or an internal error.
//
// Sentinel errors rather than message matching are what let REST answer
// 400/404 and gRPC InvalidArgument/NotFound consistently.
package incident

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/clock"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/query"
	"go.klarlabs.de/mnemos/internal/store"
)

// ErrNotFound is returned when the incident does not exist.
var ErrNotFound = errors.New("incident not found")

// errNotWired reports a connection built without an incident repository.
// Every backend provides one (memory, sqlite, libSQL, Postgres, MySQL), so this
// is a construction bug, never a property of the backend.
var errNotWired = errors.New("incident repository not wired on this connection")

// ErrInvalid is returned for a request that fails validation.
var ErrInvalid = errors.New("invalid incident request")

// WriteFunc persists an incident through the governed write path.
type WriteFunc func(ctx context.Context, inc domain.Incident) (string, error)

// ResolveFunc marks an incident resolved through the governed write path.
type ResolveFunc func(ctx context.Context, id string, at time.Time) error

// OpenRequest is one surface's request to open an incident, already
// translated from its own input.
type OpenRequest struct {
	ID               string
	Title            string
	Summary          string
	Severity         string
	RootCauseClaimID string
	DecisionIDs      []string
	OutcomeIDs       []string
	TimelineEventIDs []string
	// Actor is recorded as CreatedBy. It comes from the authenticated
	// caller, never from the request body.
	Actor string
}

// Open validates req, opens the incident through write, and returns the
// incident as written.
func Open(ctx context.Context, repo ports.IncidentRepository, write WriteFunc, req OpenRequest) (domain.Incident, error) {
	if repo == nil {
		return domain.Incident{}, errNotWired
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		gen, err := newID()
		if err != nil {
			return domain.Incident{}, fmt.Errorf("generate incident id: %w", err)
		}
		id = gen
	}
	severity := domain.IncidentSeverity(strings.TrimSpace(req.Severity))
	if severity == "" {
		severity = domain.IncidentSeverityMedium
	}
	inc := domain.Incident{
		ID:               id,
		Title:            strings.TrimSpace(req.Title),
		Summary:          req.Summary,
		Severity:         severity,
		Status:           domain.IncidentStatusOpen,
		RootCauseClaimID: strings.TrimSpace(req.RootCauseClaimID),
		DecisionIDs:      req.DecisionIDs,
		OutcomeIDs:       req.OutcomeIDs,
		TimelineEventIDs: req.TimelineEventIDs,
		OpenedAt:         clock.Now().UTC(),
		CreatedBy:        req.Actor,
	}
	if err := inc.Validate(); err != nil {
		return domain.Incident{}, fmt.Errorf("%v: %w", err, ErrInvalid)
	}
	if _, err := write(ctx, inc); err != nil {
		return domain.Incident{}, fmt.Errorf("open incident: %w", err)
	}
	return inc, nil
}

// Filter selects incidents. Both fields are optional; when both are set an
// incident must match both.
type Filter struct {
	Severity string
	Status   string
}

// List returns the incidents matching f.
func List(ctx context.Context, repo ports.IncidentRepository, f Filter) ([]domain.Incident, error) {
	if repo == nil {
		return nil, errNotWired
	}
	if err := checkFilter(f); err != nil {
		return nil, err
	}
	var (
		incidents []domain.Incident
		err       error
	)
	switch {
	case f.Severity != "":
		incidents, err = repo.ListBySeverity(ctx, domain.IncidentSeverity(f.Severity))
	case f.Status != "":
		incidents, err = repo.ListByStatus(ctx, domain.IncidentStatus(f.Status))
	default:
		incidents, err = repo.ListAll(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("list incidents: %w", err)
	}
	if f.Severity == "" || f.Status == "" {
		return incidents, nil
	}
	out := incidents[:0:0]
	for _, inc := range incidents {
		if string(inc.Status) == f.Status {
			out = append(out, inc)
		}
	}
	return out, nil
}

// Get returns one incident.
func Get(ctx context.Context, repo ports.IncidentRepository, id string) (domain.Incident, error) {
	if repo == nil {
		return domain.Incident{}, errNotWired
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return domain.Incident{}, fmt.Errorf("incident id is required: %w", ErrInvalid)
	}
	inc, found, err := repo.GetByID(ctx, id)
	if err != nil {
		return domain.Incident{}, fmt.Errorf("get incident %s: %w", id, err)
	}
	if !found {
		return domain.Incident{}, fmt.Errorf("incident %q: %w", id, ErrNotFound)
	}
	return inc, nil
}

// Resolve marks an existing incident resolved now and returns it as stored
// afterwards. An unknown id is ErrNotFound and writes nothing.
func Resolve(ctx context.Context, repo ports.IncidentRepository, resolve ResolveFunc, id string) (domain.Incident, error) {
	inc, err := Get(ctx, repo, id)
	if err != nil {
		return domain.Incident{}, err
	}
	if err := resolve(ctx, inc.ID, clock.Now().UTC()); err != nil {
		return domain.Incident{}, fmt.Errorf("resolve incident %s: %w", inc.ID, err)
	}
	return Get(ctx, repo, inc.ID)
}

// WhyWereWeWrong runs the post-incident analysis over conn: the incident,
// its root-cause belief's provenance, and the decisions built on it.
func WhyWereWeWrong(ctx context.Context, conn *store.Conn, id string) (query.WhyWereWeWrongReport, error) {
	if conn.Incidents == nil {
		return query.WhyWereWeWrongReport{}, errNotWired
	}
	inc, err := Get(ctx, conn.Incidents, id)
	if err != nil {
		return query.WhyWereWeWrongReport{}, err
	}
	eng := query.NewEngine(conn.Events, conn.Claims, conn.Relationships).
		WithDecisions(conn.Decisions).
		WithIncidents(conn.Incidents)
	report, err := eng.WhyWereWeWrong(ctx, inc.ID)
	if err != nil {
		return query.WhyWereWeWrongReport{}, fmt.Errorf("why were we wrong: %w", err)
	}
	return report, nil
}

func checkFilter(f Filter) error {
	switch domain.IncidentSeverity(f.Severity) {
	case "", domain.IncidentSeverityCritical, domain.IncidentSeverityHigh, domain.IncidentSeverityMedium, domain.IncidentSeverityLow:
	default:
		return fmt.Errorf("severity %q must be critical, high, medium or low: %w", f.Severity, ErrInvalid)
	}
	switch domain.IncidentStatus(f.Status) {
	case "", domain.IncidentStatusOpen, domain.IncidentStatusResolved, domain.IncidentStatusPostmortem:
	default:
		return fmt.Errorf("status %q must be open, resolved or postmortem: %w", f.Status, ErrInvalid)
	}
	return nil
}

func newID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "inc_" + hex.EncodeToString(buf), nil
}
