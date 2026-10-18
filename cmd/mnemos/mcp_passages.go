package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.klarlabs.de/mnemos"
)

// mcpSearchPassagesInput is the input for the search_passages tool.
type mcpSearchPassagesInput struct {
	Query string `json:"query" jsonschema:"description=What to look for, in natural language"`
	Limit int    `json:"limit,omitempty" jsonschema:"description=Cap on returned passages; 0 means 10"`
	RunID string `json:"runId,omitempty" jsonschema:"description=Restrict to a single run's episodes"`
}

type mcpPassage struct {
	EpisodeID string            `json:"episodeId"`
	Text      string            `json:"text"`
	At        string            `json:"at,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	Score     float64           `json:"score"`
}

type mcpSearchPassagesOutput struct {
	Passages []mcpPassage `json:"passages"`
}

// mcpRunSearchPassages returns the stored episode lines that best match a query
// ([mnemos.Memory.Passages]). They are raw episode text, so a run-restricted
// token must name one of its runs.
func mcpRunSearchPassages(ctx context.Context, input mcpSearchPassagesInput) (mcpSearchPassagesOutput, error) {
	text := strings.TrimSpace(input.Query)
	if text == "" {
		return mcpSearchPassagesOutput{}, userInputError(errors.New("query is required"))
	}
	if err := enforceRunScope(ctx, input.RunID); err != nil {
		return mcpSearchPassagesOutput{}, err
	}
	mem, err := newLibraryMemory(ctx, "")
	if err != nil {
		return mcpSearchPassagesOutput{}, err
	}
	defer func() { _ = mem.Close() }()

	passages, err := mem.Passages(ctx, mnemos.PassageQuery{
		Text: text, Limit: input.Limit, RunID: strings.TrimSpace(input.RunID),
	})
	if err != nil {
		return mcpSearchPassagesOutput{}, fmt.Errorf("passages: %w", err)
	}
	out := make([]mcpPassage, len(passages))
	for i, p := range passages {
		out[i] = mcpPassage{EpisodeID: p.EpisodeID, Text: p.Text, Metadata: p.Metadata, Score: p.Score}
		if !p.At.IsZero() {
			out[i].At = p.At.UTC().Format(time.RFC3339)
		}
	}
	return mcpSearchPassagesOutput{Passages: out}, nil
}
