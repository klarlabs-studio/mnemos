package mnemos

import (
	"context"
	"testing"
)

// TestClassifyClaim_FitVsNovel verifies the assimilation routing: a claim whose
// topic is covered by established (sufficient) knowledge is assimilation against an
// anchor; an unrelated claim is novel accommodation.
func TestClassifyClaim_FitVsNovel(t *testing.T) {
	m := calibMem(t)
	ctx := context.Background()
	for _, txt := range []string{
		"The checkout service latency spikes after a deploy.",
		"Checkout latency spikes correlate with the deploy rollout.",
		"After each checkout deploy, latency spikes for ten minutes.",
		"Deploys to checkout cause a transient latency spike.",
		"Checkout latency returns to baseline once the deploy settles.",
	} {
		seedTextClaim(t, m, txt)
	}

	fit, err := m.ClassifyClaim(ctx, "checkout deploy latency spike")
	if err != nil {
		t.Fatalf("ClassifyClaim (fit): %v", err)
	}
	if fit.Kind != AssimilationFits {
		t.Errorf("established topic should assimilate; got %+v", fit)
	}
	if fit.AnchorID == "" {
		t.Errorf("assimilation should name an anchor claim; got %+v", fit)
	}

	novel, err := m.ClassifyClaim(ctx, "quarterly marketing budget forecast")
	if err != nil {
		t.Fatalf("ClassifyClaim (novel): %v", err)
	}
	if novel.Kind != AssimilationNovel {
		t.Errorf("an unrelated topic should be novel accommodation; got %+v", novel)
	}
}
