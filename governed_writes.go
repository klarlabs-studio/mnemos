package mnemos

import (
	"context"
	"fmt"

	"go.klarlabs.de/axi"
	axidomain "go.klarlabs.de/axi/domain"
	"go.klarlabs.de/mnemos/internal/extract"
	"go.klarlabs.de/mnemos/internal/kernel"
)

// Write action names. These map, via kernel.ActionName, to the
// dash-cased axi action identifiers the kernel registers:
//
//	actionRemember      -> mnemos-remember        (write-local, non-idempotent)
//	actionRememberClaim -> mnemos-remember-claim  (write-local, non-idempotent)
//	actionRememberEvent -> mnemos-remember-event  (write-local, idempotent on event id)
const (
	actionRemember      = "remember"
	actionRememberClaim = "remember_claim"
	actionRememberEvent = "remember_event"
)

// evidenceSourceLibrary tags every library-originated evidence record so
// an auditor can tell a public-API write from an MCP-tool write.
const evidenceSourceLibrary = "mnemos.library"

// writeActions returns the descriptors for the three governed library
// writes. One source of truth so the kernel plugin and the executor
// bindings can't drift.
func writeActions() []kernel.Action {
	return []kernel.Action{
		{Name: actionRemember, Effect: axidomain.EffectWriteLocal, Idempotent: false,
			Description: "Ingest text, extract claims, detect relationships, and persist them."},
		{Name: actionRememberClaim, Effect: axidomain.EffectWriteLocal, Idempotent: false,
			Description: "Persist a pre-built claim and link it to source events."},
		{Name: actionRememberEvent, Effect: axidomain.EffectWriteLocal, Idempotent: true,
			Description: "Append a temporal event and forward it to the temporal engine."},
	}
}

// buildWriteKernel assembles the axi kernel that governs every public
// write on m. The kernel is never optional: New always builds one so no
// write path can bypass the evidence chain + budget.
func buildWriteKernel(m *memory, budget axi.Budget, evidenceLogPath string) (*axi.Kernel, error) {
	executors := map[string]axidomain.ActionExecutor{
		kernel.ExecutorRef(actionRemember):      rememberExecutor{m: m},
		kernel.ExecutorRef(actionRememberClaim): rememberClaimExecutor{m: m},
		kernel.ExecutorRef(actionRememberEvent): rememberEventExecutor{m: m},
	}
	return kernel.Build(m.logger, writeActions(), executors, budget, evidenceLogPath)
}

// inputPayloadKey is the single map key under which dispatchWrite stows
// the typed write input. axi.Invocation.Input is a map[string]any, so we
// box the rich Go input (Item / ClaimItem / Event, which carry
// time.Time and nested maps) under one key rather than flattening it
// through a lossy JSON round-trip. The executor unboxes via
// [writeInput].
const inputPayloadKey = "payload"

// writeInput unboxes the typed payload an executor received from
// dispatchWrite. Returns ok=false when the input isn't the expected
// boxed shape.
func writeInput[In any](input any) (In, bool) {
	var zero In
	m, ok := input.(map[string]any)
	if !ok {
		return zero, false
	}
	typed, ok := m[inputPayloadKey].(In)
	return typed, ok
}

// dispatchWrite runs a write action through the kernel, records the
// resulting session as the Memory's LastWriteSession, and decodes the
// typed result. The executor performs the actual persistence; this
// function is the single chokepoint every public write funnels through.
func dispatchWrite[Out any](ctx context.Context, m *memory, action string, input any) (Out, error) {
	var zero Out

	res, err := m.wkernel.Execute(ctx, axi.Invocation{
		Action: kernel.ActionName(action),
		Input:  map[string]any{inputPayloadKey: input},
	})
	if err != nil {
		return zero, err
	}

	// Capture the session for LastWriteSession regardless of outcome so
	// a failed write still leaves an auditable trail.
	if session, serr := m.wkernel.GetSession(string(res.SessionID)); serr == nil {
		m.setLastSession(session)
	}

	if res.Failure != nil {
		return zero, fmt.Errorf("mnemos: %s: %s: %s", action, res.Failure.Code, res.Failure.Message)
	}
	if res.Result == nil {
		return zero, fmt.Errorf("mnemos: %s: kernel returned no result", action)
	}
	if typed, ok := res.Result.Data.(Out); ok {
		return typed, nil
	}
	return zero, fmt.Errorf("mnemos: %s: unexpected result type %T", action, res.Result.Data)
}

// llmEvidence projects extractor token usage into a Kind="llm" evidence
// record so axi-go's ExecutionBudget.MaxTokens sums real spend across a
// session. Returns nil when no tokens were reported (rule-based path,
// cache hit, or a provider that doesn't surface usage), mirroring
// cmd/mnemos's processTextEvidence behaviour.
func llmEvidence(usage *extract.TokenUsage) []axidomain.EvidenceRecord {
	if usage == nil || (usage.InputTokens == 0 && usage.OutputTokens == 0) {
		return nil
	}
	source := usage.Model
	if source == "" {
		source = "unknown"
	}
	return []axidomain.EvidenceRecord{
		{
			Kind:   "llm",
			Source: source,
			Value: map[string]any{
				"input_tokens":  usage.InputTokens,
				"output_tokens": usage.OutputTokens,
				"model":         usage.Model,
			},
			// Total = input + output so a token budget covers both
			// directions, matching the MCP-side convention.
			TokensUsed: int64(usage.InputTokens + usage.OutputTokens),
		},
	}
}
