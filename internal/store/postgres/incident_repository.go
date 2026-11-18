package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// IncidentRepository persists incidents in the configured Postgres namespace.
// Semantics mirror the SQLite repository: Upsert replaces the whole row by id,
// lists are newest-opened first, and Resolve/Attach*/SetPlaybook report an
// unknown id as an error. The table is in the ADR-0007 RLS `scoped` array, so
// every statement here is confined to the connection's tenant.
type IncidentRepository struct {
	db pgQuerier
	ns string
}

const incidentColumns = `id, title, summary, severity, status,
       timeline_event_ids_json::text, root_cause_claim_id,
       decision_ids_json::text, outcome_ids_json::text, playbook_id,
       opened_at, resolved_at, created_by`

// Upsert creates or replaces the incident row by id.
func (r IncidentRepository) Upsert(ctx context.Context, incident domain.Incident) error {
	if err := incident.Validate(); err != nil {
		return fmt.Errorf("invalid incident: %w", err)
	}
	timeline, decisions, outcomes, err := marshalIncidentLists(incident)
	if err != nil {
		return err
	}
	var resolvedAt any
	if !incident.ResolvedAt.IsZero() {
		resolvedAt = incident.ResolvedAt.UTC()
	}
	_, err = r.db.ExecContext(ctx, fmt.Sprintf(`
INSERT INTO %s (id, title, summary, severity, status, timeline_event_ids_json, root_cause_claim_id,
                decision_ids_json, outcome_ids_json, playbook_id, opened_at, resolved_at, created_by)
VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8::jsonb, $9::jsonb, $10, $11, $12, $13)
ON CONFLICT (id) DO UPDATE SET
  title = EXCLUDED.title,
  summary = EXCLUDED.summary,
  severity = EXCLUDED.severity,
  status = EXCLUDED.status,
  timeline_event_ids_json = EXCLUDED.timeline_event_ids_json,
  root_cause_claim_id = EXCLUDED.root_cause_claim_id,
  decision_ids_json = EXCLUDED.decision_ids_json,
  outcome_ids_json = EXCLUDED.outcome_ids_json,
  playbook_id = EXCLUDED.playbook_id,
  opened_at = EXCLUDED.opened_at,
  resolved_at = EXCLUDED.resolved_at,
  created_by = EXCLUDED.created_by`, qualify(r.ns, "incidents")),
		incident.ID, incident.Title, incident.Summary, string(incident.Severity), string(incident.Status),
		timeline, incident.RootCauseClaimID, decisions, outcomes, incident.PlaybookID,
		incident.OpenedAt.UTC(), resolvedAt, actorOr(incident.CreatedBy),
	)
	if err != nil {
		return fmt.Errorf("upsert incident: %w", err)
	}
	return nil
}

// GetByID returns the incident, or (Incident{}, false, nil) when absent.
func (r IncidentRepository) GetByID(ctx context.Context, id string) (domain.Incident, bool, error) {
	row := r.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT %s FROM %s WHERE id = $1`, incidentColumns, qualify(r.ns, "incidents")), id)
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
	return r.query(ctx, fmt.Sprintf(`SELECT %s FROM %s ORDER BY opened_at DESC`, incidentColumns, qualify(r.ns, "incidents")))
}

// ListBySeverity returns incidents of the given severity, newest-opened first.
func (r IncidentRepository) ListBySeverity(ctx context.Context, severity domain.IncidentSeverity) ([]domain.Incident, error) {
	return r.query(ctx, fmt.Sprintf(`SELECT %s FROM %s WHERE severity = $1 ORDER BY opened_at DESC`, incidentColumns, qualify(r.ns, "incidents")), string(severity))
}

// ListByStatus returns incidents in the given status, newest-opened first.
func (r IncidentRepository) ListByStatus(ctx context.Context, status domain.IncidentStatus) ([]domain.Incident, error) {
	return r.query(ctx, fmt.Sprintf(`SELECT %s FROM %s WHERE status = $1 ORDER BY opened_at DESC`, incidentColumns, qualify(r.ns, "incidents")), string(status))
}

// Resolve sets status = resolved and stamps resolved_at. Idempotent.
func (r IncidentRepository) Resolve(ctx context.Context, id string, resolvedAt time.Time) error {
	res, err := r.db.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET status = 'resolved', resolved_at = $1 WHERE id = $2`, qualify(r.ns, "incidents")), resolvedAt.UTC(), id)
	if err != nil {
		return fmt.Errorf("resolve incident: %w", err)
	}
	return requireRow(res, id)
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
	res, err := r.db.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET playbook_id = $1 WHERE id = $2`, qualify(r.ns, "incidents")), playbookID, incidentID)
	if err != nil {
		return fmt.Errorf("set playbook on incident: %w", err)
	}
	return requireRow(res, incidentID)
}

// CountAll returns the number of incident rows.
func (r IncidentRepository) CountAll(ctx context.Context) (int64, error) {
	var n int64
	if err := r.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s`, qualify(r.ns, "incidents"))).Scan(&n); err != nil {
		return 0, fmt.Errorf("count incidents: %w", err)
	}
	return n, nil
}

// DeleteAll wipes every incident row.
func (r IncidentRepository) DeleteAll(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s`, qualify(r.ns, "incidents"))); err != nil {
		return fmt.Errorf("delete all incidents: %w", err)
	}
	return nil
}

// appendToList adds id to a jsonb string array unless already present, in one
// statement so concurrent attaches cannot lose each other.
func (r IncidentRepository) appendToList(ctx context.Context, incidentID, column, id string) error {
	res, err := r.db.ExecContext(ctx, fmt.Sprintf(`
UPDATE %[1]s SET %[2]s = CASE WHEN %[2]s @> jsonb_build_array($1::text) THEN %[2]s ELSE %[2]s || to_jsonb($1::text) END
WHERE id = $2`, qualify(r.ns, "incidents"), column), id, incidentID)
	if err != nil {
		return fmt.Errorf("update %s: %w", column, err)
	}
	return requireRow(res, incidentID)
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

func requireRow(res sql.Result, id string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("incident %s: rows affected: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("incident %s not found", id)
	}
	return nil
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
		if err := unmarshalIDList(f.raw, f.dst); err != nil {
			return domain.Incident{}, fmt.Errorf("unmarshal %s: %w", f.col, err)
		}
	}
	return inc, nil
}

func marshalIncidentLists(inc domain.Incident) (timeline, decisions, outcomes string, err error) {
	out := make([]string, 3)
	for i, xs := range [][]string{inc.TimelineEventIDs, inc.DecisionIDs, inc.OutcomeIDs} {
		if xs == nil {
			xs = []string{}
		}
		b, merr := json.Marshal(xs)
		if merr != nil {
			return "", "", "", fmt.Errorf("marshal incident id list: %w", merr)
		}
		out[i] = string(b)
	}
	return out[0], out[1], out[2], nil
}

// unmarshalIDList decodes a JSON string array; an empty array reads back as
// nil, as on SQLite and the memory backend.
func unmarshalIDList(raw string, dst *[]string) error {
	if raw == "" || raw == "null" {
		*dst = nil
		return nil
	}
	if err := json.Unmarshal([]byte(raw), dst); err != nil {
		return err
	}
	if len(*dst) == 0 {
		*dst = nil
	}
	return nil
}
