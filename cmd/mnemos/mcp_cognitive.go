package main

import (
	"context"

	mnemos "go.klarlabs.de/mnemos"
)

// Connected-brain MCP tools (parity with the HTTP /v1/who-knows etc. and the
// gRPC WhoKnows etc.). Each handler runs against the library Memory facade the
// MCP server builds lazily; these are the agent-facing cognitive reads.

type mcpWhoKnowsInput struct {
	Query string `json:"query" jsonschema:"required,description=Topic/question to find expert workers for"`
	Limit int    `json:"limit,omitempty" jsonschema:"description=Max experts to return (default 10)"`
}
type mcpExpert struct {
	Worker      string  `json:"worker"`
	Affinity    float64 `json:"affinity"`
	Reliability float64 `json:"reliability"`
	ClaimCount  int     `json:"belief_count"`
}
type mcpWhoKnowsOutput struct {
	Query   string      `json:"query"`
	Experts []mcpExpert `json:"experts"`
}

func mcpWhoKnows(ctx context.Context, mem mnemos.Memory, in mcpWhoKnowsInput) (mcpWhoKnowsOutput, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = 10
	}
	experts, err := mem.WhoKnows(ctx, in.Query, limit)
	if err != nil {
		return mcpWhoKnowsOutput{}, err
	}
	out := mcpWhoKnowsOutput{Query: in.Query, Experts: []mcpExpert{}}
	for _, e := range experts {
		out.Experts = append(out.Experts, mcpExpert{e.Worker, e.Affinity, e.Reliability, e.ClaimCount})
	}
	return out, nil
}

type mcpKnowledgeGapsInput struct {
	Limit int `json:"limit,omitempty" jsonschema:"description=Max gaps to return (default 20)"`
}
type mcpGap struct {
	ClaimID string  `json:"belief_id"`
	Text    string  `json:"text"`
	Kind    string  `json:"kind"`
	Score   float64 `json:"score"`
}
type mcpKnowledgeGapsOutput struct {
	Gaps []mcpGap `json:"gaps"`
}

func mcpKnowledgeGaps(ctx context.Context, mem mnemos.Memory, in mcpKnowledgeGapsInput) (mcpKnowledgeGapsOutput, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	gaps, err := mem.KnowledgeGaps(ctx, limit)
	if err != nil {
		return mcpKnowledgeGapsOutput{}, err
	}
	out := mcpKnowledgeGapsOutput{Gaps: []mcpGap{}}
	for _, g := range gaps {
		out.Gaps = append(out.Gaps, mcpGap{g.ClaimID, g.Text, g.Kind, g.Score})
	}
	return out, nil
}

type mcpCalibrationBucket struct {
	Lower          float64 `json:"lower"`
	Upper          float64 `json:"upper"`
	Count          int     `json:"count"`
	MeanConfidence float64 `json:"mean_confidence"`
	Accuracy       float64 `json:"accuracy"`
}
type mcpCalibrationSource struct {
	Source         string  `json:"source"`
	Samples        int     `json:"samples"`
	MeanConfidence float64 `json:"mean_confidence"`
	Accuracy       float64 `json:"accuracy"`
	Gap            float64 `json:"gap"`
	Brier          float64 `json:"brier"`
}
type mcpCalibrationOutput struct {
	Samples int                    `json:"samples"`
	ECE     float64                `json:"ece"`
	Brier   float64                `json:"brier"`
	Buckets []mcpCalibrationBucket `json:"buckets"`
	Sources []mcpCalibrationSource `json:"sources"`
}

func mcpCalibration(ctx context.Context, mem mnemos.Memory, _ struct{}) (mcpCalibrationOutput, error) {
	cal, err := mem.Calibration(ctx)
	if err != nil {
		return mcpCalibrationOutput{}, err
	}
	out := mcpCalibrationOutput{Samples: cal.Samples, ECE: cal.ECE, Brier: cal.Brier}
	for _, b := range cal.Buckets {
		out.Buckets = append(out.Buckets, mcpCalibrationBucket{b.Lower, b.Upper, b.Count, b.MeanConfidence, b.Accuracy})
	}
	for _, s := range cal.Sources {
		out.Sources = append(out.Sources, mcpCalibrationSource{s.Source, s.Samples, s.MeanConfidence, s.Accuracy, s.Gap, s.Brier})
	}
	return out, nil
}

