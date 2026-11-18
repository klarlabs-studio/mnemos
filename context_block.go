package mnemos

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.klarlabs.de/mnemos/internal/query"
)

// ContextBlockQuery asks [Memory.ContextBlock] for a run's context block.
type ContextBlockQuery struct {
	// RunID is the run to summarise. Required.
	RunID string
	// Query, when set, ranks the run's beliefs against it before rendering.
	Query string
	// MaxTokens caps the rendered block's approximate size; zero means no cap.
	MaxTokens int
}

// ContextBlock implements [Memory.ContextBlock]. It renders through
// query.Engine.BuildContextBlock, the same renderer REST /v1/context, gRPC
// ContextBlock and the memory_context MCP tool use.
func (m *memory) ContextBlock(ctx context.Context, q ContextBlockQuery) (string, error) {
	runID := strings.TrimSpace(q.RunID)
	if runID == "" {
		return "", errors.New("mnemos: ContextBlock: RunID is required")
	}
	if q.MaxTokens < 0 {
		return "", errors.New("mnemos: ContextBlock: MaxTokens must be non-negative")
	}
	engine := query.NewEngine(m.conn.Events, m.conn.Claims, m.conn.Relationships)
	block, err := engine.BuildContextBlock(ctx, query.ContextBlockOptions{
		RunID: runID, Query: q.Query, MaxTokens: q.MaxTokens,
	})
	if err != nil {
		return "", fmt.Errorf("mnemos: ContextBlock: %w", err)
	}
	return block, nil
}
