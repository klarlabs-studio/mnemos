package main

import (
	"context"
	"testing"

	mnemos "go.klarlabs.de/mnemos"
)

func TestMCPClaimReadsAndRecall(t *testing.T) {
	mem, err := mnemos.New(mnemos.WithStorage("memory://"))
	if err != nil {
		t.Fatalf("build facade: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	ctx := context.Background()

	// get_claim / get_decision on a missing id surface an error.
	if _, err := mcpGetClaim(ctx, mem, mcpGetClaimInput{ClaimID: "nope"}); err == nil {
		t.Error("get_claim missing: want error")
	}
	if _, err := mcpGetDecision(ctx, mem, mcpGetDecisionInput{ID: "nope"}); err == nil {
		t.Error("get_decision missing: want error")
	}

	// classify returns a verdict.
	if _, err := mcpClassify(ctx, mem, mcpClassifyInput{Text: "a brand new statement"}); err != nil {
		t.Errorf("classify: %v", err)
	}

	// recall over all modes returns well-formed output; unknown mode errors.
	for _, mode := range []string{"sufficiency", "effort", "context", "conflicts", "iterative"} {
		out, err := mcpRecall(ctx, mem, mcpRecallInput{Query: "x", Mode: mode})
		if err != nil {
			t.Errorf("recall mode=%s: %v", mode, err)
			continue
		}
		if out.Mode != mode || out.Results == nil {
			t.Errorf("recall mode=%s: %+v", mode, out)
		}
	}
	if _, err := mcpRecall(ctx, mem, mcpRecallInput{Query: "x", Mode: "bogus"}); err == nil {
		t.Error("recall bad mode: want error")
	}
}