type mcpHypercorrection struct {
	ContradictedClaimID  string  `json:"contradicted_belief_id"`
	ContradictedText     string  `json:"contradicted_text"`
	ContradictedTrust    float64 `json:"contradicted_trust"`
	ContradictedPromoted bool    `json:"contradicted_promoted"`
	ChallengingClaimID   string  `json:"challenging_belief_id"`
	ChallengingText      string  `json:"challenging_text"`
}
type mcpHypercorrectionsOutput struct {
	Hypercorrections []mcpHypercorrection `json:"hypercorrections"`
}

func mcpHypercorrections(ctx context.Context, mem mnemos.Memory, _ struct{}) (mcpHypercorrectionsOutput, error) {
	hcs, err := mem.Hypercorrections(ctx)
	if err != nil {
		return mcpHypercorrectionsOutput{}, err
	}
	out := mcpHypercorrectionsOutput{Hypercorrections: []mcpHypercorrection{}}
	for _, h := range hcs {
		out.Hypercorrections = append(out.Hypercorrections, mcpHypercorrection{
			h.ContradictedClaimID, h.ContradictedText, h.ContradictedTrust, h.ContradictedPromoted,
			h.ChallengingClaimID, h.ChallengingText,
		})
	}
	return out, nil
}

type mcpRecombinationsInput struct {
	Limit int `json:"limit,omitempty" jsonschema:"description=Max pairs to return (default 20)"`
}
type mcpRecombination struct {
	ClaimA     string  `json:"belief_a"`
	TextA      string  `json:"text_a"`
	ClaimB     string  `json:"belief_b"`
	TextB      string  `json:"text_b"`
	Similarity float64 `json:"similarity"`
}
type mcpRecombinationsOutput struct {
	Recombinations []mcpRecombination `json:"recombinations"`
}

func mcpRecombinations(ctx context.Context, mem mnemos.Memory, in mcpRecombinationsInput) (mcpRecombinationsOutput, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	recs, err := mem.Recombinations(ctx, limit)
	if err != nil {
		return mcpRecombinationsOutput{}, err
	}
	out := mcpRecombinationsOutput{Recombinations: []mcpRecombination{}}
	for _, r := range recs {
		out.Recombinations = append(out.Recombinations, mcpRecombination{r.ClaimA, r.TextA, r.ClaimB, r.TextB, r.Similarity})
	}
	return out, nil
}

type mcpAnalogousInput struct {
	ClaimID string `json:"belief_id" jsonschema:"required,description=The claim to find analogues for"`
	Limit   int    `json:"limit,omitempty" jsonschema:"description=Max analogues (default 10)"`
}
type mcpAnalogy struct {
	ClaimID    string  `json:"belief_id"`
	Text       string  `json:"text"`
	Similarity float64 `json:"similarity"`
}
type mcpAnalogousOutput struct {
	ClaimID   string       `json:"belief_id"`
	Analogous []mcpAnalogy `json:"analogous"`
}

func mcpAnalogousClaims(ctx context.Context, mem mnemos.Memory, in mcpAnalogousInput) (mcpAnalogousOutput, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = 10
	}
	analogies, err := mem.AnalogousClaims(ctx, in.ClaimID, limit)
	if err != nil {
		return mcpAnalogousOutput{}, err
	}
	out := mcpAnalogousOutput{ClaimID: in.ClaimID, Analogous: []mcpAnalogy{}}
	for _, a := range analogies {
		out.Analogous = append(out.Analogous, mcpAnalogy{a.ClaimID, a.Text, a.Similarity})
	}
	return out, nil
}
