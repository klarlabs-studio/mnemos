// Package config loads Mnemos configuration from a YAML file and layers it
// underneath the process environment. It follows 12-factor precedence: an
// explicit environment variable always wins, and the YAML file supplies
// values only for keys the operator has not set in the environment.
//
// The file is a convenience for local development and self-hosting, where
// exporting a dozen MNEMOS_* variables is tedious. Nothing downstream reads
// the file directly — Load parses it, Config.EnvOverrides flattens it to the
// canonical MNEMOS_* keys, and Hydrate applies those keys to the environment
// without clobbering anything already set. Every package that reads
// os.Getenv("MNEMOS_...") therefore keeps working unchanged.
package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// EnvConfigPath overrides config-file discovery when set. It mirrors the
// --config flag; the flag takes precedence over the variable.
const EnvConfigPath = "MNEMOS_CONFIG"

// scalar accepts any YAML scalar (string, int, float, bool) and stores its
// textual form. This lets operators write `port: 8080` or `decay: 0.9`
// without quoting, while every value flattens to a string for the
// environment. An absent or empty scalar stays "" and is skipped on hydrate.
type scalar string

// UnmarshalYAML captures any scalar node's raw textual value.
func (s *scalar) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("expected a scalar value, got %v at line %d", node.Tag, node.Line)
	}
	*s = scalar(node.Value)
	return nil
}

