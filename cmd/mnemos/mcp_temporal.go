package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/felixgeelhaar/mnemos/internal/domain"
	"github.com/felixgeelhaar/mnemos/internal/embedding"
	"github.com/felixgeelhaar/mnemos/internal/llm"
	"github.com/felixgeelhaar/mnemos/internal/query"
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
	Events []mcpTimelineEvent `json:"events"`
}

// mcpRecallAtTimeInput pins the answer to a historical instant. Same
// shape as mcpQueryInput but with an AsOf field that scopes the
// retrieval set.
type mcpRecallAtTimeInput struct {
	Question string `json:"question" jsonschema:"required,description=Natural language question"`
	AsOf     string `json:"asOf" jsonschema:"required,description=RFC3339 instant; claims valid at this time are returned"`
	RunID    string `json:"runId,omitempty" jsonschema:"description=Optional run ID to scope the query"`
	Hops     int    `json:"hops,omitempty" jsonschema:"description=BFS hop expansion depth through supports/contradicts edges (0-5)"`
}

// mcpRunRememberEvent persists a temporal event into the Mnemos event
// store. The bundled Chronos engine is updated in a follow-up commit
// once a numeric feature mapping is defined; for now the source event
// is recorded faithfully and recoverable via timeline_query.
func mcpRunRememberEvent(ctx context.Context, actor string, input mcpRememberEventInput) (mcpRememberEventOutput, error) {
	if strings.TrimSpace(input.Content) == "" {
		return mcpRememberEventOutput{}, errors.New("content is required")
	}
	at, err := time.Parse(time.RFC3339, strings.TrimSpace(input.At))
	if err != nil {
		return mcpRememberEventOutput{}, fmt.Errorf("invalid at timestamp: %w", err)
	}

	conn, err := openConn(ctx)
	if err != nil {
		return mcpRememberEventOutput{}, err
	}
	defer closeConn(conn)

	id := strings.TrimSpace(input.ID)
	if id == "" {
		id = fmt.Sprintf("ev_%d", time.Now().UnixNano())
	}
	meta := map[string]string{}
	maps.Copy(meta, input.Metadata)
	if input.Type != "" {
		meta["event_type"] = input.Type
	}

	ev := domain.Event{
		ID:            id,
		RunID:         input.RunID,
		SchemaVersion: "1.0",
		Content:       input.Content,
		SourceInputID: "inline:" + id,
		Timestamp:     at.UTC(),
		Metadata:      meta,
		IngestedAt:    time.Now().UTC(),
		CreatedBy:     actor,
	}
	if err := conn.Events.Append(ctx, ev); err != nil {
		return mcpRememberEventOutput{}, fmt.Errorf("append event: %w", err)
	}
	return mcpRememberEventOutput{ID: id}, nil
}

// mcpRunTimelineQuery returns events filtered by time range, type, and
// run, sorted by timestamp ascending.
func mcpRunTimelineQuery(ctx context.Context, input mcpTimelineQueryInput) (mcpTimelineQueryOutput, error) {
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

	conn, err := openConn(ctx)
	if err != nil {
		return mcpTimelineQueryOutput{}, err
	}
	defer closeConn(conn)

	var events []domain.Event
	if runID := strings.TrimSpace(input.RunID); runID != "" {
		events, err = conn.Events.ListByRunID(ctx, runID)
	} else {
		events, err = conn.Events.ListAll(ctx)
	}
	if err != nil {
		return mcpTimelineQueryOutput{}, fmt.Errorf("list events: %w", err)
	}

	out := make([]mcpTimelineEvent, 0, len(events))
	for _, ev := range events {
		if !from.IsZero() && ev.Timestamp.Before(from) {
			continue
		}
		if !to.IsZero() && ev.Timestamp.After(to) {
			continue
		}
		typ := ev.Metadata["event_type"]
		if len(input.Types) > 0 && !slices.Contains(input.Types, typ) {
			continue
		}
		out = append(out, mcpTimelineEvent{
			ID:       ev.ID,
			At:       ev.Timestamp.UTC().Format(time.RFC3339),
			Type:     typ,
			Content:  ev.Content,
			Metadata: scrubMetaKey(ev.Metadata, "event_type"),
			RunID:    ev.RunID,
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].At < out[j].At })

	if input.Limit > 0 && len(out) > input.Limit {
		out = out[:input.Limit]
	}
	return mcpTimelineQueryOutput{Events: out}, nil
}

// mcpRunRecallAtTime answers a query against the state of the
// knowledge base at a historical instant (claims whose ValidFrom <=
// AsOf and ValidTo > AsOf are considered).
func mcpRunRecallAtTime(ctx context.Context, input mcpRecallAtTimeInput) (mcpQueryOutput, error) {
	q := strings.TrimSpace(input.Question)
	if q == "" {
		return mcpQueryOutput{}, errors.New("question is required")
	}
	asOf, err := time.Parse(time.RFC3339, strings.TrimSpace(input.AsOf))
	if err != nil {
		return mcpQueryOutput{}, fmt.Errorf("invalid asOf timestamp: %w", err)
	}

	conn, err := openConn(ctx)
	if err != nil {
		return mcpQueryOutput{}, err
	}
	defer closeConn(conn)

	engine := query.NewEngine(conn.Events, conn.Claims, conn.Relationships)
	if embCfg, embErr := embedding.ConfigFromEnv(); embErr == nil {
		if embClient, ecErr := embedding.NewClient(embCfg); ecErr == nil {
			engine = engine.WithEmbeddings(conn.Embeddings, embClient)
		}
	}
	if llmCfg, llmErr := llm.ConfigFromEnv(); llmErr == nil {
		if llmClient, lcErr := llm.NewClient(llmCfg); lcErr == nil {
			engine = engine.WithLLM(llmClient)
		}
	}

	hops := max(input.Hops, 0)
	hops = min(hops, 5)
	opts := query.AnswerOptions{
		Hops: hops,
		AsOf: asOf.UTC(),
	}

	var answer domain.Answer
	if runID := strings.TrimSpace(input.RunID); runID != "" {
		answer, err = engine.AnswerForRunWithOptions(q, runID, opts)
	} else {
		answer, err = engine.AnswerWithOptions(q, opts)
	}
	if err != nil {
		return mcpQueryOutput{}, err
	}

	return mcpQueryOutput{
		Answer:           answer.AnswerText,
		Claims:           answer.Claims,
		Contradictions:   answer.Contradictions,
		Timeline:         answer.TimelineEventIDs,
		ClaimProvenance:  answer.ClaimProvenance,
		ClaimHopDistance: answer.ClaimHopDistance,
	}, nil
}

// scrubMetaKey returns a copy of in without the named key. Used to
// hide internal "event_type" from the metadata payload (it's already
// surfaced as a top-level field).
func scrubMetaKey(in map[string]string, skip string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if k == skip {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
