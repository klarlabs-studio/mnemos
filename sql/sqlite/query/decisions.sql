-- WARNING (sqlc v1.30.0 bug): do NOT place a comment line between a `-- name:`
-- directive and its query. A doc comment AFTER `-- name:` shifts sqlc's byte
-- offsets and corrupts EVERY generated query in this file (scrambled/truncated
-- SQL strings). Put per-query docs BEFORE the `-- name:` line, as below.
--
-- CreateDecision is idempotent on id. Re-recording the same decision id refreshes
-- statement, plan, reasoning, risk_level, alternatives, outcome_id, refuted_beliefs_json,
-- and failed_outcome_id but preserves chosen_at and created_at (the original decision
-- moment is the load-bearing fact).
-- name: CreateDecision :exec
INSERT INTO decisions (id, statement, plan, reasoning, risk_level, alternatives_json, outcome_id, chosen_at, created_by, created_at, scope_service, scope_env, scope_team, refuted_beliefs_json, failed_outcome_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  statement = excluded.statement,
  plan = excluded.plan,
  reasoning = excluded.reasoning,
  risk_level = excluded.risk_level,
  alternatives_json = excluded.alternatives_json,
  outcome_id = excluded.outcome_id,
  refuted_beliefs_json = excluded.refuted_beliefs_json,
  failed_outcome_id = excluded.failed_outcome_id;

-- name: GetDecisionByID :one
SELECT id, statement, plan, reasoning, risk_level, alternatives_json, outcome_id, chosen_at, created_by, created_at, scope_service, scope_env, scope_team, refuted_beliefs_json, failed_outcome_id
FROM decisions
WHERE id = ?;

-- name: ListAllDecisions :many
SELECT id, statement, plan, reasoning, risk_level, alternatives_json, outcome_id, chosen_at, created_by, created_at, scope_service, scope_env, scope_team, refuted_beliefs_json, failed_outcome_id
FROM decisions
ORDER BY chosen_at DESC;

-- name: ListDecisionsByRiskLevel :many
SELECT id, statement, plan, reasoning, risk_level, alternatives_json, outcome_id, chosen_at, created_by, created_at, scope_service, scope_env, scope_team, refuted_beliefs_json, failed_outcome_id
FROM decisions
WHERE risk_level = ?
ORDER BY chosen_at DESC;

-- name: AttachDecisionOutcome :exec
UPDATE decisions SET outcome_id = ? WHERE id = ?;

-- name: CountDecisions :one
SELECT COUNT(*) FROM decisions;

-- name: DeleteAllDecisions :exec
DELETE FROM decisions;

-- name: AppendDecisionBelief :exec
INSERT INTO decision_beliefs (decision_id, claim_id)
VALUES (?, ?)
ON CONFLICT(decision_id, claim_id) DO NOTHING;

-- name: ListDecisionBeliefs :many
SELECT decision_id, claim_id
FROM decision_beliefs
WHERE decision_id = ?;

-- name: DeleteDecisionBeliefs :exec
DELETE FROM decision_beliefs WHERE decision_id = ?;

-- name: DeleteAllDecisionBeliefs :exec
DELETE FROM decision_beliefs;
