-- name: UpsertRelationship :exec
INSERT INTO relationships (id, type, from_claim_id, to_claim_id, created_at, created_by, derived_by)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(type, from_claim_id, to_claim_id) DO UPDATE SET
  created_at = excluded.created_at,
  created_by = excluded.created_by,
  derived_by = excluded.derived_by;

-- name: ListRelationshipsByClaim :many
SELECT id, type, from_claim_id, to_claim_id, created_at, created_by, derived_by
FROM relationships
WHERE from_claim_id = ? OR to_claim_id = ?
ORDER BY created_at ASC;

-- name: DeleteRelationshipsByClaimID :exec
DELETE FROM relationships WHERE from_claim_id = ? OR to_claim_id = ?;

-- name: DeleteAllRelationships :exec
DELETE FROM relationships;
