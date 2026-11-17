package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.klarlabs.de/mnemos"
)

// mcpRememberEventInput is the MCP-side input shape for the
// remember_event tool. Mirrors mnemos.Event with JSON tags + RFC3339
// timestamps for over-the-wire safety.
type mcpRememberEventInput struct {
	ID       string            `json:"id,omitempty" jsonschema:"description=Stable event identifier; generated if empty"`
	At       string            `json:"at" jsonschema:"required,description=RFC3339 wall-clock timestamp the event occurred"`
	Type     string            `json:"type,omitempty" jsonschema:"description=Event classification (deployment, incident, decision, ...)"`
	Content  string            `json:"content" jsonschema:"required,description=Human-readable description of the event"`
	Metadata map[string]string `json:"metadata,omitempty" jsonschema:"description=Optional key/value context"`
	RunID    string            `json:"runId,omitempty" jsonschema:"description=Optional run identifier for scoped recall"`
}

type mcpRememberEventOutput struct {
	ID string `json:"id"`
}

// mcpTimelineQueryInput is the MCP-side input shape for the
// timeline_query tool.
type mcpTimelineQueryInput struct {
	From  string   `json:"from,omitempty" jsonschema:"description=RFC3339 lower bound; events at this time are included"`
	To    string   `json:"to,omitempty" jsonschema:"description=RFC3339 upper bound; events at this time are included"`
	Types []string `json:"types,omitempty" jsonschema:"description=Restrict result to events whose type is in this list"`
	RunID string   `json:"runId,omitempty" jsonschema:"description=Restrict result to a single run"`
	Limit int      `json:"limit,omitempty" jsonschema:"description=Cap on returned events; 0 means no limit"`
}

type mcpTimelineEvent struct {
	ID       string            `json:"id"`
	At       string            `json:"at"`
	Type     string            `json:"type"`
	Content  string            `json:"content"`
	Metadata map[string]string `json:"metadata,omitempty"`
	RunID    string            `json:"runId,omitempty"`
}

type mcpTimelineQueryOutput struct {
	Events []mcpTimelineEvent `json:"episodes"`
}

// mcpRecallAtTimeInput pins the answer to a historical instant. Same
// shape as mcpQueryInput but with an AsOf field that scopes the
// retrieval set.
type mcpRecallAtTimeInput struct {
	Question string `json:"question" jsonschema:"required,description=Natural language question"`
	AsOf     string `json:"asOf" jsonschema:"required,description=YYYY-MM-DD or RFC3339 instant; claims valid at this time are returned"`
	// RecordedAsOf is the transaction-time axis (ADR 0035): the answer the
	// brain would have given at that instant, with each belief's status,
	// validity and contradictions as they were recorded then.
	RecordedAsOf string `json:"recordedAsOf,omitempty" jsonschema:"description=Optional YYYY-MM-DD or RFC3339 instant; answer as the brain believed it then (status, validity and contradictions as recorded at that time). Pair with asOf: what did we believe at recordedAsOf about asOf"`
	RunID        string `json:"runId,omitempty" jsonschema:"description=Optional run ID to scope the query"`
	Hops         int    `json:"hops,omitempty" jsonschema:"description=BFS hop expansion depth through supports/contradicts edges (0-5)"`
}

