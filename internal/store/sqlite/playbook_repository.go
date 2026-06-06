package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/store/sqlite/sqlcgen"
)

// PlaybookRepository provides SQLite-backed storage for synthesised
// or hand-authored playbooks.
type PlaybookRepository struct {
	db *sql.DB
	q  *sqlcgen.Queries
}

// NewPlaybookRepository returns a PlaybookRepository backed by db.
func NewPlaybookRepository(db *sql.DB) PlaybookRepository {
	return PlaybookRepository{db: db, q: sqlcgen.New(db)}
}

// Append upserts the playbook row plus its lesson link rows.
// Snapshots the prior shape into playbook_versions before the write.
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
	lastVerified := ""
	if !p.LastVerified.IsZero() {
		lastVerified = p.LastVerified.UTC().Format(time.RFC3339Nano)
	}
	if err := r.q.CreatePlaybook(ctx, sqlcgen.CreatePlaybookParams{
		ID:           p.ID,
		Trigger:      p.Trigger,
		Statement:    p.Statement,
		ScopeService: p.Scope.Service,
		ScopeEnv:     p.Scope.Env,
		ScopeTeam:    p.Scope.Team,
		StepsJson:    string(steps),
		Confidence:   p.Confidence,
		DerivedAt:    p.DerivedAt.UTC().Format(time.RFC3339Nano),
		LastVerified: lastVerified,
		Source:       source,
		CreatedBy:    actorOr(p.CreatedBy),
	}); err != nil {
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
	row, err := r.q.GetPlaybookByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Playbook{}, fmt.Errorf("playbook %s not found", id)
	}
	if err != nil {
		return domain.Playbook{}, err
	}
	p, err := mapSQLPlaybook(row)
	if err != nil {
		return domain.Playbook{}, err
	}
	lessons, err := r.ListLessons(ctx, id)
	if err != nil {
		return domain.Playbook{}, err
	}
	p.DerivedFromLessons = lessons
	return p, nil
}

// ListByTrigger returns playbooks matching a trigger label.
func (r PlaybookRepository) ListByTrigger(ctx context.Context, trigger string) ([]domain.Playbook, error) {
	rows, err := r.q.ListPlaybooksByTrigger(ctx, trigger)
	if err != nil {
		return nil, err
	}
	return r.hydratePlaybooks(ctx, rows)
}

// ListByService returns playbooks scoped to the given service.
func (r PlaybookRepository) ListByService(ctx context.Context, service string) ([]domain.Playbook, error) {
	rows, err := r.q.ListPlaybooksByService(ctx, service)
	if err != nil {
		return nil, err
	}
	return r.hydratePlaybooks(ctx, rows)
}

// ListAll returns every playbook, highest confidence first.
func (r PlaybookRepository) ListAll(ctx context.Context) ([]domain.Playbook, error) {
	rows, err := r.q.ListAllPlaybooks(ctx)
	if err != nil {
		return nil, err
	}
	return r.hydratePlaybooks(ctx, rows)
}

// CountAll returns the total number of playbooks stored.
func (r PlaybookRepository) CountAll(ctx context.Context) (int64, error) {
	return r.q.CountPlaybooks(ctx)
}

// DeleteAll wipes playbooks + playbook_lessons.
func (r PlaybookRepository) DeleteAll(ctx context.Context) error {
	if err := r.q.DeleteAllPlaybookLessons(ctx); err != nil {
		return fmt.Errorf("delete all playbook lessons: %w", err)
	}
	return r.q.DeleteAllPlaybooks(ctx)
}

// AppendLessons is idempotent on (playbook_id, lesson_id).
func (r PlaybookRepository) AppendLessons(ctx context.Context, playbookID string, lessonIDs []string) error {
	for _, lid := range lessonIDs {
		if err := r.q.AppendPlaybookLesson(ctx, sqlcgen.AppendPlaybookLessonParams{
			PlaybookID: playbookID,
			LessonID:   lid,
		}); err != nil {
			return fmt.Errorf("append playbook lesson: %w", err)
		}
	}
	return nil
}

// ListLessons returns the lesson ids that justified the playbook.
func (r PlaybookRepository) ListLessons(ctx context.Context, playbookID string) ([]string, error) {
	rows, err := r.q.ListPlaybookLessons(ctx, playbookID)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.LessonID)
	}
	return out, nil
}

func (r PlaybookRepository) hydratePlaybooks(ctx context.Context, rows []sqlcgen.Playbook) ([]domain.Playbook, error) {
	out := make([]domain.Playbook, 0, len(rows))
	for _, row := range rows {
		p, err := mapSQLPlaybook(row)
		if err != nil {
			return nil, err
		}
		ls, err := r.ListLessons(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		p.DerivedFromLessons = ls
		out = append(out, p)
	}
	return out, nil
}

// ListVersions returns every snapshot row for the given playbook,
// newest first.
func (r PlaybookRepository) ListVersions(ctx context.Context, playbookID string) ([]ports.EntityVersion, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT version_id, payload_json, valid_from, valid_to
FROM playbook_versions
WHERE playbook_id = ?
ORDER BY version_id DESC`, playbookID)
	if err != nil {
		return nil, fmt.Errorf("list playbook versions: %w", err)
	}
	defer closeRows(rows)
	out := make([]ports.EntityVersion, 0)
	for rows.Next() {
		var v ports.EntityVersion
		var validFrom, validTo string
		if err := rows.Scan(&v.VersionID, &v.PayloadJSON, &validFrom, &validTo); err != nil {
			return nil, err
		}
		if t, perr := time.Parse(time.RFC3339Nano, validFrom); perr == nil {
			v.ValidFrom = t
		}
		if t, perr := time.Parse(time.RFC3339Nano, validTo); perr == nil {
			v.ValidTo = t
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (r PlaybookRepository) snapshotIfExists(ctx context.Context, playbookID string) error {
	row, err := r.q.GetPlaybookByID(ctx, playbookID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	current, err := mapSQLPlaybook(row)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(current)
	if err != nil {
		return fmt.Errorf("marshal playbook snapshot: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = r.db.ExecContext(ctx, `
INSERT INTO playbook_versions (playbook_id, payload_json, valid_from, valid_to)
VALUES (?, ?, ?, ?)`,
		playbookID, string(payload), current.DerivedAt.UTC().Format(time.RFC3339Nano), now,
	)
	return err
}

func mapSQLPlaybook(row sqlcgen.Playbook) (domain.Playbook, error) {
	derived, err := time.Parse(time.RFC3339Nano, row.DerivedAt)
	if err != nil {
		return domain.Playbook{}, fmt.Errorf("parse playbook.derived_at: %w", err)
	}
	var lastVerified time.Time
	if row.LastVerified != "" {
		if t, perr := time.Parse(time.RFC3339Nano, row.LastVerified); perr == nil {
			lastVerified = t
		}
	}
	var steps []domain.PlaybookStep
	if row.StepsJson != "" {
		if err := json.Unmarshal([]byte(row.StepsJson), &steps); err != nil {
			return domain.Playbook{}, fmt.Errorf("unmarshal playbook.steps_json: %w", err)
		}
	}
	return domain.Playbook{
		ID:           row.ID,
		Trigger:      row.Trigger,
		Statement:    row.Statement,
		Scope:        domain.LessonScope{Service: row.ScopeService, Env: row.ScopeEnv, Team: row.ScopeTeam},
		Steps:        steps,
		Confidence:   row.Confidence,
		DerivedAt:    derived,
		LastVerified: lastVerified,
		Source:       row.Source,
		CreatedBy:    row.CreatedBy,
	}, nil
}
