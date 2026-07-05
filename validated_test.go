package mnemos

import (
	"context"
	"testing"
	"time"
)

// TestConsolidate_ReinforceValidated verifies the confirmation half of surprise
// routing: a claim whose latest verdict CONFIRMED it is freshened (resists
// forgetting), while a refuted or un-adjudicated claim is not.
func TestConsolidate_ReinforceValidated(t *testing.T) {
	m := calibMem(t)
	ctx := context.Background()
	now := time.Now().UTC()

	seedClaim(t, m, "cv", 0.9) // validated
	adjudicate(t, m, "ev", "cv", true, now)
	seedClaim(t, m, "cr", 0.9) // refuted
	adjudicate(t, m, "er", "cr", false, now)
	seedClaim(t, m, "cn", 0.9) // no verdict

	res, err := m.Consolidate(ctx, ConsolidateOptions{ReinforceValidated: true})
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	if res.Validated != 1 {
		t.Fatalf("Validated = %d, want 1 (only cv)", res.Validated)
	}

	claims, err := m.conn.Claims.ListByIDs(ctx, []string{"cv", "cr", "cn"})
	if err != nil {
		t.Fatal(err)
	}
	vc := map[string]int{}
	for _, c := range claims {
		vc[c.ID] = c.VerifyCount
	}
	if vc["cv"] != 1 {
		t.Errorf("validated claim should be freshened (VerifyCount 1); got %d", vc["cv"])
	}
	if vc["cr"] != 0 || vc["cn"] != 0 {
		t.Errorf("only validated claims freshened; cr=%d cn=%d, want 0/0", vc["cr"], vc["cn"])
	}
}
