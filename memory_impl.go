package mnemos

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/felixgeelhaar/chronos"
	"github.com/felixgeelhaar/chronos/embed"
	"github.com/felixgeelhaar/mnemos/internal/domain"
	"github.com/felixgeelhaar/mnemos/internal/embedding"
	"github.com/felixgeelhaar/mnemos/internal/ingest"
	"github.com/felixgeelhaar/mnemos/internal/llm"
	"github.com/felixgeelhaar/mnemos/internal/parser"
	"github.com/felixgeelhaar/mnemos/internal/pipeline"
	"github.com/felixgeelhaar/mnemos/internal/query"
	"github.com/felixgeelhaar/mnemos/internal/relate"
	"github.com/felixgeelhaar/mnemos/internal/store"
	"github.com/felixgeelhaar/mnemos/providers"
	"github.com/google/uuid"
)

// chronosEventNamespace is the deterministic UUID namespace used when
// hashing string ids (run id, event type) into uuid.UUID for Chronos
// EntityState identifiers. Stable across processes so the same
// (RunID, Type) tuple always maps to the same Chronos series.
var chronosEventNamespace = uuid.NewSHA1(uuid.NameSpaceURL, []byte("mnemos://events"))

// memory is the concrete implementation of [Memory] returned by [New].
type memory struct {
	conn      *store.Conn
	actorID   string
	extractor *pipeline.Extractor
	relator   relate.Engine
	query     query.Engine
	embedder  embedding.Client

	// chronos is the bundled or user-supplied temporal engine. Always
	// non-nil after a successful [New].
	chronos *embed.Engine
	// chronosOwned is true when [New] constructed the engine itself
	// and is responsible for closing it. False when the caller
	// supplied one via [WithChronos].
	chronosOwned bool
}

var _ Memory = (*memory)(nil)

// Remember implements [Memory.Remember].
func (m *memory) Remember(ctx context.Context, item Item) error {
	if strings.TrimSpace(item.Content) == "" {
		return errors.New("mnemos: Remember: Content is required")
	}

	svc := ingest.NewService()
	in, content, err := svc.IngestText(item.Content, mergeMetadata(item))
	if err != nil {
		return fmt.Errorf("mnemos: ingest: %w", err)
	}

	events, err := parser.NewNormalizer().Normalize(in, content)
	if err != nil {
		return fmt.Errorf("mnemos: normalize: %w", err)
	}
	for i := range events {
		if item.RunID != "" {
			events[i].RunID = item.RunID
		}
		events[i].CreatedBy = m.actorID
	}

	claims, links, _, err := m.extractor.ExtractFn(events)
	if err != nil {
		return fmt.Errorf("mnemos: extract: %w", err)
	}
	for i := range claims {
		claims[i].CreatedBy = m.actorID
		if item.Type != "" {
			claims[i].Type = domain.ClaimType(item.Type)
		}
	}

	rels, err := m.relator.Detect(claims)
	if err != nil {
		return fmt.Errorf("mnemos: relate: %w", err)
	}
	existing, err := m.conn.Claims.ListAll(ctx)
	if err == nil && len(existing) > 0 {
		incremental, irelErr := m.relator.DetectIncremental(claims, existing)
		if irelErr == nil {
			rels = append(rels, incremental...)
		}
	}
	for i := range rels {
		rels[i].CreatedBy = m.actorID
	}

	if err := pipeline.PersistArtifacts(ctx, m.conn, events, claims, links, rels); err != nil {
		return fmt.Errorf("mnemos: persist: %w", err)
	}
	return nil
}

// RememberClaim implements [Memory.RememberClaim].
func (m *memory) RememberClaim(ctx context.Context, item ClaimItem) (string, error) {
	if strings.TrimSpace(item.Text) == "" {
		return "", errors.New("mnemos: RememberClaim: Text is required")
	}

	claimType := domain.ClaimType(item.Type)
	if claimType == "" {
		claimType = domain.ClaimTypeFact
	}
	confidence := item.Confidence
	if confidence == 0 {
		confidence = 1.0
	}

	id := fmt.Sprintf("cl_%d", time.Now().UnixNano())
	claim := domain.Claim{
		ID:         id,
		Text:       item.Text,
		Type:       claimType,
		Confidence: confidence,
		Status:     domain.ClaimStatusActive,
		CreatedAt:  time.Now().UTC(),
		CreatedBy:  m.actorID,
		ValidFrom:  item.ValidFrom,
		ValidTo:    item.ValidUntil,
	}

	if err := m.conn.Claims.Upsert(ctx, []domain.Claim{claim}); err != nil {
		return "", fmt.Errorf("mnemos: upsert claim: %w", err)
	}

	if len(item.EventIDs) > 0 {
		links := make([]domain.ClaimEvidence, len(item.EventIDs))
		for i, eid := range item.EventIDs {
			links[i] = domain.ClaimEvidence{ClaimID: id, EventID: eid}
		}
		if err := m.conn.Claims.UpsertEvidence(ctx, links); err != nil {
			return "", fmt.Errorf("mnemos: link evidence: %w", err)
		}
	}
	return id, nil
}

