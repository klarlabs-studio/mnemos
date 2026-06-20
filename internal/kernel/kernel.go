// Package kernel provides the library-core axi-go governance layer for
// Mnemos. Every public Mnemos write routes through an axi.Kernel built
// here so the spec non-negotiable holds: no direct storage write may
// bypass the kernel. The kernel gives each write an effect-gated
// execution, a tamper-evident per-session evidence chain, and a token /
// invocation / duration budget.
//
// This package owns the *infrastructure* (action descriptors, the axi
// plugin, the bolt logger + publisher adapters, the JSONL evidence
// sink, and the env-driven budget). The action *executors* live in the
// consuming package (the public mnemos package, cmd/mnemos) because
// they capture storage handles and engine state the static metadata
// must not leak.
//
// The same builder backs both the public library writes and the MCP
// tool surface in cmd/mnemos, so the underscore→dash action-name
// convention and the budget knobs stay defined in exactly one place.
package kernel

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"go.klarlabs.de/axi"
	"go.klarlabs.de/axi/domain"
	"go.klarlabs.de/bolt"
)

// ActionPrefix namespaces every Mnemos axi action. axi-go's action-name
// regex disallows underscores, so callers map MCP-style snake_case names
// through [ActionName] at the boundary while the kernel sees dashes.
const ActionPrefix = "mnemos-"

// Action describes one kernel-governed action: a stable name, its effect
// level, whether it is idempotent, and a human-readable description.
// Both the static plugin (metadata) and the executor binding key off the
// same Name, so registration and dispatch can't drift.
type Action struct {
	// Name is the bare action name without [ActionPrefix] and using
	// snake_case (e.g. "remember", "remember_claim"). [ActionName]
	// prepends the prefix and converts underscores to dashes.
	Name string
	// Effect is the axi effect level. Library writes are
	// domain.EffectWriteLocal (auto-approved, no human gate) but still
	// produce the per-session evidence chain + budget.
	Effect domain.EffectLevel
	// Idempotent marks actions that are safe to retry on the same input
	// without duplicating state (e.g. remember_event is idempotent on
	// event id).
	Idempotent bool
	// Description is the one-line action summary surfaced via Help.
	Description string
}

// ActionName returns the axi-go-compatible action name for a bare name:
// the [ActionPrefix] followed by the name with underscores replaced by
// dashes. Centralising the conversion keeps the regex dependency here.
func ActionName(bare string) string {
	return ActionPrefix + strings.ReplaceAll(bare, "_", "-")
}

// ExecutorRef returns the canonical executor binding ref for a bare
// action name. Mirrors the "exec." convention used across the codebase.
func ExecutorRef(bare string) string {
	return "exec." + bare
}

// Build assembles an axi.Kernel pre-registered with every supplied
// action and its executor. The plugin contributes the static action
// definitions; executors are passed in by the caller (keyed by
// [ExecutorRef]) so they can carry runtime state.
//
// The kernel logs every domain event through the bolt logger and, when
// evidenceLogPath (or MNEMOS_AXI_EVIDENCE_LOG) is set, also appends a
// JSONL audit line per event. The budget governs token / invocation /
// duration spend per session.
//
// logger may be nil; a discarding logger is substituted so library
// consumers that don't want axi chatter on stderr pay nothing.
func Build(logger *bolt.Logger, actions []Action, executors map[string]domain.ActionExecutor, budget axi.Budget, evidenceLogPath string) (*axi.Kernel, error) {
	if logger == nil {
		logger = bolt.New(bolt.NewJSONHandler(noopWriter{}))
	}

	plugin, err := newPlugin(actions)
	if err != nil {
		return nil, fmt.Errorf("kernel: build plugin: %w", err)
	}

	publishers := []domain.DomainEventPublisher{
		boltPublisher{logger: logger},
	}
	sink, sinkErr := newEvidenceSink(evidenceLogPath)
	if sinkErr != nil {
		logger.Warn().Err(sinkErr).Msg("axi evidence sink disabled")
	} else if sink != nil {
		publishers = append(publishers, sink)
	}

	k := axi.New().
		WithLogger(boltLogger{logger: logger}).
		WithDomainEventPublisher(multiPublisher(publishers)).
		WithBudget(budget)

	for ref, exec := range executors {
		k.RegisterActionExecutor(ref, exec)
	}
	if err := k.RegisterPlugin(plugin); err != nil {
		return nil, fmt.Errorf("kernel: register plugin: %w", err)
	}
	return k, nil
}

// plugin is the static axi plugin describing every Mnemos action. It
// contributes definitions only; executors are registered separately so
// they can carry runtime state without leaking into static metadata.
type plugin struct {
	actions []*domain.ActionDefinition
}

// Contribute satisfies domain.Plugin.
func (p plugin) Contribute() (*domain.PluginContribution, error) {
	return domain.NewPluginContribution("mnemos.kernel", p.actions, nil)
}

func newPlugin(actions []Action) (plugin, error) {
	p := plugin{}
	for _, a := range actions {
		name, err := domain.NewActionName(ActionName(a.Name))
		if err != nil {
			return p, fmt.Errorf("invalid axi action name for %s: %w", a.Name, err)
		}
		def, err := domain.NewActionDefinition(
			name,
			a.Description,
			domain.EmptyContract(), // typed validation happens at the call boundary
			domain.EmptyContract(),
			nil,
			domain.EffectProfile{Level: a.Effect},
			domain.IdempotencyProfile{IsIdempotent: a.Idempotent},
		)
		if err != nil {
			return p, fmt.Errorf("define action %s: %w", a.Name, err)
		}
		if err := def.BindExecutor(domain.ActionExecutorRef(ExecutorRef(a.Name))); err != nil {
			return p, fmt.Errorf("bind executor for %s: %w", a.Name, err)
		}
		p.actions = append(p.actions, def)
	}
	return p, nil
}

// BudgetFromEnv reads MNEMOS_AXI_MAX_DURATION (Go duration string),
// MNEMOS_AXI_MAX_INVOCATIONS (positive int), and MNEMOS_AXI_MAX_TOKENS
// (positive int64) and returns the resulting budget. Unset / invalid
// values fall back to permissive defaults: 5 minutes / 1000 invocations
// / unlimited tokens, so existing flows don't break when the kernel is
// made mandatory. Token usage is reported on a Kind="llm" evidence
// record per write (input + output summed), so setting MaxTokens here
// actually engages the kernel's per-session budget enforcer.
func BudgetFromEnv() axi.Budget {
	b := axi.Budget{
		MaxDuration:              5 * time.Minute,
		MaxCapabilityInvocations: 1000,
	}
	if v := os.Getenv("MNEMOS_AXI_MAX_DURATION"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			b.MaxDuration = d
		}
	}
	if v := os.Getenv("MNEMOS_AXI_MAX_INVOCATIONS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			b.MaxCapabilityInvocations = n
		}
	}
	if v := os.Getenv("MNEMOS_AXI_MAX_TOKENS"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			b.MaxTokens = n
		}
	}
	return b
}

// noopWriter discards everything. Used as the default bolt sink when a
// library consumer passes a nil logger to [Build].
type noopWriter struct{}

func (noopWriter) Write(p []byte) (int, error) { return len(p), nil }
