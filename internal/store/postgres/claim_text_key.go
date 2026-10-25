package postgres

import (
	"context"
	"fmt"
	"log/slog"

	"go.klarlabs.de/mnemos/internal/domain"
)

// ListLiveByTextKeys implements ports.ClaimRepository: the live claims (valid
// time open, not deprecated) whose text_key is one of keys, oldest first. Row
// security scopes it to the connection's tenant like every other read.
func (r ClaimRepository) ListLiveByTextKeys(ctx context.Context, keys []string) ([]domain.Claim, error) {
	return r.listByTextKeys(ctx, keys, `valid_to IS NULL AND status <> 'deprecated'`)
}

// ListMergedByTextKeys implements ports.ClaimRepository: the merge redirects
// whose text_key is one of keys, oldest first.
func (r ClaimRepository) ListMergedByTextKeys(ctx context.Context, keys []string) ([]domain.Claim, error) {
	return r.listByTextKeys(ctx, keys, `merged_into <> ''`)
}

func (r ClaimRepository) listByTextKeys(ctx context.Context, keys []string, predicate string) ([]domain.Claim, error) {
	if len(keys) == 0 {
		return []domain.Claim{}, nil
	}
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`
SELECT `+claimColumns("")+`
FROM %s
WHERE text_key = ANY($1) AND `+predicate+`
ORDER BY created_at, id`, qualify(r.ns, "claims")), pgArray(keys))
	if err != nil {
		return nil, fmt.Errorf("list claims by text key: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectClaimRows(rows)
}

// textKeyBackfillBatch bounds one backfill statement, so a large legacy
// namespace is keyed in steps rather than one long transaction.
const textKeyBackfillBatch = 1000

// backfillClaimTextKeys keys every claim of the connection's tenant written
// before text_key existed. It runs on every open; on a keyed namespace it is
// one indexed probe that finds nothing. A failure leaves only those rows
// invisible to the duplicate check, so it is logged and retried on the next
// open, never a reason to refuse the store.
func backfillClaimTextKeys(ctx context.Context, db pgQuerier, ns string) {
	for {
		n, err := backfillTextKeyBatch(ctx, db, ns)
		if err != nil {
			slog.Warn("claim text keys not backfilled yet; older duplicates are not matched until a later open finishes",
				"namespace", ns, "error", err)
			return
		}
		if n < textKeyBackfillBatch {
			return
		}
	}
}

func backfillTextKeyBatch(ctx context.Context, db pgQuerier, ns string) (int, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf(
		`SELECT id, text FROM %s WHERE text_key = '' LIMIT $1`, qualify(ns, "claims")), textKeyBackfillBatch)
	if err != nil {
		return 0, fmt.Errorf("select unkeyed claims: %w", err)
	}
	var ids, keys []string
	for rows.Next() {
		var id, text string
		if err := rows.Scan(&id, &text); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("scan unkeyed claim: %w", err)
		}
		ids = append(ids, id)
		keys = append(keys, domain.ClaimTextKey(text))
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate unkeyed claims: %w", err)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	// One statement per batch: unnest pairs each id with its key.
	if _, err := db.ExecContext(ctx, fmt.Sprintf(`
UPDATE %s AS c SET text_key = k.key
FROM unnest($1::text[], $2::text[]) AS k(id, key)
WHERE c.id = k.id`, qualify(ns, "claims")), pgArray(ids), pgArray(keys)); err != nil {
		return 0, fmt.Errorf("write text keys: %w", err)
	}
	return len(ids), nil
}