// Config mirrors the MNEMOS_* environment surface as a nested YAML document.
// Each leaf maps to exactly one environment variable (see EnvOverrides).
type Config struct {
	DB struct {
		// URL is the brain's storage DSN: sqlite://, postgres://, mysql://,
		// libsql:// or memory://. Unset, the local brain file is used.
		URL scalar `yaml:"url"`
		// MaxConns caps open connections to a networked database (Postgres,
		// MySQL). Default 25.
		MaxConns scalar `yaml:"max_conns"`
		// MaxIdleConns caps idle pooled connections (Postgres, MySQL). Default 5.
		MaxIdleConns scalar `yaml:"max_idle_conns"`
		// ConnMaxLifetime recycles a pooled connection after this Go duration
		// (Postgres, MySQL). Default 30m.
		ConnMaxLifetime scalar `yaml:"conn_max_lifetime"`
		// SharedPool makes a multi-tenant Postgres server check each request's
		// connection out of one shared pool, instead of caching a connection
		// per tenant for the life of the process.
		SharedPool scalar `yaml:"shared_pool"`
	} `yaml:"db"`

	LLM struct {
		// Provider selects the LLM for extraction and relation detection:
		// anthropic, openai, gemini, ollama or openai-compat. Unset, extraction
		// is rule-based.
		Provider scalar `yaml:"provider"`
		// APIKey authenticates to the LLM provider. Secret.
		APIKey scalar `yaml:"api_key"`
		// Model overrides the provider's default model.
		Model scalar `yaml:"model"`
		// BaseURL overrides the provider's endpoint; required for openai-compat.
		BaseURL scalar `yaml:"base_url"`
		// Timeout bounds one LLM request, as a Go duration. Default 120s.
		Timeout scalar `yaml:"timeout"`
		// CacheMaxBytes caps the on-disk LLM response cache. Default 1 GiB; 0
		// disables the size sweep (unbounded cache).
		CacheMaxBytes scalar `yaml:"cache_max_bytes"`
		// ExtractModel overrides llm.model for the extraction stage only, so a
		// strong model can extract while a cheaper one does the rest.
		ExtractModel scalar `yaml:"extract_model"`
		// ExtractBatchChars bounds one extraction request. A whole transcript in
		// a single prompt overran the per-request timeout on local models, so
		// extraction silently fell back to rule-based; batching keeps each
		// request completable. Raise it for fast/large-context providers.
		// Default 24000 characters.
		ExtractBatchChars scalar `yaml:"extract_batch_chars"`
	} `yaml:"llm"`

	Embed struct {
		// Provider selects the embedding model behind semantic recall:
		// openai, gemini, ollama or openai-compat. Unset, it falls back to the
		// LLM provider (Anthropic has no embedding API) or a local Ollama.
		Provider scalar `yaml:"provider"`
		// APIKey authenticates to the embedding provider; falls back to the
		// LLM API key. Secret.
		APIKey scalar `yaml:"api_key"`
		// Model overrides the provider's default embedding model.
		Model scalar `yaml:"model"`
		// BaseURL overrides the embedding endpoint.
		BaseURL scalar `yaml:"base_url"`
		// Timeout bounds one embedding request, as a Go duration. Default 60s.
		Timeout scalar `yaml:"timeout"`
		// Batch caps the texts in one embedding request during `reembed`.
		// Default 64; lower it for self-hosted providers with small batch limits.
		Batch scalar `yaml:"batch"`
	} `yaml:"embed"`

	Serve struct {
		// Port is the REST listener of `mnemos serve`. Default 7777.
		Port scalar `yaml:"port"`
		// TLSCertFile serves HTTPS with this certificate, with serve.tls_key_file.
		TLSCertFile scalar `yaml:"tls_cert_file"`
		// TLSKeyFile is the private key for serve.tls_cert_file.
		TLSKeyFile scalar `yaml:"tls_key_file"`
		// MTLSClientCAFile requires client certificates signed by this CA.
		MTLSClientCAFile scalar `yaml:"mtls_client_ca_file"`
		// PublicReads mirrors `serve --public-reads`: anonymous GET reads. It
		// LOOSENS authentication, so it lives in reviewable config, not only in
		// whichever flags a process started with. Off by default; not for
		// hosted deployments.
		PublicReads scalar `yaml:"public_reads"`
		// MetricsPublic mirrors `serve --metrics-public`: anonymous
		// /internal/metrics. Off by default.
		MetricsPublic scalar `yaml:"metrics_public"`
		// TrustProxy mirrors `serve --trust-proxy`: take the client IP used
		// for rate limiting from X-Forwarded-For. Set it only behind a proxy
		// that overwrites the header; off by default.
		TrustProxy scalar `yaml:"trust_proxy"`
		// ConsolidateInterval is the hosted brain's sleep cadence: how often a
		// running server consolidates (dedupe, trust refresh, credit, the skill
		// loop, session-noise clearing). Defaults to the same ~daily gap the
		// local brain sleeps on; 0 disables the cycle — set that on every
		// replica but one, since the cycle is per process.
		ConsolidateInterval scalar `yaml:"consolidate_interval"`
	} `yaml:"serve"`

	// Server points a CLIENT (e.g. the recall/brief/capture hooks) at a remote
	// hosted mnemos brain over HTTP/REST. Set by `init --url <endpoint> [--token]`.
	// When Server.URL is set, the hooks call the REST API instead of opening a
	// local store. Distinct from Registry (federation) and Serve (this process's
	// own listener).
	Server struct {
		// URL is the hosted brain the hooks and client call instead of a local
		// store.
		URL scalar `yaml:"url"`
		// Token is the bearer token sent to server.url. Secret.
		Token scalar `yaml:"token"`
	} `yaml:"server"`

	Auth struct {
		// JWTSecret signs and verifies tokens: hex, at least 32 bytes. Unset,
		// a per-install secret is created in auth.dir. Secret.
		JWTSecret scalar `yaml:"jwt_secret"`
		// JWTPrevSecret still verifies tokens signed before a secret rotation.
		// Secret.
		JWTPrevSecret scalar `yaml:"jwt_prev_secret"`
		// Dir holds the generated JWT secret. Default: .mnemos in the project
		// root, else the home directory.
		Dir scalar `yaml:"dir"`
		// UserID stamps every write's actor. Unset, writes are attributed to
		// the system user.
		UserID scalar `yaml:"user_id"`
	} `yaml:"auth"`

	Registry struct {
		// URL is the federation registry `push` and `pull` talk to.
		URL scalar `yaml:"url"`
		// Token authenticates to the registry. Secret.
		Token scalar `yaml:"token"`
	} `yaml:"registry"`

	Federation struct {
		// Enabled serves GET /v1/federation/export (anonymized playbooks).
		// Off by default.
		Enabled scalar `yaml:"enabled"`
	} `yaml:"federation"`

	Telemetry struct {
		// OptIn enables anonymized usage counts. Off by default; nothing is
		// sent unless telemetry.endpoint is also set.
		OptIn scalar `yaml:"optin"`
		// Endpoint receives the opted-in usage counts.
		Endpoint scalar `yaml:"endpoint"`
	} `yaml:"telemetry"`

	Kernel struct {
		// MaxDuration bounds one governed write session, as a Go duration.
		// Default 5m.
		MaxDuration scalar `yaml:"max_duration"`
		// MaxInvocations bounds capability calls per session. Default 1000.
		MaxInvocations scalar `yaml:"max_invocations"`
		// MaxTokens bounds LLM tokens per session. Default unlimited.
		MaxTokens scalar `yaml:"max_tokens"`
		// EvidenceLog appends every governed write's evidence as JSONL to this
		// file. Symlinks are refused.
		EvidenceLog scalar `yaml:"evidence_log"`
	} `yaml:"kernel"`

	Feedback struct {
		// ContestThreshold is the negative-feedback count at which a belief
		// becomes contested. Default 3.
		ContestThreshold scalar `yaml:"contest_threshold"`
		// Decay multiplies confidence on each negative feedback. Default 0.9.
		Decay scalar `yaml:"decay"`
	} `yaml:"feedback"`

	Job struct {
		// Timeout bounds one CLI job attempt, as a Go duration. Default 10m.
		// Whole-brain maintenance (`relate --prune-supports`) defaults to 4h
		// unless this is set.
		Timeout scalar `yaml:"timeout"`
	} `yaml:"job"`

	// Query enables the optional retrieval behaviors that otherwise need a
	// per-invocation flag (`query --prime`, `--salient`, ...). All default off.
	//
	// Hebbian, Reconsolidate and Inhibit WRITE during a read: enabling them
	// here makes every query mutate the store. That is the same exposure as
	// exporting the corresponding MNEMOS_* var in a shell profile, but stated
	// in a file that can be reviewed, so it is the better place for a standing
	// choice. Leave them unset to keep reads read-only.
	Query struct {
		// SpreadingActivation primes strongly-associated beliefs (ADR 0013 §2).
		// Read-only ranking. Off unless set to true: it lowered recall
		// quality on LoCoMo (ADR 0030).
		SpreadingActivation scalar `yaml:"spreading_activation"`
		// Salience blends a bounded stakes term into ranking (ADR 0013 §4).
		// Read-only ranking.
		Salience scalar `yaml:"salience"`
		// Hebbian strengthens edges among co-retrieved beliefs (ADR 0015 §4). WRITES.
		Hebbian scalar `yaml:"hebbian"`
		// Reconsolidate re-marks recalled beliefs verified-now (ADR 0015 §5). WRITES.
		Reconsolidate scalar `yaml:"reconsolidate"`
		// Inhibit suppresses retrievability of a beaten contradiction loser
		// (ADR 0016), never its trust. WRITES.
		Inhibit scalar `yaml:"inhibit"`
	} `yaml:"query"`

	// Capture tunes the SessionEnd capture hook. Timeout bounds the whole
	// ingest→extract→relate pipeline for one session (default 4m, sized for a
	// slow local model). It is a ceiling, not a reservation: a fast provider
	// returns as soon as it is done, so cloud/hosted setups can lower it. Note
	// that the Claude Code hook timeout written by `mnemos init` caps this in
	// practice — re-run `mnemos init` after raising it.
	Capture struct {
		// Strategy chooses when the capture hook ingests: auto, incremental,
		// end or off. auto picks incremental for local inference and end for
		// hosted providers; mnemos.example.yaml has the per-provider guidance.
		Strategy scalar `yaml:"strategy"`
		// Timeout bounds one session's capture pipeline, as a Go duration.
		// Default 4m.
		Timeout scalar `yaml:"timeout"`
	} `yaml:"capture"`

	// Floatback tunes the local upward flow that promotes important repo/workspace
	// learnings into the personal central brain (`mnemos float-back`). OnCapture is
	// an opt-in (default false): when true, a session-end capture inside a
	// repo/workspace also floats those learnings up, best-effort.
	Floatback struct {
		// OnCapture also floats repo learnings to the personal brain at
		// session end. Off by default.
		OnCapture scalar `yaml:"on_capture"`
	} `yaml:"floatback"`

	// Plasticity tunes the ADR-0015 neuromodulation control on `consolidate --plastic`.
	// Sensitivity scales how strongly recent surprise-volatility moves the global
	// learning-rate gain (default 1.0; 0 disables just neuromodulation, leaving
	// per-belief metaplasticity active).
	Plasticity struct {
		// Sensitivity scales surprise-driven learning-rate gain. Default 1.0;
		// 0 disables neuromodulation only.
		Sensitivity scalar `yaml:"sensitivity"`
	} `yaml:"plasticity"`

	// Pipeline tunes ingest-time behaviour. EpisodicEvents turns on additive
	// operational-event typing (ADR 0023 part 2) so timeline_query surfaces
	// deploys/releases/merges/incidents as typed episodes. Off by default: the
	// classifier is rule-based (~78% precision), safe for an additive tag but
	// not something to impose on every install.
	//
	// It was env-only and absent from this struct, so it could not be set from
	// a config file at all — while the docs promise every setting can be.
	Pipeline struct {
		// EpisodicEvents types operational events (deploys, releases, merges,
		// incidents) at ingest. Off by default.
		EpisodicEvents scalar `yaml:"episodic_events"`
	} `yaml:"pipeline"`

	// Metrics tunes the `serve` Prometheus product-metrics sampler (ADR 0020).
	// SampleInterval is a Go duration (default 60s; 0 disables the sampler).
	Metrics struct {
		// SampleInterval is the product-metrics sampler cadence, as a Go
		// duration. Default 60s; 0 disables it.
		SampleInterval scalar `yaml:"sample_interval"`
	} `yaml:"metrics"`

	// Log tunes the structured operational logs (ADR 0021). Level is one of
	// trace|debug|info|warn|error (default info).
	Log struct {
		// Level sets log verbosity: trace, debug, info, warn or error. Default
		// info.
		Level scalar `yaml:"level"`
	} `yaml:"log"`

	// Precedence selects the read-time federation policy (ADR 0011 Phase C):
	// tenant-wins (default), global-wins, or surface-dissonance. It decides which
	// tier wins — or whether the conflict is surfaced — when a federated read
	// turns up the same topic from both the global and the tenant/repo brain.
	Precedence scalar `yaml:"precedence"`
}