// Recall implements [Memory.Recall].
func (m *memory) Recall(ctx context.Context, q Query) ([]Result, error) {
	if strings.TrimSpace(q.Text) == "" {
		return nil, errors.New("mnemos: Recall: Text is required")
	}

	opts := query.AnswerOptions{
		Hops:           q.Hops,
		AsOf:           q.AsOf,
		IncludeHistory: q.IncludeHistory,
	}

	var ans domain.Answer
	var err error
	if q.RunID != "" {
		ans, err = m.query.AnswerForRunWithOptions(q.Text, q.RunID, opts)
	} else {
		ans, err = m.query.AnswerWithOptions(q.Text, opts)
	}
	if err != nil {
		return nil, fmt.Errorf("mnemos: answer: %w", err)
	}

	results := make([]Result, 0, len(ans.Claims))
	for _, c := range ans.Claims {
		results = append(results, Result{
			ClaimID:     c.ID,
			Text:        c.Text,
			Type:        string(c.Type),
			Confidence:  c.Confidence,
			TrustScore:  c.TrustScore,
			HopDistance: ans.ClaimHopDistance[c.ID],
			Provenance:  ans.ClaimProvenance[c.ID],
		})
	}
	if q.Limit > 0 && len(results) > q.Limit {
		results = results[:q.Limit]
	}
	// Suppress unused-context: the underlying engine is sync today; ctx
	// is reserved for future cancellation propagation through the
	// query.Engine surface.
	_ = ctx
	return results, nil
}

// RememberEvent implements [Memory.RememberEvent].
func (m *memory) RememberEvent(ctx context.Context, e Event) error {
	if e.At.IsZero() {
		return errors.New("mnemos: RememberEvent: At is required")
	}
	if strings.TrimSpace(e.Content) == "" {
		return errors.New("mnemos: RememberEvent: Content is required")
	}

	id := strings.TrimSpace(e.ID)
	if id == "" {
		id = newEventID()
	}
	meta := map[string]string{}
	maps.Copy(meta, e.Metadata)
	if e.Type != "" {
		meta["event_type"] = e.Type
	}

	ev := domain.Event{
		ID:            id,
		RunID:         e.RunID,
		SchemaVersion: "1.0",
		Content:       e.Content,
		SourceInputID: "inline:" + id, // synthetic origin for direct API calls
		Timestamp:     e.At.UTC(),
		Metadata:      meta,
		IngestedAt:    time.Now().UTC(),
		CreatedBy:     m.actorID,
	}
	if err := m.conn.Events.Append(ctx, ev); err != nil {
		return fmt.Errorf("mnemos: append event: %w", err)
	}

	// Forward the event to the bundled Chronos engine as a presence
	// signal: each event maps to a single-feature EntityState at the
	// event timestamp. This lets the detector see deployment / incident
	// / decision bursts as recurrence + spike patterns over time.
	//
	// The mapping is deliberately minimal:
	//   - EntityID  = NS-hash(event_type) so each type is its own series
	//   - ScopeID   = NS-hash(run_id) so scopes match RememberEvent runs
	//   - Features  = [1.0] presence indicator
	//   - Labels    = ["event"]
	//   - Meta      = event type + truncated content for explainability
	//
	// Chronos failures are non-fatal: the Mnemos event store is the
	// source of truth and was already persisted above. Returning an
	// error here would make a Chronos hiccup look like a Remember
	// failure to the caller, which is wrong.
	if m.chronos != nil {
		_ = m.chronos.Process(ctx, m.eventToEntityState(e, id))
	}
	return nil
}

