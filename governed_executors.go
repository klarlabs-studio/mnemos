package mnemos

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"time"

	axidomain "go.klarlabs.de/axi/domain"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ingest"
	"go.klarlabs.de/mnemos/internal/parser"
	"go.klarlabs.de/mnemos/internal/pipeline"
)

// The executors below carry the actual persistence logic for the three
// public writes. They run inside the axi kernel — the only place in the
// library where conn.*.Append / Upsert / PersistArtifacts is reachable.
// Each returns an []axidomain.EvidenceRecord describing the write (ids,
// counts, and an "llm" record with TokensUsed when a model ran) so the
// session's evidence chain is populated and the MaxTokens budget can sum
// real spend.

// rememberInput is the typed input the remember executor receives.
type rememberInput struct {
	Item Item
}

// rememberOutput is what the remember executor returns. Remember's
// public signature is (error) only, so callers ignore the payload; it
// exists so the kernel has a non-nil result on success.
type rememberOutput struct {
	Events        int
	Claims        int
	Relationships int
}

type rememberExecutor struct{ m *memory }

// Execute runs the full ingest -> normalize -> extract -> relate ->
// persist pipeline that previously lived directly in Memory.Remember.
func (e rememberExecutor) Execute(ctx context.Context, input any, _ axidomain.CapabilityInvoker) (axidomain.ExecutionResult, []axidomain.EvidenceRecord, error) {
	in, ok := writeInput[rememberInput](input)
	if !ok {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("remember: unexpected input type %T", input)
	}
	m := e.m
	item := in.Item

	svc := ingest.NewService()
	ingestIn, content, err := svc.IngestText(item.Content, mergeMetadata(item))
	if err != nil {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("ingest: %w", err)
	}

	events, err := parser.NewNormalizer().Normalize(ingestIn, content)
	if err != nil {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("normalize: %w", err)
	}
	for i := range events {
		if item.RunID != "" {
			events[i].RunID = item.RunID
		}
		events[i].CreatedBy = m.actorID
	}

	claims, links, _, err := m.extractor.ExtractFn(events)
	if err != nil {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("extract: %w", err)
	}
	usage := m.extractor.LastUsage()
	for i := range claims {
		claims[i].CreatedBy = m.actorID
		if item.Type != "" {
			claims[i].Type = domain.ClaimType(item.Type)
		}
	}

	rels, err := m.relator.Detect(claims)
	if err != nil {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("relate: %w", err)
	}
	existing, lerr := m.conn.Claims.ListAll(ctx)
	if lerr == nil && len(existing) > 0 {
		if incremental, irelErr := m.relator.DetectIncremental(claims, existing); irelErr == nil {
			rels = append(rels, incremental...)
		}
	}
	for i := range rels {
		rels[i].CreatedBy = m.actorID
	}

	if err := pipeline.PersistArtifacts(ctx, m.conn, events, claims, links, rels); err != nil {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("persist: %w", err)
	}

	out := rememberOutput{Events: len(events), Claims: len(claims), Relationships: len(rels)}
	records := []axidomain.EvidenceRecord{
		{Kind: "mnemos.remember", Source: evidenceSourceLibrary, Value: map[string]any{
			"events":        out.Events,
			"claims":        out.Claims,
			"relationships": out.Relationships,
			"run_id":        item.RunID,
		}},
	}
	records = append(records, llmEvidence(usage)...)

	return axidomain.ExecutionResult{
		Data:    out,
		Summary: fmt.Sprintf("remembered events=%d claims=%d relationships=%d", out.Events, out.Claims, out.Relationships),
	}, records, nil
}

// rememberClaimInput is the typed input the remember-claim executor
// receives.
type rememberClaimInput struct {
	Claim ClaimItem
}

// rememberClaimOutput carries the generated claim id back to the public
// RememberClaim method.
type rememberClaimOutput struct {
	ClaimID string
}

type rememberClaimExecutor struct{ m *memory }

// Execute persists a pre-built claim (and any evidence links) — the
// logic previously inline in Memory.RememberClaim.
func (e rememberClaimExecutor) Execute(ctx context.Context, input any, _ axidomain.CapabilityInvoker) (axidomain.ExecutionResult, []axidomain.EvidenceRecord, error) {
	in, ok := writeInput[rememberClaimInput](input)
	if !ok {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("remember_claim: unexpected input type %T", input)
	}
	m := e.m
	item := in.Claim

	// Defense-in-depth for the spec non-negotiable "claims require
	// evidence". The public RememberClaim already rejects evidence-less
	// claims, but the executor is the single persistence chokepoint, so it
	// enforces the invariant too — no internal path can upsert a claim
	// without backing evidence.
	evidenceIDs := nonBlank(item.EventIDs)
	if len(evidenceIDs) == 0 {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("remember_claim: claims require evidence (no valid EventIDs)")
	}

	claimType := domain.ClaimType(item.Type)
	if claimType == "" {
		claimType = domain.ClaimTypeFact
	}
	confidence := item.Confidence
	if confidence == 0 {
		confidence = 1.0
	}

	id := newClaimID()
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
		Lifecycle:  domain.ClaimLifecycle(item.Lifecycle),
	}

	if err := m.conn.Claims.Upsert(ctx, []domain.Claim{claim}); err != nil {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("upsert claim: %w", err)
	}

	links := make([]domain.ClaimEvidence, len(evidenceIDs))
	for i, eid := range evidenceIDs {
		links[i] = domain.ClaimEvidence{ClaimID: id, EventID: eid}
	}
	if err := m.conn.Claims.UpsertEvidence(ctx, links); err != nil {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("link evidence: %w", err)
	}
	linkCount := len(links)

	records := []axidomain.EvidenceRecord{
		{Kind: "mnemos.remember_claim", Source: evidenceSourceLibrary, Value: map[string]any{
			"claim_id":       id,
			"type":           string(claimType),
			"confidence":     confidence,
			"evidence_links": linkCount,
			"run_id":         item.RunID,
		}},
	}
	return axidomain.ExecutionResult{
		Data:    rememberClaimOutput{ClaimID: id},
		Summary: fmt.Sprintf("remembered claim %s (links=%d)", id, linkCount),
	}, records, nil
}

