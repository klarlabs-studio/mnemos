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
	actionRemember       = "remember"
	actionRememberClaim  = "remember_claim"
	actionRememberEvent  = "remember_event"
	actionRecordDecision = "record_decision"
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
		{Name: actionRecordDecision, Effect: axidomain.EffectWriteLocal, Idempotent: false,
			Description: "Record an agent decision (belief -> plan -> outcome audit record)."},
	}
}

// buildWriteKernel assembles the axi kernel that governs every public
// write on m. The kernel is never optional: New always builds one so no
// write path can bypass the evidence chain + budget.
func buildWriteKernel(m *memory, budget axi.Budget, evidenceLogPath string) (*kernel.Governed, error) {
	executors := map[string]axidomain.ActionExecutor{
		kernel.ExecutorRef(actionRemember):       rememberExecutor{m: m},
		kernel.ExecutorRef(actionRememberClaim):  rememberClaimExecutor{m: m},
		kernel.ExecutorRef(actionRememberEvent):  rememberEventExecutor{m: m},
		kernel.ExecutorRef(actionRecordDecision): recordDecisionExecutor{m: m},
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
//
// # Which failures leave an auditable session
//
// axi persists (Save()s) the execution session on every path where one
// exists, so LastWriteSession reflects the write afterwards in these
// cases:
//
//   - Success — the session carries the full evidence chain.
//   - Post-execution business failure — an executor returning an error
//     surfaces as res.Failure with a saved session (its evidence chain
//     holds whatever the executor recorded before failing).
//   - Budget failure (BUDGET_EXCEEDED) — same: the session exists and is
//     auditable.
//   - Hard kernel error mid-execution — an unregistered executor or a
//     ctx-cancel surfaced during execution makes Execute return
//     (nil, err). axi still Save()s the session; we recover it via
//     LastSession() so the failed write is NOT silently trail-less.
//
// The only genuinely session-less failures are pre-execution ones: an
// invalid action name or input rejected before NewExecutionSession runs
// (e.g. a ctx already cancelled before the kernel starts the session).
// Those never reach Save, so no session can exist — LastWriteSession
// keeps reporting the prior write (or nil). The public methods guard
// their own required-field validation before reaching here, so the
// session-less window is limited to a pre-start ctx cancellation.
func dispatchWrite[Out any](ctx context.Context, m *memory, action string, input any) (Out, error) {
	var zero Out

	// Cumulative budget PRE-execution gate. When the Memory's token
	// budget is already exhausted, reject before the executor runs so no
	// orphaned data is persisted. The write that *crosses* the threshold
	// is not blocked here — its model cost isn't known until the work
	// runs — it persists and then reports post-hoc below.
	if err := m.checkTokenBudgetExhausted(action); err != nil {
		return zero, err
	}

	res, err := m.wkernel.Execute(ctx, axi.Invocation{
		Action: kernel.ActionName(action),
		Input:  map[string]any{inputPayloadKey: input},
	})
	if err != nil {
		// Hard kernel error: Execute returned no result, but axi has
		// already Save()d the session for every path that started one.
		// Recover it so a failed write still leaves an auditable trail —
		// the comment that promised this used to lie because this branch
		// returned early.
		if session := m.wkernel.LastSession(); session != nil {
			m.setLastSession(session)
		}
		return zero, err
	}

	// Post-execution outcome: the session is obtainable by its id.
	var session *axidomain.ExecutionSession
	if s, serr := m.wkernel.GetSession(string(res.SessionID)); serr == nil {
		session = s
		m.setLastSession(s)
	}

	if res.Failure != nil {
		return zero, fmt.Errorf("mnemos: %s: %s: %s", action, res.Failure.Code, res.Failure.Message)
	}
	if res.Result == nil {
		return zero, fmt.Errorf("mnemos: %s: kernel returned no result", action)
	}

	// Account this write's token spend against the cumulative budget. If
	// it crossed the threshold, the data already persisted (above) but we
	// report the breach post-hoc, mirroring axi's own post-execution
	// MaxTokens semantics. Subsequent writes hit the pre-gate.
	if err := m.accountTokenSpend(action, session); err != nil {
		return zero, err
	}

	if typed, ok := res.Result.Data.(Out); ok {
		return typed, nil
	}
	return zero, fmt.Errorf("mnemos: %s: unexpected result type %T", action, res.Result.Data)
}

// checkTokenBudgetExhausted enforces the cumulative-budget PRE-execution
// gate. Returns a budget error (no executor runs, nothing persists) when
// the Memory's token budget is already fully spent.
func (m *memory) checkTokenBudgetExhausted(action string) error {
	if m.tokenBudget <= 0 {
		return nil
	}
	spent := m.tokensSpent.Load()
	if spent >= m.tokenBudget {
		return fmt.Errorf("mnemos: %s: BUDGET_EXCEEDED: token budget %d already exhausted (used %d)",
			action, m.tokenBudget, spent)
	}
	return nil
}

// accountTokenSpend adds the just-completed write's token spend to the
// cumulative counter and reports a budget breach when this write crossed
// the threshold. The write has already persisted by the time this runs —
// its cost is only knowable after the model ran — so the breach is
// reported post-hoc rather than preventing the write.
func (m *memory) accountTokenSpend(action string, session *axidomain.ExecutionSession) error {
	if m.tokenBudget <= 0 || session == nil {
		return nil
	}
	var spend int64
	for _, rec := range session.Evidence() {
		spend += rec.TokensUsed
	}
	if spend == 0 {
		return nil
	}
	total := m.tokensSpent.Add(spend)
	if total >= m.tokenBudget {
		return fmt.Errorf("mnemos: %s: BUDGET_EXCEEDED: token budget %d exceeded (used %d)",
			action, m.tokenBudget, total)
	}
	return nil
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
