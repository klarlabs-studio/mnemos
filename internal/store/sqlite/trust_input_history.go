package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// trustInputs is the stored-on-the-row part of a claim's trust input
// (confidence, half_life_days, last_confirmed) as the column values hold it.
type trustInputs struct {
	found         bool
	confidence    float64
	halfLifeDays  float64
	lastConfirmed string
	credit        float64
}

// sameInputs reports whether the claims-row trust inputs are unchanged.
func (a trustInputs) sameInputs(b trustInputs) bool {
	return a.confidence == b.confidence && a.halfLifeDays == b.halfLifeDays && a.lastConfirmed == b.lastConfirmed
}

// readTrustInputs reads claimID's stored trust inputs inside tx. found is
// false when the claim does not exist yet.
func readTrustInputs(ctx context.Context, tx *sql.Tx, claimID string) (trustInputs, error) {
	in := trustInputs{found: true}
	var components string
	err := tx.QueryRowContext(ctx,
		`SELECT confidence, half_life_days, last_confirmed, confidence_components FROM claims WHERE id = ?`, claimID,
	).Scan(&in.confidence, &in.halfLifeDays, &in.lastConfirmed, &components)
	if errors.Is(err, sql.ErrNoRows) {
		return trustInputs{}, nil
	}
	if err != nil {
		return trustInputs{}, fmt.Errorf("read trust inputs of %s: %w", claimID, err)
	}
	in.credit = domain.AppliedCredit(decodeConfidenceComponents(components))
	return in, nil
}

// recordTrustInputChange versions a write to claimID's stored trust inputs
// (ADR 0036 §7). Call it in the write's transaction, after the write, with the
// values read before it: it reads what the row holds now and, when confidence,
// half-life or last confirmation differ, records their prior values in
// claim_trust_input_history; when the applied net credit differs, it records
// the change in claim_credit_history (ADR 0036 step 3), whichever statement
// made it — ApplyBeliefCredit or an upsert carrying confidence_components. Comparing against the row rather than
// against what the caller wrote keeps it right whatever conflict rule the
// statement applied. A claim the write created (prior not found) has no prior
// values and records nothing.
//
// Every statement that writes confidence, half_life_days or last_confirmed
// must reach this; TestTrustInputWriters_RecordHistory fails otherwise.
func recordTrustInputChange(ctx context.Context, tx *sql.Tx, claimID string, prior trustInputs, at time.Time) error {
	if !prior.found {
		return nil
	}
	now, err := readTrustInputs(ctx, tx, claimID)
	if err != nil {
		return err
	}
	if !now.found {
		return nil
	}
	stamp := at.UTC().Format(time.RFC3339Nano)
	if !now.sameInputs(prior) {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO claim_trust_input_history (claim_id, prior_confidence, prior_half_life_days, prior_last_confirmed, changed_at) VALUES (?, ?, ?, ?, ?)`,
			claimID, prior.confidence, prior.halfLifeDays, prior.lastConfirmed, stamp,
		); err != nil {
			return fmt.Errorf("record trust input change of %s: %w", claimID, err)
		}
	}
	if now.credit != prior.credit {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO claim_credit_history (claim_id, from_credit, to_credit, changed_at) VALUES (?, ?, ?, ?)`,
			claimID, prior.credit, now.credit, stamp,
		); err != nil {
			return fmt.Errorf("record credit change of %s: %w", claimID, err)
		}
	}
	return nil
}

// ListTrustInputHistory implements [ports.TrustInputHistoryLister]: the
// trust-input changes of claimIDs, oldest first.
func (r ClaimRepository) ListTrustInputHistory(ctx context.Context, claimIDs []string) ([]domain.TrustInputChange, error) {
	out := make([]domain.TrustInputChange, 0)
	err := inChunks(claimIDs, func(chunk []string) error {
		part, err := r.listTrustInputHistoryChunk(ctx, chunk)
		out = append(out, part...)
		return err
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ChangedAt.Before(out[j].ChangedAt) })
	return out, nil
}

func (r ClaimRepository) listTrustInputHistoryChunk(ctx context.Context, ids []string) ([]domain.TrustInputChange, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT claim_id, prior_confidence, prior_half_life_days, prior_last_confirmed, changed_at FROM claim_trust_input_history WHERE claim_id IN (`+placeholders(len(ids))+`) ORDER BY id`, //nolint:gosec // G202: placeholders are literal "?" strings
		anyArgs(ids)...)
	if err != nil {
		return nil, fmt.Errorf("list trust input history: %w", err)
	}
	defer closeRows(rows)
	var out []domain.TrustInputChange
	for rows.Next() {
		var (
			c                 domain.TrustInputChange
			confirmed, change string
		)
		if err := rows.Scan(&c.ClaimID, &c.Confidence, &c.HalfLifeDays, &confirmed, &change); err != nil {
			return nil, fmt.Errorf("scan trust input history row: %w", err)
		}
		if c.LastConfirmed, err = parseOptionalTime(confirmed); err != nil {
			return nil, fmt.Errorf("parse prior_last_confirmed of %s: %w", c.ClaimID, err)
		}
		if c.ChangedAt, err = time.Parse(time.RFC3339Nano, change); err != nil {
			return nil, fmt.Errorf("parse trust input changed_at of %s: %w", c.ClaimID, err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate trust input history rows: %w", err)
	}
	return out, nil
}
