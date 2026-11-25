package govwrite

import (
	"context"
	"fmt"
	"time"

	axidomain "go.klarlabs.de/axi/domain"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
)

// DirectWriter runs this package's write executors against a conn WITHOUT
// this package's kernel. It exists for the root library, whose public writes
// are already governed by the library's own kernel: a library executor that
// needs a claim upsert, a verification or a feedback write calls these so the
// library and the daemon persist through the same executor bodies, exactly as
// [ApplyRetraction] does for retraction. Every other caller uses [Writer].
//
// Its method set mirrors the [Writer] methods of the same names, so code
// written against an interface of them (internal/beliefops) runs on either.
type DirectWriter struct{ conn *store.Conn }

// Direct returns a [DirectWriter] over conn.
func Direct(conn *store.Conn) *DirectWriter { return &DirectWriter{conn: conn} }

// Conn returns the wrapped connection for reads, like [Writer.Conn].
func (d *DirectWriter) Conn() *store.Conn { return d.conn }

// runDirect executes exec with input boxed the way [dispatch] boxes it and
// returns the typed result.
func runDirect[Out any](ctx context.Context, action string, exec axidomain.ActionExecutor, input any) (Out, error) {
	var zero Out
	res, _, err := exec.Execute(ctx, map[string]any{inputPayloadKey: input}, nil)
	if err != nil {
		return zero, fmt.Errorf("govwrite: %s: %w", action, err)
	}
	typed, ok := res.Data.(Out)
	if !ok {
		return zero, fmt.Errorf("govwrite: %s: unexpected result type %T", action, res.Data)
	}
	return typed, nil
}

// Claims mirrors [Writer.Claims].
func (d *DirectWriter) Claims(ctx context.Context, claims []domain.Claim, reason ClaimReason) (int, error) {
	out, err := runDirect[writeCount](ctx, actionWriteClaims, claimsExecutor(*d), claimsInput{Claims: claims, Reason: reason})
	return out.Accepted, err
}

// MarkVerified mirrors [Writer.MarkVerified].
func (d *DirectWriter) MarkVerified(ctx context.Context, claimID string, verifiedAt time.Time, halfLifeDays float64) error {
	_, err := runDirect[string](ctx, actionMarkVerified, markVerifiedExecutor(*d), markVerifiedInput{
		ClaimID: claimID, VerifiedAt: verifiedAt, HalfLifeDays: halfLifeDays,
	})
	return err
}

// SetValidity mirrors [Writer.SetValidity].
func (d *DirectWriter) SetValidity(ctx context.Context, claimID string, validTo time.Time, why ClaimReason) error {
	_, err := runDirect[string](ctx, actionSetValidity, setValidityExecutor(*d), setValidityInput{
		ClaimID: claimID, ValidTo: validTo, Why: why,
	})
	return err
}

// Feedback mirrors [Writer.Feedback].
func (d *DirectWriter) Feedback(ctx context.Context, state domain.ClaimFeedback) (string, error) {
	return runDirect[string](ctx, actionWriteFeedback, feedbackExecutor(*d), feedbackInput{State: state})
}