// rememberEventInput is the typed input the remember-event executor
// receives.
type rememberEventInput struct {
	Event Event
}

// rememberEventOutput carries the resolved event id back to the public
// method.
type rememberEventOutput struct {
	EventID string
}

type rememberEventExecutor struct{ m *memory }

// Execute appends a temporal event and forwards it to the bundled
// Chronos engine — the logic previously inline in Memory.RememberEvent.
func (e rememberEventExecutor) Execute(ctx context.Context, input any, _ axidomain.CapabilityInvoker) (axidomain.ExecutionResult, []axidomain.EvidenceRecord, error) {
	in, ok := writeInput[rememberEventInput](input)
	if !ok {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("remember_event: unexpected input type %T", input)
	}
	m := e.m
	ev := in.Event

	id := ev.ID
	if id == "" {
		id = newEventID()
	}
	meta := map[string]string{}
	maps.Copy(meta, ev.Metadata)
	if ev.Type != "" {
		meta["event_type"] = ev.Type
	}

	stored := domain.Event{
		ID:            id,
		RunID:         ev.RunID,
		SchemaVersion: "1.0",
		Content:       ev.Content,
		SourceInputID: "inline:" + id, // synthetic origin for direct API calls
		Timestamp:     ev.At.UTC(),
		Metadata:      meta,
		IngestedAt:    time.Now().UTC(),
		CreatedBy:     m.actorID,
	}
	if err := m.conn.Events.Append(ctx, stored); err != nil {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("append event: %w", err)
	}

	// Forward to Chronos as a presence signal. Chronos failures are
	// non-fatal: the Mnemos event store is the source of truth and was
	// already persisted above.
	if m.chronos != nil {
		_ = m.chronos.Process(ctx, m.eventToEntityState(ev, id))
	}

	records := []axidomain.EvidenceRecord{
		{Kind: "mnemos.remember_event", Source: evidenceSourceLibrary, Value: map[string]any{
			"event_id":   id,
			"event_type": ev.Type,
			"run_id":     ev.RunID,
		}},
	}
	return axidomain.ExecutionResult{
		Data:    rememberEventOutput{EventID: id},
		Summary: fmt.Sprintf("remembered event %s", id),
	}, records, nil
}

type recordDecisionInput struct{ Decision Decision }
type recordDecisionOutput struct{ DecisionID string }

type recordDecisionExecutor struct{ m *memory }

// Execute persists an agent decision (belief -> plan -> outcome audit
// record) through the governed write path, generating an id and defaulting
// ChosenAt/RiskLevel. Empty risk defaults to medium so a caller that only
// records "what was decided" still produces a valid audit row.
func (e recordDecisionExecutor) Execute(ctx context.Context, input any, _ axidomain.CapabilityInvoker) (axidomain.ExecutionResult, []axidomain.EvidenceRecord, error) {
	in, ok := writeInput[recordDecisionInput](input)
	if !ok {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("record_decision: unexpected input type %T", input)
	}
	m := e.m
	d := in.Decision

	id := d.ID
	if id == "" {
		id = newDecisionID()
	}
	chosenAt := d.ChosenAt
	if chosenAt.IsZero() {
		chosenAt = time.Now().UTC()
	}
	risk := domain.RiskLevel(strings.TrimSpace(d.RiskLevel))
	if risk == "" {
		risk = domain.RiskLevelMedium
	}
	dom := domain.Decision{
		ID:           id,
		Statement:    d.Statement,
		Plan:         d.Plan,
		Reasoning:    d.Reasoning,
		RiskLevel:    risk,
		Beliefs:      nonBlank(d.Beliefs),
		Alternatives: d.Alternatives,
		OutcomeID:    strings.TrimSpace(d.OutcomeID),
		ChosenAt:     chosenAt.UTC(),
		CreatedBy:    m.actorID,
		CreatedAt:    time.Now().UTC(),
	}
	if err := dom.Validate(); err != nil {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("record_decision: %w", err)
	}
	if err := m.conn.Decisions.Append(ctx, dom); err != nil {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("append decision: %w", err)
	}

	records := []axidomain.EvidenceRecord{
		{Kind: "mnemos.record_decision", Source: evidenceSourceLibrary, Value: map[string]any{
			"decision_id": id,
			"risk_level":  string(dom.RiskLevel),
			"beliefs":     len(dom.Beliefs),
		}},
	}
	return axidomain.ExecutionResult{
		Data:    recordDecisionOutput{DecisionID: id},
		Summary: fmt.Sprintf("recorded decision %s", id),
	}, records, nil
}
