-- name: CreateLesson :exec
-- Idempotent on id. Updates statement/confidence/last_verified on
-- conflict so re-running synthesis with stronger evidence ratchets
-- the confidence forward without churning the row's identity.
INSERT INTO lessons (id, statement, scope_service, scope_env, scope_team, trigger, kind, confidence, derived_at, last_verified, source, created_by, polarity, subject_class)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  statement = excluded.statement,
  confidence = excluded.confidence,
  last_verified = excluded.last_verified,
  derived_at = excluded.derived_at,
  polarity = excluded.polarity,
  subject_class = excluded.subject_class;

-- name: GetLessonByID :one
SELECT id, statement, scope_service, scope_env, scope_team, trigger, kind, confidence, derived_at, last_verified, source, created_by, polarity, subject_class
FROM lessons
WHERE id = ?;

-- name: ListLessonsByService :many
SELECT id, statement, scope_service, scope_env, scope_team, trigger, kind, confidence, derived_at, last_verified, source, created_by, polarity, subject_class
FROM lessons
WHERE scope_service = ?
ORDER BY confidence DESC, derived_at DESC;

-- name: ListLessonsByTrigger :many
SELECT id, statement, scope_service, scope_env, scope_team, trigger, kind, confidence, derived_at, last_verified, source, created_by, polarity, subject_class
FROM lessons
WHERE trigger = ?
ORDER BY confidence DESC, derived_at DESC;

-- name: ListAllLessons :many
SELECT id, statement, scope_service, scope_env, scope_team, trigger, kind, confidence, derived_at, last_verified, source, created_by, polarity, subject_class
FROM lessons
ORDER BY confidence DESC, derived_at DESC;

-- name: CountLessons :one
SELECT COUNT(*) FROM lessons;

-- name: DeleteAllLessons :exec
DELETE FROM lessons;

-- name: AppendLessonEvidence :exec
-- Idempotent on the composite key so re-running synthesis doesn't
-- duplicate evidence rows.
INSERT INTO lesson_evidence (lesson_id, action_id)
VALUES (?, ?)
ON CONFLICT(lesson_id, action_id) DO NOTHING;

-- name: ListLessonEvidence :many
SELECT lesson_id, action_id
FROM lesson_evidence
WHERE lesson_id = ?;

-- name: DeleteLessonEvidence :exec
DELETE FROM lesson_evidence WHERE lesson_id = ?;

-- name: DeleteAllLessonEvidence :exec
DELETE FROM lesson_evidence;