// eventToEntityState maps a public mnemos.Event into a chronos.EntityState
// suitable for the bundled in-process engine. See [memory.RememberEvent]
// for the mapping rationale.
func (m *memory) eventToEntityState(e Event, eventID string) chronos.EntityState {
	typ := strings.TrimSpace(e.Type)
	if typ == "" {
		typ = "untyped"
	}
	runID := e.RunID
	if runID == "" {
		runID = "default"
	}
	meta := map[string]string{
		"mnemos_event_id": eventID,
		"event_type":      typ,
	}
	if c := strings.TrimSpace(e.Content); c != "" {
		if len(c) > 200 {
			c = c[:200]
		}
		meta["content_preview"] = c
	}
	return chronos.EntityState{
		ID:        uuid.New(),
		EntityID:  uuid.NewSHA1(chronosEventNamespace, []byte(typ)),
		ScopeID:   uuid.NewSHA1(chronosEventNamespace, []byte(runID)),
		Timestamp: e.At.UTC(),
		Features:  []float64{1.0},
		Labels:    []string{"event"},
		Meta:      meta,
	}
}

// Timeline implements [Memory.Timeline].
func (m *memory) Timeline(ctx context.Context, q TimelineQuery) ([]Event, error) {
	var events []domain.Event
	var err error
	if q.RunID != "" {
		events, err = m.conn.Events.ListByRunID(ctx, q.RunID)
	} else {
		events, err = m.conn.Events.ListAll(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("mnemos: list events: %w", err)
	}

	out := make([]Event, 0, len(events))
	for _, ev := range events {
		if !q.From.IsZero() && ev.Timestamp.Before(q.From) {
			continue
		}
		if !q.To.IsZero() && ev.Timestamp.After(q.To) {
			continue
		}
		typ := ev.Metadata["event_type"]
		if len(q.Types) > 0 && !slices.Contains(q.Types, typ) {
			continue
		}
		out = append(out, Event{
			ID:       ev.ID,
			At:       ev.Timestamp,
			Type:     typ,
			Content:  ev.Content,
			Metadata: copyMetaWithout(ev.Metadata, "event_type"),
			RunID:    ev.RunID,
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })

	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

// Close implements [Memory.Close].
func (m *memory) Close() error {
	var firstErr error
	if m.conn != nil {
		if err := m.conn.Close(); err != nil {
			firstErr = err
		}
		m.conn = nil
	}
	if m.chronos != nil && m.chronosOwned {
		if err := m.chronos.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	m.chronos = nil
	return firstErr
}

// mergeMetadata produces the input metadata map for ingest.IngestText
// from an Item. The "source" key follows the same convention as the CLI
// (defaults to "raw_text" when none supplied).
func mergeMetadata(item Item) map[string]string {
	out := map[string]string{}
	maps.Copy(out, item.Metadata)
	if item.Source != "" {
		out["source"] = item.Source
	}
	return out
}

func copyMetaWithout(in map[string]string, skip string) map[string]string {
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

// newEventID returns a fresh event id. Mirrors internal/parser's id
// strategy without exporting it.
func newEventID() string {
	return fmt.Sprintf("ev_%d", time.Now().UnixNano())
}

// textGenAdapter wraps a [providers.TextGenerator] to satisfy the
// internal llm.Client contract Mnemos's engines consume.
type textGenAdapter struct {
	tg providers.TextGenerator
}

func newTextGenAdapter(tg providers.TextGenerator) llm.Client {
	return &textGenAdapter{tg: tg}
}

// Complete satisfies llm.Client by translating between the internal
// message shape and the public providers.TextGenerator surface.
func (a *textGenAdapter) Complete(ctx context.Context, messages []llm.Message) (llm.Response, error) {
	in := providers.GenerateTextInput{
		Messages: make([]providers.Message, len(messages)),
	}
	for i, m := range messages {
		in.Messages[i] = providers.Message{
			Role:    providers.Role(string(m.Role)),
			Content: m.Content,
		}
	}
	out, err := a.tg.GenerateText(ctx, in)
	if err != nil {
		return llm.Response{}, err
	}
	return llm.Response{
		Content:      out.Content,
		Model:        out.Model,
		InputTokens:  out.InputTokens,
		OutputTokens: out.OutputTokens,
	}, nil
}

// embedderAdapter wraps a [providers.Embedder] to satisfy the internal
// embedding.Client contract.
type embedderAdapter struct {
	e providers.Embedder
}

func newEmbedderAdapter(e providers.Embedder) embedding.Client {
	return &embedderAdapter{e: e}
}

// Embed satisfies embedding.Client by delegating to the public
// providers.Embedder.
func (a *embedderAdapter) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out, err := a.e.Embed(ctx, providers.EmbedInput{Texts: texts})
	if err != nil {
		return nil, err
	}
	return out.Vectors, nil
}
