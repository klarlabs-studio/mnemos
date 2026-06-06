package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// IncidentRepository provides SQLite-backed storage for incidents.
// All mutations use INSERT OR REPLACE (upsert-by-id) so re-opening an
// incident with updated evidence rewrites the row atomically.
type IncidentRepository struct {
	db *sql.DB
}

// NewIncidentRepository returns an IncidentRepository backed by db.
func NewIncidentRepository(db *sql.DB) IncidentRepository {
	return IncidentRepository{db: db}
}

// Upsert creates or replaces the incident row by ID.
func (r IncidentRepository) Upsert(ctx context.Context, incident domain.Incident) error {
	if err := incident.Validate(); err != nil {
		return fmt.Errorf("invalid incident: %w", err)
	}

	timelineJSON, err := json.Marshal(incident.TimelineEventIDs)
	if err != nil {
		return fmt.Errorf("marshal timeline_event_ids: %w", err)
	}
	decisionJSON, err := json.Marshal(incident.DecisionIDs)
	if err != nil {
		return fmt.Errorf("marshal decision_ids: %w", err)
	}
	outcomeJSON, err := json.Marshal(incident.OutcomeIDs)
	if err != nil {
		return fmt.Errorf("marshal outcome_ids: %w", err)
	}

	resolvedAt := ""
	if !incident.ResolvedAt.IsZero() {
		resolvedAt = incident.ResolvedAt.UTC().Format(time.RFC3339Nano)
	}

	const q = `
INSERT OR REPLACE INTO incidents (
	id, title, summary, severity, status,
	timeline_event_ids_json, root_cause_claim_id,
	decision_ids_json, outcome_ids_json, playbook_id,
	opened_at, resolved_at, created_by
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	_, err = r.db.ExecContext(ctx, q,
		incident.ID,
		incident.Title,
		incident.Summary,
		string(incident.Severity),
		string(incident.Status),
		string(timelineJSON),
		incident.RootCauseClaimID,
		string(decisionJSON),
		string(outcomeJSON),
		incident.PlaybookID,
		incident.OpenedAt.UTC().Format(time.RFC3339Nano),
		resolvedAt,
		actorOr(incident.CreatedBy),
	)
	if err != nil {
		return fmt.Errorf("upsert incident: %w", err)
	}
	return nil
}

// GetByID returns the incident with the given ID.
// Returns (Incident{}, false, nil) when no row exists.
func (r IncidentRepository) GetByID(ctx context.Context, id string) (domain.Incident, bool, error) {
	const q = `
SELECT id, title, summary, severity, status,
       timeline_event_ids_json, root_cause_claim_id,
       decision_ids_json, outcome_ids_json, playbook_id,
       opened_at, resolved_at, created_by
FROM incidents WHERE id = ?`

	row := r.db.QueryRowContext(ctx, q, id)
	inc, err := scanIncident(row)
	if err == errNoRows {
		return domain.Incident{}, false, nil
	}
	if err != nil {
		return domain.Incident{}, false, err
	}
	return inc, true, nil
}

// ListAll returns every incident ordered by opened_at descending.
func (r IncidentRepository) ListAll(ctx context.Context) ([]domain.Incident, error) {
	const q = `
SELECT id, title, summary, severity, status,
       timeline_event_ids_json, root_cause_claim_id,
       decision_ids_json, outcome_ids_json, playbook_id,
       opened_at, resolved_at, created_by
FROM incidents ORDER BY opened_at DESC`

	return r.queryIncidents(ctx, q)
}

// ListBySeverity returns incidents matching severity, newest first.
func (r IncidentRepository) ListBySeverity(ctx context.Context, severity domain.IncidentSeverity) ([]domain.Incident, error) {
	const q = `
SELECT id, title, summary, severity, status,
       timeline_event_ids_json, root_cause_claim_id,
       decision_ids_json, outcome_ids_json, playbook_id,
       opened_at, resolved_at, created_by
FROM incidents WHERE severity = ? ORDER BY opened_at DESC`

	return r.queryIncidents(ctx, q, string(severity))
}

// ListByStatus returns incidents matching status, newest first.
func (r IncidentRepository) ListByStatus(ctx context.Context, status domain.IncidentStatus) ([]domain.Incident, error) {
	const q = `
SELECT id, title, summary, severity, status,
       timeline_event_ids_json, root_cause_claim_id,
       decision_ids_json, outcome_ids_json, playbook_id,
       opened_at, resolved_at, created_by
FROM incidents WHERE status = ? ORDER BY opened_at DESC`

	return r.queryIncidents(ctx, q, string(status))
}

// Resolve stamps resolved_at and sets status = "resolved". Idempotent.
func (r IncidentRepository) Resolve(ctx context.Context, id string, resolvedAt time.Time) error {
	const q = `UPDATE incidents SET status = 'resolved', resolved_at = ? WHERE id = ?`
	res, err := r.db.ExecContext(ctx, q, resolvedAt.UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return fmt.Errorf("resolve incident: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("incident %s not found", id)
	}
	return nil
}

// AttachDecision appends decisionID to the decision_ids JSON array if absent.
func (r IncidentRepository) AttachDecision(ctx context.Context, incidentID, decisionID string) error {
	return r.appendToJSONArray(ctx, incidentID, "decision_ids_json", decisionID)
}

// AttachOutcome appends outcomeID to the outcome_ids JSON array if absent.
func (r IncidentRepository) AttachOutcome(ctx context.Context, incidentID, outcomeID string) error {
	return r.appendToJSONArray(ctx, incidentID, "outcome_ids_json", outcomeID)
}

// SetPlaybook records the synthesised playbook id.
func (r IncidentRepository) SetPlaybook(ctx context.Context, incidentID, playbookID string) error {
	const q = `UPDATE incidents SET playbook_id = ? WHERE id = ?`
	res, err := r.db.ExecContext(ctx, q, playbookID, incidentID)
	if err != nil {
		return fmt.Errorf("set playbook on incident: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("incident %s not found", incidentID)
	}
	return nil
}

// CountAll returns the total number of incident rows.
func (r IncidentRepository) CountAll(ctx context.Context) (int64, error) {
	var n int64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM incidents`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count incidents: %w", err)
	}
	return n, nil
}

