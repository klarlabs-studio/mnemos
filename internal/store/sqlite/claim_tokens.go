package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/relate"
)

// claimTokenBuildBatch is how many claims one committed step of an index
// build covers.
const claimTokenBuildBatch = 5000

// ensureClaimTokens builds claim_tokens when the stored index was written
// under a different tokenizer than this binary's, or never written: a brain's
// first open on a version with the index backfills it, and a later tokenizer
// change rebuilds it. Tokens from another tokenizer would make candidates go
// missing without any error, so a mismatch is never used.
//
// The build commits every claimTokenBuildBatch claims and records a cursor, so
// a process killed part-way (a capture hook hitting its timeout) leaves
// progress the next open resumes. Backfill measured ~5.7s per 100k claims;
// until it completes, RelateCandidates reports ErrRelateCandidatesNotReady and
// the write path loads the corpus as it always did.
func ensureClaimTokens(db *sql.DB) error {
	var ready, building, cursor string
	err := db.QueryRow(`SELECT tokenizer_version, building_version, build_cursor FROM relate_token_state WHERE id = 1`).
		Scan(&ready, &building, &cursor)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if ready == relate.TokenizerVersion {
		return nil
	}
	if building != relate.TokenizerVersion {
		// Start over: rows from another tokenizer, or none at all.
		if _, err := db.Exec(`DELETE FROM claim_tokens`); err != nil {
			return fmt.Errorf("clear: %w", err)
		}
		if _, err := db.Exec(`INSERT INTO relate_token_state(id, tokenizer_version, building_version, build_cursor)
			VALUES (1, '', ?, '') ON CONFLICT(id) DO UPDATE SET tokenizer_version = '',
			building_version = excluded.building_version, build_cursor = ''`, relate.TokenizerVersion); err != nil {
			return err
		}
		cursor = ""
	}
	for {
		next, done, err := buildClaimTokenBatch(db, cursor)
		if err != nil {
			return err
		}
		if done {
			break
		}
		cursor = next
	}
	_, err = db.Exec(`UPDATE relate_token_state SET tokenizer_version = building_version, building_version = '', build_cursor = '' WHERE id = 1`)
	return err
}

// buildClaimTokenBatch indexes the next batch of claims after cursor (by id)
// in one transaction and advances the stored cursor with it.
func buildClaimTokenBatch(db *sql.DB, cursor string) (next string, done bool, err error) {
	tx, err := db.Begin()
	if err != nil {
		return "", false, err
	}
	defer rollbackTx(tx)
	rows, err := tx.Query(`SELECT id, text FROM claims WHERE id > ? ORDER BY id LIMIT ?`, cursor, claimTokenBuildBatch)
	if err != nil {
		return "", false, fmt.Errorf("read claims: %w", err)
	}
	type idText struct{ id, text string }
	var batch []idText
	for rows.Next() {
		var c idText
		if err := rows.Scan(&c.id, &c.text); err != nil {
			_ = rows.Close()
			return "", false, err
		}
		batch = append(batch, c)
	}
	if err := rows.Close(); err != nil {
		return "", false, err
	}
	if len(batch) == 0 {
		return cursor, true, nil
	}
	for _, c := range batch {
		toks := relate.SortedContentTokens(c.text)
		if len(toks) == 0 {
			continue
		}
		args := make([]any, 0, 2*len(toks))
		for _, tok := range toks {
			args = append(args, tok, c.id)
		}
		// OR IGNORE: a claim upserted during the build already has its rows.
		if _, err := tx.Exec(`INSERT OR IGNORE INTO claim_tokens(token, claim_id) VALUES `+
			strings.TrimSuffix(strings.Repeat("(?,?),", len(toks)), ","), args...); err != nil {
			return "", false, fmt.Errorf("index %s: %w", c.id, err)
		}
	}
	next = batch[len(batch)-1].id
	if _, err := tx.Exec(`UPDATE relate_token_state SET build_cursor = ? WHERE id = 1`, next); err != nil {
		return "", false, err
	}
	return next, false, tx.Commit()
}