// EnvOverrides flattens the config to canonical MNEMOS_* keys. Empty leaves
// are omitted so they never shadow a downstream default with "".
func (c *Config) EnvOverrides() map[string]string {
	pairs := []struct {
		key string
		val scalar
	}{
		{"MNEMOS_DB_URL", c.DB.URL},
		{"MNEMOS_DB_MAX_CONNS", c.DB.MaxConns},
		{"MNEMOS_DB_MAX_IDLE_CONNS", c.DB.MaxIdleConns},
		{"MNEMOS_DB_CONN_MAX_LIFETIME", c.DB.ConnMaxLifetime},
		{"MNEMOS_PG_SHARED_POOL", c.DB.SharedPool},

		{"MNEMOS_LLM_PROVIDER", c.LLM.Provider},
		{"MNEMOS_LLM_API_KEY", c.LLM.APIKey},
		{"MNEMOS_LLM_MODEL", c.LLM.Model},
		{"MNEMOS_LLM_BASE_URL", c.LLM.BaseURL},
		{"MNEMOS_LLM_TIMEOUT", c.LLM.Timeout},
		{"MNEMOS_LLM_CACHE_MAX_BYTES", c.LLM.CacheMaxBytes},
		{"MNEMOS_EXTRACT_MODEL", c.LLM.ExtractModel},
		{"MNEMOS_EXTRACT_BATCH_CHARS", c.LLM.ExtractBatchChars},

		{"MNEMOS_EMBED_PROVIDER", c.Embed.Provider},
		{"MNEMOS_EMBED_API_KEY", c.Embed.APIKey},
		{"MNEMOS_EMBED_MODEL", c.Embed.Model},
		{"MNEMOS_EMBED_BASE_URL", c.Embed.BaseURL},
		{"MNEMOS_EMBED_TIMEOUT", c.Embed.Timeout},
		{"MNEMOS_EMBED_BATCH", c.Embed.Batch},

		{"MNEMOS_SERVE_PORT", c.Serve.Port},
		{"MNEMOS_TLS_CERT_FILE", c.Serve.TLSCertFile},
		{"MNEMOS_TLS_KEY_FILE", c.Serve.TLSKeyFile},
		{"MNEMOS_MTLS_CLIENT_CA_FILE", c.Serve.MTLSClientCAFile},
		{"MNEMOS_PUBLIC_READS", c.Serve.PublicReads},
		{"MNEMOS_METRICS_PUBLIC", c.Serve.MetricsPublic},
		{"MNEMOS_TRUST_PROXY", c.Serve.TrustProxy},
		{"MNEMOS_CONSOLIDATE_INTERVAL", c.Serve.ConsolidateInterval},

		{"MNEMOS_URL", c.Server.URL},
		{"MNEMOS_TOKEN", c.Server.Token},

		{"MNEMOS_JWT_SECRET", c.Auth.JWTSecret},
		{"MNEMOS_JWT_PREV_SECRET", c.Auth.JWTPrevSecret},
		{"MNEMOS_AUTH_DIR", c.Auth.Dir},
		{"MNEMOS_USER_ID", c.Auth.UserID},

		{"MNEMOS_REGISTRY_URL", c.Registry.URL},
		{"MNEMOS_REGISTRY_TOKEN", c.Registry.Token},

		{"MNEMOS_FEDERATION_ENABLED", c.Federation.Enabled},

		{"MNEMOS_TELEMETRY_OPTIN", c.Telemetry.OptIn},
		{"MNEMOS_TELEMETRY_ENDPOINT", c.Telemetry.Endpoint},

		{"MNEMOS_AXI_MAX_DURATION", c.Kernel.MaxDuration},
		{"MNEMOS_AXI_MAX_INVOCATIONS", c.Kernel.MaxInvocations},
		{"MNEMOS_AXI_MAX_TOKENS", c.Kernel.MaxTokens},
		{"MNEMOS_AXI_EVIDENCE_LOG", c.Kernel.EvidenceLog},

		{"MNEMOS_FEEDBACK_CONTEST_THRESHOLD", c.Feedback.ContestThreshold},
		{"MNEMOS_FEEDBACK_DECAY", c.Feedback.Decay},

		{"MNEMOS_JOB_TIMEOUT", c.Job.Timeout},

		{"MNEMOS_SPREADING_ACTIVATION", c.Query.SpreadingActivation},
		{"MNEMOS_SALIENCE", c.Query.Salience},
		{"MNEMOS_HEBBIAN", c.Query.Hebbian},
		{"MNEMOS_RECONSOLIDATE", c.Query.Reconsolidate},
		{"MNEMOS_INHIBIT", c.Query.Inhibit},

		{"MNEMOS_CAPTURE_STRATEGY", c.Capture.Strategy},
		{"MNEMOS_CAPTURE_TIMEOUT", c.Capture.Timeout},

		{"MNEMOS_FLOATBACK_ON_CAPTURE", c.Floatback.OnCapture},

		{"MNEMOS_PLASTICITY_SENSITIVITY", c.Plasticity.Sensitivity},
		{"MNEMOS_EPISODIC_EVENTS", c.Pipeline.EpisodicEvents},

		{"MNEMOS_METRICS_SAMPLE_INTERVAL", c.Metrics.SampleInterval},

		{"MNEMOS_LOG_LEVEL", c.Log.Level},

		{"MNEMOS_PRECEDENCE", c.Precedence},
	}
	out := make(map[string]string, len(pairs))
	for _, p := range pairs {
		if p.val != "" {
			out[p.key] = string(p.val)
		}
	}
	return out
}

