package mnemos

import (
	"context"
	"testing"
	"time"
)

// Only explicit confirmation may set LastConfirmed (ADR 0026), because it is
// the freshness reference canonical trust reads. Replay rehearsal bumps
// LastVerified too; if it also confirmed, every rehearsed belief would refresh
// its own trust, and stale knowledge that keeps being replayed would never be
// forgotten.
func TestConfirmation_ReplayRehearsesButOnlyAValidatedOutcomeConfirms(t *testing.T) {
	m := calibMem(t)
	ctx := context.Background()
	now := time.Now().UTC()

	seedClaim(t, m, "rehearsed", 0.9)
	seedClaim(t, m, "validated", 0.9)
	adjudicate(t, m, "ev", "validated", true, now)

	if _, err := m.replayFreshen(ctx, []string{"rehearsed"}); err != nil {
		t.Fatalf("replayFreshen: %v", err)
	}
	if _, err := m.reinforceValidatedClaims(ctx); err != nil {
		t.Fatalf("reinforceValidatedClaims: %v", err)
	}

	got := map[string]time.Time{}
	verified := map[string]time.Time{}
	claims, err := m.conn.Claims.ListByIDs(ctx, []string{"rehearsed", "validated"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range claims {
		got[c.ID], verified[c.ID] = c.LastConfirmed, c.LastVerified
	}
	if verified["rehearsed"].IsZero() {
		t.Fatal("fixture: replay did not rehearse the belief at all")
	}
	if !got["rehearsed"].IsZero() {
		t.Errorf("replay rehearsal set LastConfirmed = %v; rehearsal is not confirmation", got["rehearsed"])
	}
	if got["validated"].IsZero() {
		t.Error("a validated outcome did not confirm its belief")
	}
}
