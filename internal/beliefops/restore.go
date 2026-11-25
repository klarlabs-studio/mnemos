package beliefops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/store"
)

// Restoration is the receipt of [Restore].
type Restoration struct {
	ClaimID   string `json:"belief_id"`
	OldStatus string `json:"old_status"`
	NewStatus string `json:"new_status"`
	// ReopenedValidity is true when the belief's valid time had been closed
	// and Restore opened it again.
	ReopenedValidity bool `json:"reopened_validity"`
	// HeldBy names the live belief that already holds this one's text, scope
	// and audience — typically the claim written when the belief was learned
	// again (#393). Restore then writes nothing: two live copies of one belief
	// are the duplicate the write path exists to prevent.
	HeldBy string `json:"held_by,omitempty"`
}

// Restore brings a retired belief back: the undo of forget, of deprecate,
// and of a valid-time close (#393). A deprecated belief returns to the status
// it held before it was deprecated (active when the history does not say);
// a closed valid-time interval is opened again. Both are recorded with the
// reason (ReasonRestore when none is given) in the status and validity
// histories, so the belief's past stays readable.
//
// A belief that is already live is reported unchanged. A merge redirect is
// refused (ErrInvalid): its belief lives on in the claim it was merged into.
// When another live claim holds the same belief, nothing is written and
// HeldBy names it.
func Restore(ctx context.Context, conn *store.Conn, w Writer, e Edit) (Restoration, error) {
	claims, err := loadClaims(ctx, conn, strings.TrimSpace(e.ClaimID))
	if err != nil {
		return Restoration{}, err
	}
	c := claims[0]
	out := Restoration{ClaimID: c.ID, OldStatus: string(c.Status), NewStatus: string(c.Status)}
	if err := restorable(c); err != nil {
		return Restoration{}, err
	}
	deprecated := c.Status == domain.ClaimStatusDeprecated
	closed := !c.ValidTo.IsZero()
	if !deprecated && !closed {
		return out, nil
	}
	holder, err := liveHolder(ctx, conn, c)
	if err != nil {
		return Restoration{}, err
	}
	if holder != "" {
		out.HeldBy = holder
		return out, nil
	}
	why := govwrite.ClaimReason{Reason: reasonOr(e.Reason, ReasonRestore), ChangedBy: e.Actor}
	if closed {
		if err := w.SetValidity(ctx, c.ID, time.Time{}, why); err != nil {
			return Restoration{}, fmt.Errorf("reopen valid time: %w", err)
		}
		out.ReopenedValidity = true
	}
	if deprecated {
		prior, err := statusBeforeDeprecation(ctx, conn, c.ID)
		if err != nil {
			return Restoration{}, err
		}
		c.Status = prior
		c.ValidTo = time.Time{}
		why.AssertStatus = true
		if _, err := w.Claims(ctx, []domain.Claim{c}, why); err != nil {
			return Restoration{}, fmt.Errorf("restore status: %w", err)
		}
		out.NewStatus = string(prior)
	}
	return out, nil
}

// liveHolder returns the id of another live claim stating c's belief in c's
// scope and audience, or "".
func liveHolder(ctx context.Context, conn *store.Conn, c domain.Claim) (string, error) {
	live, err := conn.Claims.ListLiveByTextKeys(ctx, []string{domain.ClaimTextKey(c.Text)})
	if err != nil {
		return "", fmt.Errorf("look up live holder: %w", err)
	}
	for _, l := range live {
		if l.ID != c.ID && l.Scope == c.Scope && visibilityOf(l) == visibilityOf(c) {
			return l.ID, nil
		}
	}
	return "", nil
}

func visibilityOf(c domain.Claim) domain.Visibility {
	if c.Visibility == "" {
		return domain.DefaultVisibility
	}
	return c.Visibility
}

// statusBeforeDeprecation reads the status the most recent move into
// deprecated came from. A history that does not say (no transition, an
// initial deprecated write, or a prior status that is itself deprecated)
// gives active.
func statusBeforeDeprecation(ctx context.Context, conn *store.Conn, claimID string) (domain.ClaimStatus, error) {
	hist, err := conn.Claims.ListStatusHistoryByClaimID(ctx, claimID)
	if err != nil {
		return "", fmt.Errorf("read status history: %w", err)
	}
	for i := len(hist) - 1; i >= 0; i-- {
		t := hist[i]
		if t.ToStatus == domain.ClaimStatusDeprecated && t.FromStatus != domain.ClaimStatusDeprecated {
			if t.FromStatus == "" {
				break
			}
			return t.FromStatus, nil
		}
	}
	return domain.ClaimStatusActive, nil
}

// RequireRestorable reports ErrInvalid when the named belief cannot be
// restored (a merge redirect), so callers whose kernel flattens errors to text
// can check before dispatch, as with [Require].
func RequireRestorable(ctx context.Context, conn *store.Conn, claimID string) error {
	claims, err := loadClaims(ctx, conn, strings.TrimSpace(claimID))
	if err != nil {
		return err
	}
	return restorable(claims[0])
}

func restorable(c domain.Claim) error {
	if c.MergedInto != "" {
		return invalid("belief %s was merged into %s, which holds it", c.ID, c.MergedInto)
	}
	return nil
}