// Load reads and parses a YAML config file. Unknown keys are rejected so a
// typo surfaces as an error rather than silently doing nothing.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // operator-supplied config path
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	return &cfg, nil
}

// Discover resolves the config-file path with this precedence:
//
//  1. explicit (the --config flag value), when non-empty
//  2. the MNEMOS_CONFIG environment variable
//  3. the nearest .mnemos/mnemos.yaml walking up from the working directory
//  4. the XDG default ~/.config/mnemos/config.yaml
//
// It returns the first path that exists, or ("", false) when none do. An
// explicit path (cases 1 and 2) is returned even if the file is missing so
// the caller can surface a clear "you asked for X but it isn't there" error.
func Discover(explicit string) (path string, found bool) {
	if explicit != "" {
		return explicit, true
	}
	if env := os.Getenv(EnvConfigPath); env != "" {
		return env, true
	}
	if p, ok := walkUpForConfig(); ok {
		return p, true
	}
	if p := xdgConfigPath(); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
	}
	return "", false
}

// walkUpForConfig looks for .mnemos/mnemos.yaml from the working directory
// upward, stopping at the filesystem root or the user's home directory —
// mirroring the DB-path resolution in cmd/mnemos so config and database are
// discovered against the same project boundary.
func walkUpForConfig() (string, bool) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", false
	}
	home, _ := os.UserHomeDir()
	dir := cwd
	for {
		candidate := filepath.Join(dir, ".mnemos", "mnemos.yaml")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, true
		}
		if home != "" && dir == home {
			return "", false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// xdgConfigPath returns ~/.config/mnemos/config.yaml, honoring XDG_CONFIG_HOME.
func xdgConfigPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "mnemos", "config.yaml")
}

