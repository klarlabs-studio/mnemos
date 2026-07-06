package main

import (
	"context"

	mnemos "go.klarlabs.de/mnemos"
)

// Working-memory + temporal MCP tools (parity with gRPC GetBlocks/SetBlock/
// Signals and HTTP GET /v1/blocks, /v1/signals). These give an out-of-process
// agent its "core memory" (bounded, always-injected blocks) and the temporal
// "what is changing?" reading that complements recall.

type mcpGetBlocksInput struct {
	Owner string `json:"owner" jsonschema:"required,description=The agent whose working-memory blocks to read"`
}
type mcpBlock struct {
	Owner     string `json:"owner"`
	Label     string `json:"label"`
	Value     string `json:"value"`
	UpdatedAt string `json:"updated_at,omitempty"`
}
type mcpGetBlocksOutput struct {
	Owner  string     `json:"owner"`
	Blocks []mcpBlock `json:"blocks"`
}

func mcpGetBlocks(ctx context.Context, mem mnemos.Memory, in mcpGetBlocksInput) (mcpGetBlocksOutput, error) {
	blocks, err := mem.Blocks(ctx, in.Owner)
	if err != nil {
		return mcpGetBlocksOutput{}, err
	}
	out := mcpGetBlocksOutput{Owner: in.Owner, Blocks: []mcpBlock{}}
	for _, b := range blocks {
		out.Blocks = append(out.Blocks, mcpBlock{b.Owner, b.Label, b.Value, rfc3339OrEmpty(b.UpdatedAt)})
	}
	return out, nil
}

type mcpSetBlockInput struct {
	Owner  string `json:"owner" jsonschema:"required,description=The agent that owns the block"`
	Label  string `json:"label" jsonschema:"required,description=The block name (e.g. persona, open_threads)"`
	Value  string `json:"value" jsonschema:"description=The block text; empty value with append=false clears the block"`
	Append bool   `json:"append,omitempty" jsonschema:"description=Append to the block (evicting oldest lines to stay within budget) instead of replacing it"`
}
type mcpSetBlockOutput struct {
	Owner string `json:"owner"`
	Label string `json:"label"`
}

func mcpSetBlock(ctx context.Context, mem mnemos.Memory, in mcpSetBlockInput) (mcpSetBlockOutput, error) {
	var err error
	if in.Append {
		err = mem.AppendBlock(ctx, in.Owner, in.Label, in.Value)
	} else {
		err = mem.SetBlock(ctx, in.Owner, in.Label, in.Value)
	}
	if err != nil {
		return mcpSetBlockOutput{}, err
	}
	return mcpSetBlockOutput{Owner: in.Owner, Label: in.Label}, nil
}

type mcpSignalsInput struct {
	RunID         string  `json:"run_id,omitempty" jsonschema:"description=The run (Chronos scope) to read signals for; empty = the default run"`
	MinConfidence float64 `json:"min_confidence,omitempty" jsonschema:"description=Drop signals below this confidence; 0 keeps all"`
	Limit         int     `json:"limit,omitempty" jsonschema:"description=Max signals (default 50)"`
}
type mcpSignal struct {
	Pattern     string  `json:"pattern"`
	RunID       string  `json:"run_id,omitempty"`
	Strength    float64 `json:"strength"`
	Confidence  float64 `json:"confidence"`
	Class       string  `json:"class,omitempty"`
	DetectedAt  string  `json:"detected_at,omitempty"`
	WindowStart string  `json:"window_start,omitempty"`
	WindowEnd   string  `json:"window_end,omitempty"`
}
type mcpSignalsOutput struct {
	Signals []mcpSignal `json:"signals"`
}

func mcpSignals(ctx context.Context, mem mnemos.Memory, in mcpSignalsInput) (mcpSignalsOutput, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}
	signals, err := mem.Signals(ctx, mnemos.SignalQuery{RunID: in.RunID, MinConfidence: in.MinConfidence, Limit: limit})
	if err != nil {
		return mcpSignalsOutput{}, err
	}
	out := mcpSignalsOutput{Signals: []mcpSignal{}}
	for _, s := range signals {
		out.Signals = append(out.Signals, mcpSignal{
			Pattern: s.Pattern, RunID: s.RunID, Strength: s.Strength, Confidence: s.Confidence, Class: s.Class,
			DetectedAt:  rfc3339OrEmpty(s.DetectedAt),
			WindowStart: rfc3339OrEmpty(s.WindowStart),
			WindowEnd:   rfc3339OrEmpty(s.WindowEnd),
		})
	}
	return out, nil
}
