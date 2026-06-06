package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
)

// PlaybookRepository persists synthesised playbooks in the MySQL backend.
type PlaybookRepository struct {
	db *sql.DB
}

// Append upserts a playbook plus its lesson link rows. Snapshots the
// prior shape into playbook_versions before overwrite.
func (r PlaybookRepository) Append(ctx context.Context, p domain.Playbook) error {
	if err := p.Validate(); err != nil {
		return fmt.Errorf("invalid playbook: %w", err)
	}
	if err := r.snapshotIfExists(ctx, p.ID); err != nil {
		return fmt.Errorf("snapshot playbook %s: %w", p.ID, err)
	}
	source := p.Source
	if source == "" {
		source = "synthesize"
	}
	steps, err := json.Marshal(p.Steps)
	if err != nil {
		return fmt.Errorf("marshal playbook steps: %w", err)
	}
	var lastVerified any
	if !p.LastVerified.IsZero() {
		lastVerified = p.LastVerified.UTC()
	}
	_, err = r.db.ExecContext(ctx, `
INSERT INTO playbooks (id, `+"`trigger`"+`, statement, scope_service, scope_env, scope_team, steps_json, confidence, derived_at, last_verified, source, created_by)
VALUES (?, ?, ?, ?, ?, ?, CAST(? AS JSON), ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
  `+"`trigger`"+` = VALUES(`+"`trigger`"+`),
  statement = VALUES(statement),
  steps_json = VALUES(steps_json),
  confidence = VALUES(confidence),
  derived_at = VALUES(derived_at),
  last_verified = VALUES(last_verified)`,
		p.ID, p.Trigger, p.Statement,
		p.Scope.Service, p.Scope.Env, p.Scope.Team,
		string(steps),
		p.Confidence,
		p.DerivedAt.UTC(),
		lastVerified,
		source,
		actorOr(p.CreatedBy),
	)
	if err != nil {
		return fmt.Errorf("insert playbook: %w", err)
	}
	if len(p.DerivedFromLessons) > 0 {
		if err := r.AppendLessons(ctx, p.ID, p.DerivedFromLessons); err != nil {
			return err
		}
	}
	return nil
}

// GetByID returns the playbook plus its lesson provenance.
func (r PlaybookRepository) GetByID(ctx context.Context, id string) (domain.Playbook, error) {
	row := r.db.QueryRowContext(ctx, `
SELECT id, `+"`trigger`"+`, statement, scope_service, scope_env, scope_team, steps_json, confidence, derived_at, last_verified, source, created_by
FROM playbooks WHERE id = ?`, id)
	p, err := scanPlaybookRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Playbook{}, fmt.Errorf("playbook %s not found", id)
	}
	if err != nil {
		return domain.Playbook{}, err
	}
	ls, err := r.ListLessons(ctx, id)
	if err != nil {
		return domain.Playbook{}, err
	}
	p.DerivedFromLessons = ls
	return p, nil
}

// ListByTrigger returns playbooks matching a trigger label.
func (r PlaybookRepository) ListByTrigger(ctx context.Context, trigger string) ([]domain.Playbook, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, `+"`trigger`"+`, statement, scope_service, scope_env, scope_team, steps_json, confidence, derived_at, last_verified, source, created_by
FROM playbooks WHERE `+"`trigger`"+` = ? ORDER BY confidence DESC, derived_at DESC`, trigger)
	if err != nil {
		return nil, fmt.Errorf("list playbooks by trigger: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return r.collectPlaybookRows(ctx, rows)
}

// ListByService returns playbooks scoped to the given service.
func (r PlaybookRepository) ListByService(ctx context.Context, service string) ([]domain.Playbook, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, `+"`trigger`"+`, statement, scope_service, scope_env, scope_team, steps_json, confidence, derived_at, last_verified, source, created_by
FROM playbooks WHERE scope_service = ? ORDER BY confidence DESC, derived_at DESC`, service)
	if err != nil {
		return nil, fmt.Errorf("list playbooks by service: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return r.collectPlaybookRows(ctx, rows)
}

// ListAll returns every playbook newest-first by confidence.
func (r PlaybookRepository) ListAll(ctx context.Context) ([]domain.Playbook, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, `+"`trigger`"+`, statement, scope_service, scope_env, scope_team, steps_json, confidence, derived_at, last_verified, source, created_by
FROM playbooks ORDER BY confidence DESC, derived_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list all playbooks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return r.collectPlaybookRows(ctx, rows)
}

// CountAll returns the total number of playbooks stored.
func (r PlaybookRepository) CountAll(ctx context.Context) (int64, error) {
	var n int64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM playbooks`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count playbooks: %w", err)
	}
	return n, nil
}

// DeleteAll wipes playbooks + playbook_lessons.
func (r PlaybookRepository) DeleteAll(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM playbook_lessons`); err != nil {
		return fmt.Errorf("delete all playbook lessons: %w", err)
	}
	if _, err := r.db.ExecContext(ctx, `DELETE FROM playbooks`); err != nil {
		return fmt.Errorf("delete all playbooks: %w", err)
	}
	return nil
}