// writeClaimTokens replaces the token rows of each claim inside tx. Called by
// every claim upsert, so a claim's tokens always match its current text.
// existed reports which claims were already stored: a fresh claim has no rows
// to clear, and skipping that delete is most of the cost on a bulk load. Each
// claim's tokens go in one multi-row insert.
func writeClaimTokens(ctx context.Context, tx *sql.Tx, claims []domain.Claim, existed map[string]bool) error {
	for _, c := range claims {
		if existed[c.ID] {
			if _, err := tx.ExecContext(ctx, `DELETE FROM claim_tokens WHERE claim_id = ?`, c.ID); err != nil {
				return fmt.Errorf("clear tokens of %s: %w", c.ID, err)
			}
		}
		toks := relate.SortedContentTokens(c.Text)
		if len(toks) == 0 {
			continue
		}
		args := make([]any, 0, 2*len(toks))
		for _, tok := range toks {
			args = append(args, tok, c.ID)
		}
		q := `INSERT OR IGNORE INTO claim_tokens(token, claim_id) VALUES ` +
			strings.TrimSuffix(strings.Repeat("(?,?),", len(toks)), ",")
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			return fmt.Errorf("index %s: %w", c.ID, err)
		}
	}
	return nil
}

// RelateCandidates implements ports.RelateCandidateSource: the claims
// q.SelectCandidates accepts, in relate.SortCandidates order.
func (r ClaimRepository) RelateCandidates(ctx context.Context, q relate.CandidateQuery) (ports.RelateCandidateSet, error) {
	var ready string
	if err := r.db.QueryRowContext(ctx, `SELECT tokenizer_version FROM relate_token_state WHERE id = 1`).Scan(&ready); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ports.RelateCandidateSet{}, fmt.Errorf("relate token state: %w", err)
	}
	if ready != relate.TokenizerVersion {
		return ports.RelateCandidateSet{}, ports.ErrRelateCandidatesNotReady
	}
	var ids []string
	seen := map[string]bool{}
	collect := func(query string, args ...any) error {
		rows, err := r.db.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer closeRows(rows)
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
		return rows.Err()
	}
	// Document frequencies, so the budget can plan rarest-first, then the
	// postings of only the tokens the plan takes. A skipped common token's
	// posting list (half the brain, on a large one) is never read.
	//
	// A frequency only matters up to the budget: a token carried by more
	// claims can never fit, so its exact count changes neither the tokens taken
	// nor the skipped count. Counting stops at budget+1, which keeps a common
	// token's count from walking its whole posting list (~500k entries at 1M
	// beliefs) on every write. Unlimited needs no frequencies at all.
	df := map[string]int{}
	if bound := q.BudgetBound(); bound > 0 {
		for _, tok := range q.Tokens {
			var n int
			if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT 1 FROM claim_tokens WHERE token = ? LIMIT ?)`, tok, bound).Scan(&n); err != nil {
				return ports.RelateCandidateSet{}, fmt.Errorf("relate token frequency: %w", err)
			}
			df[tok] = n
		}
	}
	take, skipped := q.PlanTokens(df)
	for start := 0; start < len(take); start += 500 {
		chunk := take[start:min(start+500, len(take))]
		if err := collect(`SELECT DISTINCT claim_id FROM claim_tokens WHERE token IN (`+placeholders(len(chunk))+`)`, anyArgs(chunk)...); err != nil {
			return ports.RelateCandidateSet{}, fmt.Errorf("relate candidates by token: %w", err)
		}
	}
	for start := 0; start < len(q.CitedIDs); start += 500 {
		chunk := q.CitedIDs[start:min(start+500, len(q.CitedIDs))]
		if err := collect(`SELECT id FROM claims WHERE id IN (`+placeholders(len(chunk))+`)`, anyArgs(chunk)...); err != nil {
			return ports.RelateCandidateSet{}, fmt.Errorf("relate candidates by citation: %w", err)
		}
	}
	if err := collect(`SELECT id FROM claims WHERE type = ? AND test_requirement_ref <> ''`, string(domain.ClaimTypeTestResult)); err != nil {
		return ports.RelateCandidateSet{}, fmt.Errorf("relate candidates by test requirement: %w", err)
	}
	claims, err := r.ListByIDs(ctx, ids)
	if err != nil {
		return ports.RelateCandidateSet{}, err
	}
	relate.SortCandidates(claims)
	return ports.RelateCandidateSet{Claims: claims, SkippedTokens: skipped}, nil
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func anyArgs(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}
