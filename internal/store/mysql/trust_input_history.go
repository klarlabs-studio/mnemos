package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// trustInputs is the stored-on-the-row part of a claim's trust input
// (confidence, half_life_days, last_confirmed).
type trustInputs struct {
	found         bool
	confidence    float64
	halfLifeDays  float64
	lastConfirmed sql.NullTime
	credit        float64
}

// sameInputs reports whether the claims-row trust inputs are unchanged.
func (a trustInputs) sameInputs(b trustInputs) bool {
	return a.confidence == b.confidence && a.halfLifeDays == b.halfLifeDays &&
		a.lastConfirmed.Valid == b.lastConfirmed.Valid &&
		(!a.lastConfirmed.Valid || a.lastConfirmed.Time.Equal(b.lastConfirmed.Time))
}

// readTrustInputs reads claimID's stored trust inputs inside tx, locking the
// row so concurrent writers each record the values they replaced. found is
// false when the claim does not exist yet.
func readTrustInputs(ctx context.Context, tx *sql.Tx, claimID string) (trustInputs, error) {
	in := trustInputs{found: true}
	var components sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT confidence, half_life_days, last_confirmed, confidence_components FROM claims WHERE id = ? FOR UPDATE`, claimID,
	).Scan(&in.confidence, &in.halfLifeDays, &in.lastConfirmed, &components)
	if errors.Is(err, sql.ErrNoRows) {
		return trustInputs{}, nil
	}
	if err != nil {
		return trustInputs{}, fmt.Errorf("read trust inputs of %s: %w", claimID, err)
	}
	in.credit = domain.AppliedCredit(decodeConfidenceComponents(components.String))
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
// statement applied. A claim the write created has no prior values and
// records nothing.
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
	if now.credit != prior.credit {
		if _, err := tx.ExecContext(ctx, `INSERT INTO claim_credit_history (claim_id, from_credit, to_credit, changed_at) VALUES (?, ?, ?, ?)`,
			claimID, prior.credit, now.credit, at.UTC()); err != nil {
			return fmt.Errorf("record credit change of %s: %w", claimID, err)
		}
	}
	if now.sameInputs(prior) {
		return nil
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO claim_trust_input_history (claim_id, prior_confidence, prior_half_life_days, prior_last_confirmed, changed_at) VALUES (?, ?, ?, ?, ?)`,
		claimID, prior.confidence, prior.halfLifeDays, prior.lastConfirmed, at.UTC(),
	); err != nil {
		return fmt.Errorf("record trust input change of %s: %w", claimID, err)
	}
	return nil
}

// versionedTrustInputWrite runs write, a single-claim statement that may change
// claimID's stored trust inputs, in a transaction that versions the change.
func (r ClaimRepository) versionedTrustInputWrite(ctx context.Context, claimID string, write func(*sql.Tx) error) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin trust input write tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	prior, err := readTrustInputs(ctx, tx, claimID)
	if err != nil {
		return err
	}
	if err := write(tx); err != nil {
		return err
	}
	if err := recordTrustInputChange(ctx, tx, claimID, prior, time.Now().UTC()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit trust input write tx: %w", err)
	}
	return nil
}

// ListTrustInputHistory implements [ports.TrustInputHistoryLister]: the
// trust-input changes of claimIDs, oldest first.
func (r ClaimRepository) ListTrustInputHistory(ctx context.Context, claimIDs []string) ([]domain.TrustInputChange, error) {
	out := make([]domain.TrustInputChange, 0)
	if len(claimIDs) == 0 {
		return out, nil
	}
	placeholders, args := inPlaceholders(claimIDs)
	//nolint:gosec // G202: placeholders are literal "?" tokens, not user input
	rows, err := r.db.QueryContext(ctx, `SELECT claim_id, prior_confidence, prior_half_life_days, prior_last_confirmed, changed_at
		FROM claim_trust_input_history WHERE claim_id IN (`+placeholders+`) ORDER BY changed_at, id`, args...)
	if err != nil {
		return nil, fmt.Errorf("list trust input history: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			c         domain.TrustInputChange
			confirmed sql.NullTime
		)
		if err := rows.Scan(&c.ClaimID, &c.Confidence, &c.HalfLifeDays, &confirmed, &c.ChangedAt); err != nil {
			return nil, fmt.Errorf("scan trust input history row: %w", err)
		}
		if confirmed.Valid {
			c.LastConfirmed = confirmed.Time.UTC()
		}
		c.ChangedAt = c.ChangedAt.UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}