// Hydrate applies the config's env overrides to the process environment
// without clobbering variables the operator already exported. Environment
// always wins; the file fills the gaps. It returns the keys it actually set.
func Hydrate(cfg *Config) ([]string, error) {
	var applied []string
	for key, val := range cfg.EnvOverrides() {
		if _, present := os.LookupEnv(key); present {
			continue // environment wins
		}
		if err := os.Setenv(key, val); err != nil {
			return applied, fmt.Errorf("set %s: %w", key, err)
		}
		applied = append(applied, key)
	}
	return applied, nil
}

// LoadAndHydrate discovers, loads, and applies a config file in one call.
// It returns the resolved path ("" when no file was found), the keys it set,
// and any error. A missing file is only an error when the path was explicit
// (via --config or MNEMOS_CONFIG); implicit discovery misses are not errors.
func LoadAndHydrate(explicit string) (path string, applied []string, err error) {
	resolved, found := Discover(explicit)
	if !found {
		return "", nil, nil
	}
	if _, statErr := os.Stat(resolved); statErr != nil {
		explicitlyRequested := explicit != "" || os.Getenv(EnvConfigPath) != ""
		if explicitlyRequested {
			return resolved, nil, fmt.Errorf("config file not found: %s", resolved)
		}
		return "", nil, nil
	}
	cfg, err := Load(resolved)
	if err != nil {
		return resolved, nil, err
	}
	applied, err = Hydrate(cfg)
	return resolved, applied, err
}
