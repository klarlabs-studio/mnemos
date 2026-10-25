package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"go.klarlabs.de/mnemos/internal/domain"
)

// ListLiveByTextKeys implements ports.ClaimRepository: the live claims (valid
// time open, not deprecated) whose text_key is one of keys, oldest first.
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
	placeholders, args := inPlaceholders(keys)
	//nolint:gosec // G202: placeholders are literal "?" tokens and predicate a constant, not user input
	rows, err := r.db.QueryContext(ctx, `
SELECT `+claimColumns("")+`
FROM claims
WHERE text_key IN (`+placeholders+`) AND `+predicate+`
ORDER BY created_at, id`, args...)
	if err != nil {
		return nil, fmt.Errorf("list claims by text key: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectClaimRows(rows)
}

// textKeyBackfillBatch bounds one backfill transaction, so a large legacy
// database is keyed in steps rather than one long lock.
const textKeyBackfillBatch = 1000

// backfillClaimTextKeys keys every claim written before text_key existed. It
// runs on every open; on a keyed database it is one indexed probe that finds
// nothing. A failure leaves only those rows invisible to the duplicate check,
// so it is logged and retried on the next open, never a reason to refuse the
// store.
func backfillClaimTextKeys(ctx context.Context, db *sql.DB) {
	for {
		n, err := backfillTextKeyBatch(ctx, db)
		if err != nil {
			slog.Warn("claim text keys not backfilled yet; older duplicates are not matched until a later open finishes",
				"error", err)
			return
		}
		if n < textKeyBackfillBatch {
			return
		}
	}
}

func backfillTextKeyBatch(ctx context.Context, db *sql.DB) (int, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, text FROM claims WHERE text_key = '' LIMIT ?`, textKeyBackfillBatch)
	if err != nil {
		return 0, fmt.Errorf("select unkeyed claims: %w", err)
	}
	keys := map[string]string{}
	for rows.Next() {
		var id, text string
		if err := rows.Scan(&id, &text); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("scan unkeyed claim: %w", err)
		}
		keys[id] = domain.ClaimTextKey(text)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate unkeyed claims: %w", err)
	}
	return len(keys), writeTextKeys(ctx, db, keys)
}

func writeTextKeys(ctx context.Context, db *sql.DB, keys map[string]string) error {
	if len(keys) == 0 {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin text key backfill: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for id, key := range keys {
		if _, err := tx.ExecContext(ctx, `UPDATE claims SET text_key = ? WHERE id = ?`, key, id); err != nil {
			return fmt.Errorf("key claim %s: %w", id, err)
		}
	}
	return tx.Commit()
}