// DeleteAll wipes every incident row.
func (r IncidentRepository) DeleteAll(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM incidents`); err != nil {
		return fmt.Errorf("delete all incidents: %w", err)
	}
	return nil
}

// ── helpers ────────────────────────────────────────────────────────────────

// errNoRows is the sentinel for not-found scans, matching sql.ErrNoRows
// but kept private so callers use the (T, bool, error) convention.
var errNoRows = sql.ErrNoRows

// rowScanner abstracts *sql.Row so scanIncident can be shared.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanIncident(row rowScanner) (domain.Incident, error) {
	var (
		id, title, summary         string
		severity, status           string
		timelineJSON               string
		rootCauseClaimID           string
		decisionJSON, outcomeJSON  string
		playbookID                 string
		openedAtStr, resolvedAtStr string
		createdBy                  string
	)
	err := row.Scan(
		&id, &title, &summary, &severity, &status,
		&timelineJSON, &rootCauseClaimID,
		&decisionJSON, &outcomeJSON, &playbookID,
		&openedAtStr, &resolvedAtStr, &createdBy,
	)
	if err != nil {
		return domain.Incident{}, err
	}

	openedAt, err := time.Parse(time.RFC3339Nano, openedAtStr)
	if err != nil {
		return domain.Incident{}, fmt.Errorf("parse incident.opened_at: %w", err)
	}

	var resolvedAt time.Time
	if resolvedAtStr != "" {
		resolvedAt, err = time.Parse(time.RFC3339Nano, resolvedAtStr)
		if err != nil {
			return domain.Incident{}, fmt.Errorf("parse incident.resolved_at: %w", err)
		}
	}

	var timeline []string
	if err := unmarshalStringSlice(timelineJSON, &timeline); err != nil {
		return domain.Incident{}, fmt.Errorf("unmarshal timeline_event_ids_json: %w", err)
	}
	var decisions []string
	if err := unmarshalStringSlice(decisionJSON, &decisions); err != nil {
		return domain.Incident{}, fmt.Errorf("unmarshal decision_ids_json: %w", err)
	}
	var outcomes []string
	if err := unmarshalStringSlice(outcomeJSON, &outcomes); err != nil {
		return domain.Incident{}, fmt.Errorf("unmarshal outcome_ids_json: %w", err)
	}

	return domain.Incident{
		ID:               id,
		Title:            title,
		Summary:          summary,
		Severity:         domain.IncidentSeverity(severity),
		Status:           domain.IncidentStatus(status),
		TimelineEventIDs: timeline,
		RootCauseClaimID: rootCauseClaimID,
		DecisionIDs:      decisions,
		OutcomeIDs:       outcomes,
		PlaybookID:       playbookID,
		OpenedAt:         openedAt,
		ResolvedAt:       resolvedAt,
		CreatedBy:        createdBy,
	}, nil
}

func (r IncidentRepository) queryIncidents(ctx context.Context, q string, args ...any) ([]domain.Incident, error) {
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query incidents: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Incident
	for rows.Next() {
		inc, err := scanIncident(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, inc)
	}
	return out, rows.Err()
}

// appendToJSONArray reads the current JSON string-array stored in
// column, appends id if not already present, and writes it back.
// This is safe for the small arrays expected on incidents
// (decisions / outcomes / timeline). For large arrays a dedicated
// link table would be preferable, but incidents are low-volume.
func (r IncidentRepository) appendToJSONArray(ctx context.Context, incidentID, column, id string) error {
	selectQ := fmt.Sprintf(`SELECT %s FROM incidents WHERE id = ?`, column)
	var raw string
	if err := r.db.QueryRowContext(ctx, selectQ, incidentID).Scan(&raw); err == sql.ErrNoRows {
		return fmt.Errorf("incident %s not found", incidentID)
	} else if err != nil {
		return fmt.Errorf("read %s: %w", column, err)
	}

	var ids []string
	if err := unmarshalStringSlice(raw, &ids); err != nil {
		return fmt.Errorf("parse %s: %w", column, err)
	}
	for _, existing := range ids {
		if existing == id {
			return nil // already present — idempotent
		}
	}
	ids = append(ids, id)

	updated, err := json.Marshal(ids)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", column, err)
	}
	updateQ := fmt.Sprintf(`UPDATE incidents SET %s = ? WHERE id = ?`, column)
	if _, err := r.db.ExecContext(ctx, updateQ, string(updated), incidentID); err != nil {
		return fmt.Errorf("update %s: %w", column, err)
	}
	return nil
}

func unmarshalStringSlice(src string, dst *[]string) error {
	if src == "" || src == "null" {
		*dst = nil
		return nil
	}
	return json.Unmarshal([]byte(src), dst)
}
