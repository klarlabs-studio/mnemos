package beliefops_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/beliefops"
	"go.klarlabs.de/mnemos/internal/domain"
)

// Forget, then restore: the belief is live again with the status it had
// before, and the round trip is in its history (#393).
func TestRestore_UndoesForgetToThePriorStatus(t *testing.T) {
	ctx := context.Background()
	for _, f := range fixtures(t) {
		seed(t, f.conn, "c-r", "The audit log is kept for 400 days")
		c := stored(t, f.conn, "c-r")
		c.Status = domain.ClaimStatusContested
		if err := f.conn.Claims.Upsert(ctx, []domain.Claim{c}); err != nil {
			t.Fatal(err)
		}
		if _, err := beliefops.Deprecate(ctx, f.conn, f.w, beliefops.Edit{ClaimID: "c-r", Actor: "agent"}, beliefops.ReasonForget); err != nil {
			t.Fatalf("%s: forget: %v", f.name, err)
		}

		r, err := beliefops.Restore(ctx, f.conn, f.w, beliefops.Edit{ClaimID: "c-r", Actor: "agent"})
		if err != nil {
			t.Fatalf("%s: restore: %v", f.name, err)
		}
		if r.OldStatus != "deprecated" || r.NewStatus != "contested" || r.ReopenedValidity || r.HeldBy != "" {
			t.Errorf("%s: receipt = %+v, want deprecated -> contested", f.name, r)
		}
		if got := stored(t, f.conn, "c-r").Status; got != domain.ClaimStatusContested {
			t.Errorf("%s: stored status = %s, want contested", f.name, got)
		}
		hist, err := f.conn.Claims.ListStatusHistoryByClaimID(ctx, "c-r")
		if err != nil || len(hist) == 0 || hist[len(hist)-1].Reason != beliefops.ReasonRestore {
			t.Errorf("%s: last transition = %+v (%v), want the restore with its default reason", f.name, hist, err)
		}
	}
}

// A belief whose valid time was closed is opened again, and the reopening is
// in its validity history.
func TestRestore_ReopensClosedValidTime(t *testing.T) {
	ctx := context.Background()
	for _, f := range fixtures(t) {
		seed(t, f.conn, "c-v", "The staging cluster runs Kubernetes 1.31")
		if err := f.conn.Claims.SetValidity(ctx, "c-v", time.Now().UTC().Add(-time.Hour), domain.ChangeNote{Reason: "superseded"}); err != nil {
			t.Fatal(err)
		}

		r, err := beliefops.Restore(ctx, f.conn, f.w, beliefops.Edit{ClaimID: "c-v", Reason: "still true", Actor: "agent"})
		if err != nil {
			t.Fatalf("%s: restore: %v", f.name, err)
		}
		if !r.ReopenedValidity || r.NewStatus != "active" {
			t.Errorf("%s: receipt = %+v, want valid time reopened", f.name, r)
		}
		if vt := stored(t, f.conn, "c-v").ValidTo; !vt.IsZero() {
			t.Errorf("%s: valid_to = %v after restore, want open", f.name, vt)
		}
		hist, err := f.conn.Claims.ListValidityHistory(ctx, "c-v")
		if err != nil || len(hist) == 0 || hist[len(hist)-1].Reason != "still true" {
			t.Errorf("%s: validity history = %+v (%v), want the reopening with its reason", f.name, hist, err)
		}
	}
}

// When the belief has been learned again, the new claim holds it: restoring
// the old one would make two live copies, so Restore names the holder and
// writes nothing. A live belief is reported unchanged; a merge redirect and an
// unknown id are refused.
func TestRestore_RefusesWhatItCannotRestore(t *testing.T) {
	ctx := context.Background()
	for _, f := range fixtures(t) {
		seed(t, f.conn, "c-old", "Releases are cut on Thursdays")
		if _, err := beliefops.Deprecate(ctx, f.conn, f.w, beliefops.Edit{ClaimID: "c-old"}, beliefops.ReasonForget); err != nil {
			t.Fatal(err)
		}
		seed(t, f.conn, "c-new", "releases are cut on thursdays.")

		r, err := beliefops.Restore(ctx, f.conn, f.w, beliefops.Edit{ClaimID: "c-old"})
		if err != nil || r.HeldBy != "c-new" || r.NewStatus != "deprecated" {
			t.Errorf("%s: restore of a relearned belief = %+v (%v), want held by c-new and no write", f.name, r, err)
		}
		if got := stored(t, f.conn, "c-old").Status; got != domain.ClaimStatusDeprecated {
			t.Errorf("%s: c-old status = %s, want still deprecated", f.name, got)
		}

		if r, err := beliefops.Restore(ctx, f.conn, f.w, beliefops.Edit{ClaimID: "c-new"}); err != nil || r.NewStatus != "active" || r.ReopenedValidity {
			t.Errorf("%s: restore of a live belief = %+v (%v), want unchanged", f.name, r, err)
		}

		seed(t, f.conn, "c-merged", "Every release is cut on a Thursday")
		m := stored(t, f.conn, "c-merged")
		m.Status, m.MergedInto = domain.ClaimStatusDeprecated, "c-new"
		if err := f.conn.Claims.Upsert(ctx, []domain.Claim{m}); err != nil {
			t.Fatal(err)
		}
		if _, err := beliefops.Restore(ctx, f.conn, f.w, beliefops.Edit{ClaimID: "c-merged"}); !errors.Is(err, beliefops.ErrInvalid) {
			t.Errorf("%s: restore of a merge redirect = %v, want ErrInvalid", f.name, err)
		}
		if _, err := beliefops.Restore(ctx, f.conn, f.w, beliefops.Edit{ClaimID: "c-nope"}); !errors.Is(err, beliefops.ErrNotFound) {
			t.Errorf("%s: restore of an unknown id = %v, want ErrNotFound", f.name, err)
		}
	}
}
