package mnemos

import (
	"github.com/felixgeelhaar/chronos/embed"
	"github.com/felixgeelhaar/mnemos/providers"
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
