package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"go.klarlabs.de/mnemos/internal/domain"
)

// ListLiveByTextKeys implements ports.ClaimRepository: the live claims whose
// text_key is one of keys, oldest first. The predicate is liveClaimSQL, the
// same definition of "live" brain health counts.
func (r ClaimRepository) ListLiveByTextKeys(ctx context.Context, keys []string) ([]domain.Claim, error) {
	out := []domain.Claim{}
	err := inChunks(keys, func(chunk []string) error {
		part, err := r.listLiveByTextKeysChunk(ctx, chunk)
		out = append(out, part...)
		return err
	})
	if err != nil {
		return nil, err
	}
	sortOldestFirst(out)
	return out, nil
}

func (r ClaimRepository) listLiveByTextKeysChunk(ctx context.Context, keys []string) ([]domain.Claim, error) {
	args := make([]any, len(keys))
	for i, k := range keys {
		args[i] = k
	}
	query := `SELECT ` + scanClaimColumns + ` FROM claims WHERE text_key IN (` +
		strings.TrimSuffix(strings.Repeat("?,", len(keys)), ",") + `) AND ` + liveClaimSQL
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list live claims by text key: %w", err)
	}
	defer closeRows(rows)

	var out []domain.Claim
	for rows.Next() {
		c, err := scanClaim(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate live claims by text key: %w", err)
	}
	return out, nil
}

// sortOldestFirst orders claims by created_at then id, the order the duplicate
// check relies on to pick the same canonical claim every time.
func sortOldestFirst(claims []domain.Claim) {
	sort.Slice(claims, func(i, j int) bool {
		if !claims[i].CreatedAt.Equal(claims[j].CreatedAt) {
			return claims[i].CreatedAt.Before(claims[j].CreatedAt)
		}
		return claims[i].ID < claims[j].ID
	})
}

// textKeyBackfillBatch bounds one backfill transaction, so a large legacy
// brain is keyed in steps that never hold the write lock for long.
const textKeyBackfillBatch = 1000

// backfillClaimTextKeys keys every claim written before text_key existed.
//
// It runs on every open rather than behind the schema-version gate: a row
// written by an older binary after this one first ran would otherwise stay
// unkeyed, and the duplicate check would never match it. On a keyed brain it
// is one indexed probe that finds nothing. Every key is non-empty, so each
// batch shrinks the remainder and the loop ends.
func backfillClaimTextKeys(db *sql.DB) error {
	for {
		n, err := backfillTextKeyBatch(db)
		if err != nil || n < textKeyBackfillBatch {
			return err
		}
	}
}

func backfillTextKeyBatch(db *sql.DB) (int, error) {
	rows, err := db.Query(`SELECT id, text FROM claims WHERE text_key = '' LIMIT ?`, textKeyBackfillBatch)
	if err != nil {
		return 0, fmt.Errorf("select unkeyed claims: %w", err)
	}
	keys := map[string]string{}
	for rows.Next() {
		var id, text string
		if err := rows.Scan(&id, &text); err != nil {
			closeRows(rows)
			return 0, fmt.Errorf("scan unkeyed claim: %w", err)
		}
		keys[id] = domain.ClaimTextKey(text)
	}
	closeRows(rows)
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate unkeyed claims: %w", err)
	}
	return len(keys), writeTextKeys(db, keys)
}

func writeTextKeys(db *sql.DB, keys map[string]string) error {
	if len(keys) == 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin text key backfill: %w", err)
	}
	defer rollbackTx(tx)
	for id, key := range keys {
		if _, err := tx.Exec(`UPDATE claims SET text_key = ? WHERE id = ?`, key, id); err != nil {
			return fmt.Errorf("key claim %s: %w", id, err)
		}
	}
	return tx.Commit()
}
