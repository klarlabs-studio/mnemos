package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// IncidentRepository persists incidents in the MySQL backend. Semantics
// mirror the SQLite repository: Upsert replaces the whole row by id, lists are
// newest-opened first, and Resolve/Attach*/SetPlaybook report an unknown id as
// an error. Tenant isolation is the namespace (one database per tenant).
type IncidentRepository struct {
	db *sql.DB
}

const incidentColumns = `id, title, summary, severity, status,
       CAST(timeline_event_ids_json AS CHAR), root_cause_claim_id,
       CAST(decision_ids_json AS CHAR), CAST(outcome_ids_json AS CHAR), playbook_id,
       opened_at, resolved_at, created_by`

// Upsert creates or replaces the incident row by id.
func (r IncidentRepository) Upsert(ctx context.Context, incident domain.Incident) error {
	if err := incident.Validate(); err != nil {
		return fmt.Errorf("invalid incident: %w", err)
	}
	lists := make([]string, 3)
	for i, xs := range [][]string{incident.TimelineEventIDs, incident.DecisionIDs, incident.OutcomeIDs} {
		if xs == nil {
			xs = []string{}
		}
		b, err := json.Marshal(xs)
		if err != nil {
			return fmt.Errorf("marshal incident id list: %w", err)
		}
		lists[i] = string(b)
	}
	var resolvedAt any
	if !incident.ResolvedAt.IsZero() {
		resolvedAt = incident.ResolvedAt.UTC()
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO incidents (id, title, summary, severity, status, timeline_event_ids_json, root_cause_claim_id,
                       decision_ids_json, outcome_ids_json, playbook_id, opened_at, resolved_at, created_by)
VALUES (?, ?, ?, ?, ?, CAST(? AS JSON), ?, CAST(? AS JSON), CAST(? AS JSON), ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
  title = VALUES(title),
  summary = VALUES(summary),
  severity = VALUES(severity),
  status = VALUES(status),
  timeline_event_ids_json = VALUES(timeline_event_ids_json),
  root_cause_claim_id = VALUES(root_cause_claim_id),
  decision_ids_json = VALUES(decision_ids_json),
  outcome_ids_json = VALUES(outcome_ids_json),
  playbook_id = VALUES(playbook_id),
  opened_at = VALUES(opened_at),
  resolved_at = VALUES(resolved_at),
  created_by = VALUES(created_by)`,
		incident.ID, incident.Title, incident.Summary, string(incident.Severity), string(incident.Status),
		lists[0], incident.RootCauseClaimID, lists[1], lists[2], incident.PlaybookID,
		incident.OpenedAt.UTC(), resolvedAt, actorOr(incident.CreatedBy),
	)
	if err != nil {
		return fmt.Errorf("upsert incident: %w", err)
	}
	return nil
}

// GetByID returns the incident, or (Incident{}, false, nil) when absent.
func (r IncidentRepository) GetByID(ctx context.Context, id string) (domain.Incident, bool, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+incidentColumns+` FROM incidents WHERE id = ?`, id)
	inc, err := scanIncident(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Incident{}, false, nil
	}
	if err != nil {
		return domain.Incident{}, false, err
	}
	return inc, true, nil
}

// ListAll returns every incident, newest-opened first.
func (r IncidentRepository) ListAll(ctx context.Context) ([]domain.Incident, error) {
	return r.query(ctx, `SELECT `+incidentColumns+` FROM incidents ORDER BY opened_at DESC`)
}

// ListBySeverity returns incidents of the given severity, newest-opened first.
func (r IncidentRepository) ListBySeverity(ctx context.Context, severity domain.IncidentSeverity) ([]domain.Incident, error) {
	return r.query(ctx, `SELECT `+incidentColumns+` FROM incidents WHERE severity = ? ORDER BY opened_at DESC`, string(severity))
}

// ListByStatus returns incidents in the given status, newest-opened first.
func (r IncidentRepository) ListByStatus(ctx context.Context, status domain.IncidentStatus) ([]domain.Incident, error) {
	return r.query(ctx, `SELECT `+incidentColumns+` FROM incidents WHERE status = ? ORDER BY opened_at DESC`, string(status))
}

// Resolve sets status = resolved and stamps resolved_at. Idempotent: an
// existing incident is found even when the update changes nothing (MySQL
// reports matched-but-unchanged rows as 0 affected).
func (r IncidentRepository) Resolve(ctx context.Context, id string, resolvedAt time.Time) error {
	if err := r.requireIncident(ctx, id); err != nil {
		return err
	}
	if _, err := r.db.ExecContext(ctx, `UPDATE incidents SET status = 'resolved', resolved_at = ? WHERE id = ?`, resolvedAt.UTC(), id); err != nil {
		return fmt.Errorf("resolve incident: %w", err)
	}
	return nil
}

// AttachDecision appends decisionID to decision_ids if absent.
func (r IncidentRepository) AttachDecision(ctx context.Context, incidentID, decisionID string) error {
	return r.appendToList(ctx, incidentID, "decision_ids_json", decisionID)
}

// AttachOutcome appends outcomeID to outcome_ids if absent.
func (r IncidentRepository) AttachOutcome(ctx context.Context, incidentID, outcomeID string) error {
	return r.appendToList(ctx, incidentID, "outcome_ids_json", outcomeID)
}

// SetPlaybook records the synthesised playbook id.
func (r IncidentRepository) SetPlaybook(ctx context.Context, incidentID, playbookID string) error {
	if err := r.requireIncident(ctx, incidentID); err != nil {
		return err
	}
	if _, err := r.db.ExecContext(ctx, `UPDATE incidents SET playbook_id = ? WHERE id = ?`, playbookID, incidentID); err != nil {
		return fmt.Errorf("set playbook on incident: %w", err)
	}
	return nil
}

// CountAll returns the number of incident rows.
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

// appendToList adds id to a JSON string array unless already present, in one
// statement so concurrent attaches cannot lose each other.
func (r IncidentRepository) appendToList(ctx context.Context, incidentID, column, id string) error {
	if err := r.requireIncident(ctx, incidentID); err != nil {
		return err
	}
	q := fmt.Sprintf(`UPDATE incidents SET %[1]s = IF(JSON_CONTAINS(%[1]s, JSON_QUOTE(?)), %[1]s, JSON_ARRAY_APPEND(%[1]s, '$', ?)) WHERE id = ?`, column)
	if _, err := r.db.ExecContext(ctx, q, id, id, incidentID); err != nil {
		return fmt.Errorf("update %s: %w", column, err)
	}
	return nil
}

func (r IncidentRepository) requireIncident(ctx context.Context, id string) error {
	var one int
	err := r.db.QueryRowContext(ctx, `SELECT 1 FROM incidents WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("incident %s not found", id)
	}
	if err != nil {
		return fmt.Errorf("look up incident %s: %w", id, err)
	}
	return nil
}

func (r IncidentRepository) query(ctx context.Context, q string, args ...any) ([]domain.Incident, error) {
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

type incidentScanner interface {
	Scan(dest ...any) error
}

func scanIncident(row incidentScanner) (domain.Incident, error) {
	var (
		inc                                       domain.Incident
		severity, status                          string
		timelineJSON, decisionsJSON, outcomesJSON string
		resolvedAt                                sql.NullTime
	)
	if err := row.Scan(&inc.ID, &inc.Title, &inc.Summary, &severity, &status,
		&timelineJSON, &inc.RootCauseClaimID, &decisionsJSON, &outcomesJSON, &inc.PlaybookID,
		&inc.OpenedAt, &resolvedAt, &inc.CreatedBy); err != nil {
		return domain.Incident{}, err
	}
	inc.Severity = domain.IncidentSeverity(severity)
	inc.Status = domain.IncidentStatus(status)
	inc.OpenedAt = inc.OpenedAt.UTC()
	if resolvedAt.Valid {
		inc.ResolvedAt = resolvedAt.Time.UTC()
	}
	for _, f := range []struct {
		raw string
		dst *[]string
		col string
	}{
		{timelineJSON, &inc.TimelineEventIDs, "timeline_event_ids_json"},
		{decisionsJSON, &inc.DecisionIDs, "decision_ids_json"},
		{outcomesJSON, &inc.OutcomeIDs, "outcome_ids_json"},
	} {
		if f.raw == "" || f.raw == "null" {
			continue
		}
		if err := json.Unmarshal([]byte(f.raw), f.dst); err != nil {
			return domain.Incident{}, fmt.Errorf("unmarshal %s: %w", f.col, err)
		}
		if len(*f.dst) == 0 {
			*f.dst = nil
		}
	}
	return inc, nil
}
