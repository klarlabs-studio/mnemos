# Mnemos as a Go library

> Available since v0.17.0; the agent-supplied-claim path (`RememberClaim`)
> + bundled-Chronos forwarding ship in v0.17.1. Marked `v0.x` until the
> public API has been exercised by at least two external agent runtimes.

The root `mnemos` package is a small, framework-neutral Go API for
embedding Mnemos in any agent runtime (Claude Code, Codex, Hermes,
Nomi, OpenClaw, NanoClaw, and similar) or programmatic system. It is
additive: every CLI / MCP / HTTP surface continues to work unchanged
and now routes through the same library internally.

## Install

```bash
go get github.com/felixgeelhaar/mnemos@latest
```

Blank-import the storage providers you need (the in-memory provider is
useful for tests; SQLite is the right default for most callers):

```go
import (
    _ "github.com/felixgeelhaar/mnemos/internal/store/memory"
    _ "github.com/felixgeelhaar/mnemos/internal/store/sqlite"
    // Postgres / MySQL / libSQL providers also available; import only
    // what your binary needs.
)
```

## Zero-config

```go
package main

import (
    "context"

    "github.com/felixgeelhaar/mnemos"
    _ "github.com/felixgeelhaar/mnemos/internal/store/sqlite"
)

func main() {
    mem, err := mnemos.New() // passive mode, XDG storage, bundled Chronos
    if err != nil { panic(err) }
    defer mem.Close()

    ctx := context.Background()
    _ = mem.Remember(ctx, mnemos.Item{
        Type:    "fact",
        Content: "The user prefers Go for backend work.",
    })

    results, _ := mem.Recall(ctx, mnemos.Query{Text: "user Go work"})
    for _, r := range results {
        // r.Text, r.Type, r.Confidence, r.TrustScore, ...
    }
}
```

No environment variables required. The storage DSN is resolved from
`MNEMOS_DB_URL` > a project-local `.mnemos/mnemos.db` (walked up from
the CWD like `.git`) > the XDG default
`~/.local/share/mnemos/mnemos.db`. The first call materialises the
schema; subsequent calls reuse it.

## The three modes

### Passive — no LLM

Default. Rule-based claim extraction; token-overlap query ranking when
no embeddings are present. Zero per-call cost. Suitable for tests,
smoke demos, and any setup without a language-model provider
configured.

```go
mem, _ := mnemos.New(mnemos.WithPassiveMode())
```

**Limitations:** the rule-based extractor is conservative; expect
fewer claims per document than the LLM path. Recall ranking is
token-overlap only — queries that share no tokens with the stored
content return nothing. Documented openly so adopters can decide.

### Shared — agent runtime supplies the provider

```go
import "github.com/felixgeelhaar/mnemos/providers"

type myTextGen struct{ client *anthropic.Client }

func (g *myTextGen) GenerateText(
    ctx context.Context, in providers.GenerateTextInput,
) (providers.GenerateTextOutput, error) {
    // wrap your existing provider call
}

mem, _ := mnemos.New(
    mnemos.WithSharedProvider(&myTextGen{client: agentLLM}, nil),
)
```

The `Embedder` argument is optional; pass `nil` if the consumer's
provider doesn't expose an embedding API (e.g. Anthropic). Mnemos
falls back to token-overlap ranking.

This is the recommended mode for agent runtimes that already have a
model client and want Mnemos to share the same key, model, and budget.

### Enhanced — Mnemos uses its own provider

```go
mem, _ := mnemos.New(
    mnemos.WithEnhancedMode(mnemos.ProviderConfig{
        LLMProvider: "ollama",
        LLMModel:    "llama3.2",
        LLMBaseURL:  "http://localhost:11434",
        // Embedding-side defaults to the LLM-side fields if unset.
    }),
)
```

Useful when you want background enrichment on a separate (cheaper /
specialised) model, or to separate billing.

## Three input modes (passive / LLM-driven / agent-supplied)

Mnemos has three orthogonal paths for getting knowledge into the store.
They cover different consumer needs and can be mixed freely.

### Mode 1 — passive text ingestion (no LLM)

```go
mem.Remember(ctx, mnemos.Item{
    Type:    "fact",
    Content: "The deployment succeeded in production.",
})
```

Mnemos runs its built-in rule-based extractor; one or more `Claim`s are
derived from the text and linked to a synthetic source event. No model
call, no per-call cost. Best for tests, smoke demos, or environments
where you don't want any external dependency.

### Mode 2 — LLM-driven text ingestion (shared or enhanced)