// mcpRunRememberEvent persists a temporal event via the public library
// API. The handler routes through [mnemos.Memory.RememberEvent] rather
// than reaching into internal/store directly — this is the proof-of-
// concept that the public surface is sufficient for the temporal MCP
// tools. Other MCP handlers still use internal/ because they need
// flags / outputs the library doesn't expose.
func mcpRunRememberEvent(ctx context.Context, actor string, input mcpRememberEventInput) (mcpRememberEventOutput, error) {
	if strings.TrimSpace(input.Content) == "" {
		return mcpRememberEventOutput{}, errors.New("content is required")
	}
	if err := enforceRunScope(ctx, input.RunID); err != nil {
		return mcpRememberEventOutput{}, err
	}
	at, err := time.Parse(time.RFC3339, strings.TrimSpace(input.At))
	if err != nil {
		return mcpRememberEventOutput{}, userInputError(fmt.Errorf("invalid at timestamp: %w", err))
	}

	mem, err := newLibraryMemory(ctx, actor)
	if err != nil {
		return mcpRememberEventOutput{}, err
	}
	defer func() { _ = mem.Close() }()

	id := strings.TrimSpace(input.ID)
	if id == "" {
		id = fmt.Sprintf("ev_%d", time.Now().UnixNano())
	}
	if err := mem.RememberEvent(ctx, mnemos.Event{
		ID:       id,
		At:       at,
		Type:     input.Type,
		Content:  input.Content,
		Metadata: input.Metadata,
		RunID:    input.RunID,
	}); err != nil {
		return mcpRememberEventOutput{}, fmt.Errorf("remember event: %w", err)
	}
	return mcpRememberEventOutput{ID: id}, nil
}

// mcpRunTimelineQuery returns events filtered by time range, type, and
// run, sorted by timestamp ascending. Routes through the public
// [mnemos.Memory.Timeline] surface (the library does the filtering +
// sorting; the handler only marshals the result into the MCP wire
// shape).
func mcpRunTimelineQuery(ctx context.Context, input mcpTimelineQueryInput) (mcpTimelineQueryOutput, error) {
	if err := enforceRunScope(ctx, input.RunID); err != nil {
		return mcpTimelineQueryOutput{}, err
	}
	var from, to time.Time
	var err error
	if s := strings.TrimSpace(input.From); s != "" {
		if from, err = time.Parse(time.RFC3339, s); err != nil {
			return mcpTimelineQueryOutput{}, fmt.Errorf("invalid from timestamp: %w", err)
		}
	}
	if s := strings.TrimSpace(input.To); s != "" {
		if to, err = time.Parse(time.RFC3339, s); err != nil {
			return mcpTimelineQueryOutput{}, fmt.Errorf("invalid to timestamp: %w", err)
		}
	}

	mem, err := newLibraryMemory(ctx, "")
	if err != nil {
		return mcpTimelineQueryOutput{}, err
	}
	defer func() { _ = mem.Close() }()

	events, err := mem.Timeline(ctx, mnemos.TimelineQuery{
		From:  from,
		To:    to,
		Types: input.Types,
		RunID: strings.TrimSpace(input.RunID),
		Limit: input.Limit,
	})
	if err != nil {
		return mcpTimelineQueryOutput{}, fmt.Errorf("timeline: %w", err)
	}

	out := make([]mcpTimelineEvent, len(events))
	for i, ev := range events {
		out[i] = mcpTimelineEvent{
			ID:       ev.ID,
			At:       ev.At.UTC().Format(time.RFC3339),
			Type:     ev.Type,
			Content:  ev.Content,
			Metadata: ev.Metadata,
			RunID:    ev.RunID,
		}
	}
	return mcpTimelineQueryOutput{Events: out}, nil
}

// mcpRunRecallAtTime is query_knowledge with a required valid time. It used to
// be a second implementation with its own engine wiring, parser and hop rule,
// and it searched only the global brain where query_knowledge federates the
// repo brain too. It is now an adapter: the same handler, the same brains, the
// same answer for the same question.
func mcpRunRecallAtTime(ctx context.Context, input mcpRecallAtTimeInput) (recallOutput, error) {
	if strings.TrimSpace(input.AsOf) == "" {
		return recallOutput{}, userInputError(errors.New("asOf is required"))
	}
	return mcpRunQueryScoped(ctx, mcpQueryInput{
		Question:     input.Question,
		RunID:        input.RunID,
		Hops:         input.Hops,
		AsOf:         input.AsOf,
		RecordedAsOf: input.RecordedAsOf,
	})
}
