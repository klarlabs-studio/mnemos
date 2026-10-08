package sqlite

import (
	"context"
	"fmt"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
)

// openValiditySQL is the population knowledge gaps sweep: valid time open.
// Unlike brain health's live beliefs it keeps deprecated claims.
const openValiditySQL = `(valid_to IS NULL OR valid_to = '')`

// gapScanFraction is the share of open claims above which the candidates are
// read with one ordered scan instead of fetched by id. On a contradiction-dense
// brain (2M contradictions over 1M claims) nearly every claim is contested, and
// fetching ~900k claims in id batches cost more than reading them all: 20.4 s
// against 11.7 s for the full sweep. A variable so the equivalence test can
// force either route.
var gapScanFraction = 0.25

// GapCandidates implements ports.GapCandidateSource. Contradiction counts are
// per endpoint, both directions, whatever the other endpoint's state, as
// KnowledgeGaps counted them.
func (r ClaimRepository) GapCandidates(ctx context.Context, minContradictions int) (ports.GapCandidates, error) {
	out := ports.GapCandidates{Contradictions: map[string]int{}, Evidence: map[string]int{}}
	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM claims WHERE `+openValiditySQL).Scan(&out.OpenClaims); err != nil {
		return out, fmt.Errorf("count open claims: %w", err)
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT claim_id, count(*) FROM (
			SELECT from_claim_id AS claim_id FROM relationships WHERE type = 'contradicts'
			UNION ALL
			SELECT to_claim_id FROM relationships WHERE type = 'contradicts'
		) GROUP BY claim_id`)
	if err != nil {
		return out, fmt.Errorf("count contradictions: %w", err)
	}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			closeRows(rows)
			return out, err
		}
		out.Contradictions[id] = n
	}
	closeRows(rows)
	if err := rows.Err(); err != nil {
		return out, err
	}
	ids := map[string]struct{}{}
	hyp, err := r.db.QueryContext(ctx, `SELECT id FROM claims WHERE type = ? AND `+openValiditySQL, string(domain.ClaimTypeHypothesis))
	if err != nil {
		return out, fmt.Errorf("list hypotheses: %w", err)
	}
	for hyp.Next() {
		var id string
		if err := hyp.Scan(&id); err != nil {
			closeRows(hyp)
			return out, err
		}
		ids[id] = struct{}{}
	}
	closeRows(hyp)
	if err := hyp.Err(); err != nil {
		return out, err
	}
	for id, n := range out.Contradictions {
		if n >= minContradictions {
			ids[id] = struct{}{}
		}
	}
	list := make([]string, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	if float64(len(list)) > gapScanFraction*float64(out.OpenClaims) {
		return r.gapCandidatesByScan(ctx, out, ids)
	}
	for start := 0; start < len(list); start += 5000 {
		chunk := list[start:min(start+5000, len(list))]
		cs, err := r.ListByIDs(ctx, chunk)
		if err != nil {
			return out, err
		}
		for _, c := range cs {
			if c.ValidTo.IsZero() {
				out.Claims = append(out.Claims, c)
			}
		}
		ev, err := r.db.QueryContext(ctx, `SELECT claim_id, count(*) FROM claim_evidence WHERE claim_id IN (`+placeholders(len(chunk))+`) GROUP BY claim_id`, anyArgs(chunk)...)
		if err != nil {
			return out, fmt.Errorf("count evidence: %w", err)
		}
		for ev.Next() {
			var id string
			var n int
			if err := ev.Scan(&id, &n); err != nil {
				closeRows(ev)
				return out, err
			}
			out.Evidence[id] = n
		}
		closeRows(ev)
		if err := ev.Err(); err != nil {
			return out, err
		}
	}
	return out, nil
}

// gapCandidatesByScan reads every claim once and keeps the candidates, with
// evidence counts from one grouped pass. Same result as the by-id route.
func (r ClaimRepository) gapCandidatesByScan(ctx context.Context, out ports.GapCandidates, ids map[string]struct{}) (ports.GapCandidates, error) {
	all, err := r.ListAll(ctx)
	if err != nil {
		return out, err
	}
	for _, c := range all {
		if _, ok := ids[c.ID]; ok && c.ValidTo.IsZero() {
			out.Claims = append(out.Claims, c)
		}
	}
	rows, err := r.db.QueryContext(ctx, `SELECT claim_id, count(*) FROM claim_evidence GROUP BY claim_id`)
	if err != nil {
		return out, fmt.Errorf("count evidence: %w", err)
	}
	defer closeRows(rows)
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return out, err
		}
		if _, ok := ids[id]; ok {
			out.Evidence[id] = n
		}
	}
	return out, rows.Err()
}