// AppendLessons is idempotent on (playbook_id, lesson_id).
func (r PlaybookRepository) AppendLessons(ctx context.Context, playbookID string, lessonIDs []string) error {
	for _, lid := range lessonIDs {
		if _, err := r.db.ExecContext(ctx,
			`INSERT IGNORE INTO playbook_lessons (playbook_id, lesson_id) VALUES (?, ?)`,
			playbookID, lid,
		); err != nil {
			return fmt.Errorf("append playbook lesson: %w", err)
		}
	}
	return nil
}

// ListLessons returns the lesson ids that justified the playbook.
func (r PlaybookRepository) ListLessons(ctx context.Context, playbookID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT lesson_id FROM playbook_lessons WHERE playbook_id = ? ORDER BY lesson_id`,
		playbookID,
	)
	if err != nil {
		return nil, fmt.Errorf("list playbook lessons: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]string, 0)
	for rows.Next() {
		var lid string
		if err := rows.Scan(&lid); err != nil {
			return nil, err
		}
		out = append(out, lid)
	}
	return out, rows.Err()
}

func (r PlaybookRepository) collectPlaybookRows(ctx context.Context, rows *sql.Rows) ([]domain.Playbook, error) {
	out := make([]domain.Playbook, 0)
	for rows.Next() {
		p, err := scanPlaybookRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		ls, err := r.ListLessons(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].DerivedFromLessons = ls
	}
	return out, nil
}

// ListVersions returns every snapshot row for the given playbook.
func (r PlaybookRepository) ListVersions(ctx context.Context, playbookID string) ([]ports.EntityVersion, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT version_id, payload_json, valid_from, valid_to
FROM playbook_versions WHERE playbook_id = ? ORDER BY version_id DESC`, playbookID)
	if err != nil {
		return nil, fmt.Errorf("list playbook versions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]ports.EntityVersion, 0)
	for rows.Next() {
		var v ports.EntityVersion
		if err := rows.Scan(&v.VersionID, &v.PayloadJSON, &v.ValidFrom, &v.ValidTo); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (r PlaybookRepository) snapshotIfExists(ctx context.Context, playbookID string) error {
	current, err := r.GetByID(ctx, playbookID)
	if err != nil {
		return nil //nolint:nilerr // missing prior row = no snapshot
	}
	payload, err := json.Marshal(current)
	if err != nil {
		return fmt.Errorf("marshal playbook snapshot: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `
INSERT INTO playbook_versions (playbook_id, payload_json, valid_from, valid_to)
VALUES (?, CAST(? AS JSON), ?, ?)`,
		playbookID, string(payload), current.DerivedAt.UTC(), time.Now().UTC(),
	)
	return err
}

func scanPlaybookRow(row *sql.Row) (domain.Playbook, error) {
	var p domain.Playbook
	var stepsRaw string
	var lastVerified sql.NullTime
	if err := row.Scan(
		&p.ID, &p.Trigger, &p.Statement,
		&p.Scope.Service, &p.Scope.Env, &p.Scope.Team,
		&stepsRaw, &p.Confidence,
		&p.DerivedAt, &lastVerified,
		&p.Source, &p.CreatedBy,
	); err != nil {
		return domain.Playbook{}, err
	}
	if lastVerified.Valid {
		p.LastVerified = lastVerified.Time
	}
	if err := unmarshalSteps(stepsRaw, &p.Steps); err != nil {
		return domain.Playbook{}, err
	}
	return p, nil
}

func scanPlaybookRows(rows *sql.Rows) (domain.Playbook, error) {
	var p domain.Playbook
	var stepsRaw string
	var lastVerified sql.NullTime
	if err := rows.Scan(
		&p.ID, &p.Trigger, &p.Statement,
		&p.Scope.Service, &p.Scope.Env, &p.Scope.Team,
		&stepsRaw, &p.Confidence,
		&p.DerivedAt, &lastVerified,
		&p.Source, &p.CreatedBy,
	); err != nil {
		return domain.Playbook{}, err
	}
	if lastVerified.Valid {
		p.LastVerified = lastVerified.Time
	}
	if err := unmarshalSteps(stepsRaw, &p.Steps); err != nil {
		return domain.Playbook{}, err
	}
	return p, nil
}

func unmarshalSteps(raw string, dst *[]domain.PlaybookStep) error {
	if strings.TrimSpace(raw) == "" || raw == "null" {
		*dst = nil
		return nil
	}
	out := []domain.PlaybookStep{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return fmt.Errorf("unmarshal playbook steps: %w", err)
	}
	*dst = out
	return nil
}

var _ = time.Now
