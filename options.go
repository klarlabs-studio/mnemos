package mnemos

import (
	"github.com/felixgeelhaar/chronos/embed"
	"go.klarlabs.de/bolt"
	"go.klarlabs.de/mnemos/providers"
)

// Option configures [New]. Options are constructed by the With* helpers
// in this package. The interface is intentionally sealed (its method is
// unexported) so the option set can grow without breaking existing
// callers.
type Option interface {
	applyOption(*config)
}

// ProviderConfig describes a dedicated language-model + embedding
// provider configuration. Used with [WithEnhancedMode] when Mnemos
// should run its own model for background enrichment (typically a
// smaller/cheaper model than the host agent uses).
//
// LLMProvider names the family: "anthropic", "openai", "gemini",
// "ollama", or "openai-compat". LLMAPIKey, LLMModel, and LLMBaseURL fill
// in the connection details; the defaults match the corresponding
// MNEMOS_LLM_* environment variables.
//
// EmbedProvider, EmbedAPIKey, EmbedModel, and EmbedBaseURL configure the
// embedding side. When EmbedProvider is empty, the embedding side falls
// back to the LLM-side fields (mirroring the env-var behaviour). Note
// that Anthropic does not offer an embedding API; pair it with a
// separate embedding provider (Voyage, OpenAI, Ollama, ...).
type ProviderConfig struct {
	LLMProvider string
	LLMAPIKey   string
	LLMModel    string
	LLMBaseURL  string

	EmbedProvider string
	EmbedAPIKey   string
	EmbedModel    string
	EmbedBaseURL  string
}

// mode tracks which of the three operating modes was selected. Callers
// who pass conflicting options get a clear error from [New] rather than
// silent precedence rules.
type mode int

const (
	modeUnset mode = iota
	modePassive
	modeShared
	modeEnhanced
)

// config is the resolved configuration assembled from the Option set.
// Internal only — callers construct via Option helpers.
type config struct {
	mode        mode
	textGen     providers.TextGenerator
	embedder    providers.Embedder
	enhancedCfg ProviderConfig
	storageDSN  string
	actorID     string

	// chronos, when non-nil, overrides the default embedded Chronos
	// engine [New] boots. Supplied by [WithChronos]; ownership stays
	// with the caller (Mnemos will not call its Close).
	chronos *embed.Engine
	// chronosOwned tracks whether [New] constructed the Chronos engine
	// itself (true) or accepted one from the caller (false). Drives
	// whether [Memory.Close] also closes the engine.
	chronosOwned bool

	// tokenBudget overrides the kernel's MaxTokens cap on every write.
	// Zero (the default) means "use the env-derived value"
	// (MNEMOS_AXI_MAX_TOKENS, itself defaulting to unlimited). Set via
	// [WithTokenBudget].
	tokenBudget int64
	// tokenBudgetSet records whether [WithTokenBudget] was called, so a
	// caller can explicitly select an unlimited budget (0) and override
	// a non-zero env value.
	tokenBudgetSet bool
	// evidenceLog overrides the kernel's JSONL audit-log path. Empty
	// means "use MNEMOS_AXI_EVIDENCE_LOG" (itself defaulting to
	// disabled). Set via [WithEvidenceLog].
	evidenceLog string

	// logger, when non-nil, backs the governed-write kernel's
	// domain-event log. Nil (the default) keeps axi chatter off stderr.
	// Set via [WithLogger].
	logger *bolt.Logger
}

type optionFunc func(*config)

func (f optionFunc) applyOption(c *config) { f(c) }

// WithPassiveMode selects the no-LLM mode. Claim extraction uses the
// built-in rule-based engine; query ranking falls back to token overlap
// when no embeddings exist. This is the default if no mode option is
// supplied.
//
// Use this mode when:
//   - You don't have a language-model provider configured.
//   - You want zero per-call cost.
//   - You're embedding Mnemos in a test or smoke-demo environment.
func WithPassiveMode() Option {
	return optionFunc(func(c *config) {
		c.mode = modePassive
		c.textGen = nil
		c.embedder = nil
	})
}

// WithSharedProvider configures Mnemos to use a [providers.TextGenerator]
// and optional [providers.Embedder] supplied by the caller. Use this
// mode when an agent runtime already has a model client configured and
// wants Mnemos to share the same key, model, and budget.
//
// embedder may be nil; Mnemos falls back to token-overlap ranking when
// embeddings are not supplied. Pairing a TextGenerator with no Embedder
// is supported and useful when the consumer's provider doesn't expose
// an embedding API (e.g. Anthropic) and the consumer doesn't want to
// configure a second provider just for embeddings.
func WithSharedProvider(tg providers.TextGenerator, embedder providers.Embedder) Option {
	return optionFunc(func(c *config) {
		c.mode = modeShared
		c.textGen = tg
		c.embedder = embedder
	})
}

// WithEnhancedMode configures Mnemos to use its own dedicated
// language-model + embedding provider, separate from any host agent's
// configuration. Useful when you want background enrichment on a
// cheaper or specialised model, or when separating billing.
func WithEnhancedMode(cfg ProviderConfig) Option {
	return optionFunc(func(c *config) {
		c.mode = modeEnhanced
		c.enhancedCfg = cfg
	})
}

