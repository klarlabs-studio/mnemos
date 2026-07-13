package main

import (
	"context"
	"testing"

	mnemos "go.klarlabs.de/mnemos"
)

// The connected-brain MCP tool handlers run against the library facade and
// return well-formed (empty) results over an empty store — this exercises the
// delegation + output shaping without a full MCP stdio round-trip.
func TestMCPCognitiveHandlers(t *testing.T) {
	mem, err := mnemos.New(mnemos.WithStorage("memory://"))
	if err != nil {
		t.Fatalf("build facade: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	ctx := context.Background()

	wk, err := mcpWhoKnows(ctx, mem, mcpWhoKnowsInput{Query: "creatinine", Limit: 5})
	if err != nil || wk.Experts == nil {
		t.Fatalf("who_knows: %v %+v", err, wk)
	}
	if kg, err := mcpKnowledgeGaps(ctx, mem, mcpKnowledgeGapsInput{}); err != nil || kg.Gaps == nil {
		t.Fatalf("knowledge_gaps: %v %+v", err, kg)
	}
	if _, err := mcpCalibration(ctx, mem, struct{}{}); err != nil {
		t.Fatalf("calibration: %v", err)
	}
	if pe, err := mcpPredictiveError(ctx, mem, struct{}{}); err != nil || len(pe.Levels) != 4 {
		t.Fatalf("predictive_error: %v %+v", err, pe)
	}
	if hc, err := mcpHypercorrections(ctx, mem, struct{}{}); err != nil || hc.Hypercorrections == nil {
		t.Fatalf("hypercorrections: %v %+v", err, hc)
	}
	if rc, err := mcpRecombinations(ctx, mem, mcpRecombinationsInput{}); err != nil || rc.Recombinations == nil {
		t.Fatalf("recombinations: %v %+v", err, rc)
	}
	an, err := mcpAnalogousClaims(ctx, mem, mcpAnalogousInput{ClaimID: "cl1", Limit: 3})
	if err != nil || an.Analogous == nil || an.ClaimID != "cl1" {
		t.Fatalf("analogous_claims: %v %+v", err, an)
	}
}