Same `Remember` call as Mode 1; the difference is the construction
option:

```go
mem, _ := mnemos.New(mnemos.WithSharedProvider(myTextGen, myEmbedder))
mem.Remember(ctx, mnemos.Item{
    Type:    "fact",
    Content: "Long document with many implicit assertions ...",
})
```

The configured `TextGenerator` runs structured extraction; claims are
typed (fact / hypothesis / decision) with calibrated confidence. Best
for ingesting prose, meeting notes, design docs.

### Mode 3 — agent-supplied claims (no extraction)

When the agent runtime has *already* derived structured assertions
(with its own model, from parsed structured data, from a function-call
result), it can hand them to Mnemos directly:

```go
claimID, _ := mem.RememberClaim(ctx, mnemos.ClaimItem{
    Text:       "User prefers Go for backend work.",
    Type:       "fact",
    Confidence: 0.95,
    EventIDs:   []string{"evt-source-1"}, // link to events you persisted
    RunID:      "session-A",
})
```

No extraction runs; Mnemos persists the claim verbatim. Recommended
pattern: call `RememberEvent` first to anchor the claim, then
`RememberClaim` with the event id in `EventIDs`. Claims without
evidence skip the corroboration + freshness ranking factors.

## Temporal memory: events + Chronos

Mnemos bundles [Chronos](https://github.com/felixgeelhaar/chronos) as an
in-process engine (added in Chronos v0.6.0). `mnemos.New()` boots a
default in-memory Chronos; supply your own via `WithChronos(eng)` when
you want durable storage or a custom detector configuration.

```go
import "github.com/felixgeelhaar/chronos/embed"

eng, _ := embed.New(embed.WithStorage("sqlite:///app-chronos.db"))
defer eng.Close()

mem, _ := mnemos.New(mnemos.WithChronos(eng))
// Now mem and eng share Chronos state; mem won't close eng on its own.

_ = mem.RememberEvent(context.Background(), mnemos.Event{
    At:      time.Now(),
    Type:    "deployment",
    Content: "shipped v2.3.0",
    RunID:   "release-cycle",
})

events, _ := mem.Timeline(context.Background(), mnemos.TimelineQuery{
    RunID: "release-cycle",
})
```

Source events live in Mnemos's event store; pattern detection signals
live in Chronos. The library's `Timeline` method currently returns
the source events sorted by timestamp. A future release will surface
Chronos signals alongside source events; for now, power users wanting
detection results call into the `*embed.Engine` they supplied via
`WithChronos`.

## Provider interfaces

The `mnemos/providers` subpackage exposes two framework-neutral
interfaces consumers implement:

```go
type TextGenerator interface {
    GenerateText(ctx context.Context, in GenerateTextInput) (GenerateTextOutput, error)
}

type Embedder interface {
    Embed(ctx context.Context, in EmbedInput) (EmbedOutput, error)
}
```

These are intentionally small — they carry only the fields Mnemos
needs. No provider-specific configuration leaks out; you wrap your
provider client in a small adapter.

## Storage

Mnemos talks to its storage layer through a URL-scheme registry. The
providers blank-imported by your binary determine which schemes
resolve. Common imports:

```go
import (
    _ "github.com/felixgeelhaar/mnemos/internal/store/memory"   // memory://
    _ "github.com/felixgeelhaar/mnemos/internal/store/sqlite"   // sqlite://
    _ "github.com/felixgeelhaar/mnemos/internal/store/postgres" // postgres:// (also CockroachDB, YugabyteDB, Neon, ...)
    _ "github.com/felixgeelhaar/mnemos/internal/store/mysql"    // mysql:// (also PlanetScale, TiDB, MariaDB, Vitess)
    _ "github.com/felixgeelhaar/mnemos/internal/store/libsql"   // libsql:// (Turso remote or local file)
)
```

Override the DSN via `WithStorage("postgres://...?namespace=mnemos")`.

## API stability

The library API is marked `v0.x` until at least two external agent
runtimes have shipped against it. During this window:

- Method signatures on `Memory` are stable; new methods may be added.
- New `With*` options may be added; existing ones won't be removed.
- The `providers` interface shapes are stable; new fields may be added
  to inputs/outputs in a backward-compatible way.
- Internal types under `internal/` are NOT part of the contract and may
  change at any time.

## See also

- [`mnemos/example_test.go`](../example_test.go) — godoc examples for each mode.
- [`mnemos/memory_test.go`](../memory_test.go) — end-to-end tests.
- [ADR 0003-0006](adr/) — context for the cognitive-stack simplification
  that produced this library.