// WithStorage overrides the storage DSN. The DSN selects the provider by
// URL scheme (e.g. "sqlite://", "memory://", "postgres://", "mysql://",
// "libsql://"). The corresponding provider package must be blank-imported
// by the consuming program for the scheme to be registered.
//
// When unset, [New] resolves the DSN the same way the mnemos CLI does:
// MNEMOS_DB_URL env > project-local ./.mnemos/mnemos.db (walked up like
// .git) > XDG default ~/.local/share/mnemos/mnemos.db.
func WithStorage(dsn string) Option {
	return optionFunc(func(c *config) {
		c.storageDSN = dsn
	})
}

// WithActor sets the user/agent id Mnemos stamps onto every write. Use
// this when you need attribution different from the MNEMOS_USER_ID env
// var (which is the default). Passing an empty string is a no-op.
func WithActor(userID string) Option {
	return optionFunc(func(c *config) {
		if userID == "" {
			return
		}
		c.actorID = userID
	})
}

// WithTokenBudget sets a cumulative language-model token budget across
// all governed writes on the returned [Memory]. The budget is enforced
// in two complementary places, because a write's true model cost is only
// known after the extraction runs:
//
//   - PRE-execution gate: when the budget is already exhausted (the
//     accumulated spend has reached maxTokens), the next write is
//     rejected BEFORE its executor runs. The model is not called and
//     nothing is persisted — no orphaned data. The error mentions the
//     budget.
//
//   - Post-hoc report: the single write that CROSSES the threshold (spend
//     was under budget at the start, over it after) still persists, then
//     returns a budget error reporting the breach. Its tokens weren't
//     knowable until the work ran, so blocking it pre-execution is
//     impossible; this is inherent, not a workaround. Subsequent writes
//     hit the pre-execution gate above.
//
// The budget is cumulative for the lifetime of the Memory handle and
// counts the per-write "llm" token spend reported on each write's
// evidence chain (rule-based / cached writes report zero and never
// advance it). It is independent of axi's per-write MaxTokens cap, which
// is env-driven (MNEMOS_AXI_MAX_TOKENS) and bounds a single session's
// spend. The duration and invocation caps remain env-driven via
// MNEMOS_AXI_MAX_DURATION / MNEMOS_AXI_MAX_INVOCATIONS.
//
// Zero selects an unlimited cumulative budget. A negative value is
// clamped to zero.
func WithTokenBudget(maxTokens int64) Option {
	return optionFunc(func(c *config) {
		if maxTokens < 0 {
			maxTokens = 0
		}
		c.tokenBudget = maxTokens
		c.tokenBudgetSet = true
	})
}

// WithEvidenceLog routes the kernel's per-write evidence chain to a JSONL
// audit log at the given path (parent directories are created as needed).
// The special values "-" and "stdout" route to standard output. This
// mirrors the MNEMOS_AXI_EVIDENCE_LOG environment variable, which is used
// when this option is not set; passing an empty string is a no-op.
//
// The evidence chain is always recorded in-memory on the [WriteSession]
// regardless of this option; the log adds durable cross-session audit.
func WithEvidenceLog(path string) Option {
	return optionFunc(func(c *config) {
		if path == "" {
			return
		}
		c.evidenceLog = path
	})
}

// WithChronos supplies an existing [embed.Engine] for Mnemos to use as
// its temporal backend instead of booting a default one. Useful when:
//
//   - The host already runs Chronos for other purposes and wants to
//     share the same engine + storage.
//   - You need a non-default Chronos configuration (custom detector
//     set, parallel detection, dedicated SQL storage).
//
// Ownership of the supplied engine stays with the caller; [Memory.Close]
// will NOT call [embed.Engine.Close] on it. When this option is not
// used, [New] constructs a default in-memory engine and owns it.
func WithChronos(eng *embed.Engine) Option {
	return optionFunc(func(c *config) {
		c.chronos = eng
		c.chronosOwned = false
	})
}

// WithLogger injects a [bolt.Logger] for the governed-write kernel's
// structured domain-event log. By default Mnemos keeps the kernel silent
// (nil logger); supply one when you want governed-write events on your own
// sink. bolt's slog bridge ([bolt.NewSlogHandler]) lets you route a
// standard library *slog.Logger here too.
//
// Passing nil is a no-op (keeps the silent default).
func WithLogger(logger *bolt.Logger) Option {
	return optionFunc(func(c *config) {
		if logger == nil {
			return
		}
		c.logger = logger
	})
}

// WithSQLite is a convenience alias for [WithStorage] that builds a
// "sqlite://<path>" DSN from a filesystem path. The sqlite provider
// package must be blank-imported by the consuming program for the scheme
// to be registered:
//
//	import _ "go.klarlabs.de/mnemos/sqlite"
//
// Equivalent to WithStorage("sqlite://" + path). Passing an empty path is
// a no-op (the default DSN resolution applies).
func WithSQLite(path string) Option {
	return optionFunc(func(c *config) {
		if path == "" {
			return
		}
		c.storageDSN = "sqlite://" + path
	})
}
