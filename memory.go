// Package mnemos exposes a small, framework-neutral Go API for the
// Mnemos evidence layer. It is intended to be consumed by agent runtimes
// (Claude Code, Codex, Hermes, Nomi, OpenClaw, NanoClaw, and similar),
// programmatic systems, and tests.
//
// # Three modes
//
// Mnemos supports three modes, selected at construction time:
//
//   - Passive  — no language model required. Rule-based claim extraction
//     and token-overlap query ranking. Works with zero environment
//     configuration. Use [WithPassiveMode].
//   - Shared   — the agent runtime hands Mnemos its model provider so
//     enrichment shares the same key, model, and budget. Use
//     [WithSharedProvider].
//   - Enhanced — Mnemos uses its own dedicated provider configuration,
//     typically a different (smaller/cheaper) model for background
//     enrichment. Use [WithEnhancedMode].
//
// # Zero-config
//
// The default constructor does the right thing without any options:
//
//	mem, err := mnemos.New()
//	if err != nil { panic(err) }
//	defer mem.Close()
//
// This boots Mnemos in [WithPassiveMode] against the default storage
// (XDG-resolved SQLite at ~/.local/share/mnemos/mnemos.db) with an
// embedded Chronos for temporal queries.
//
// # Storage backends
//
// Mnemos talks to its storage layer through a URL-scheme registry. The
// providers a binary supports are determined by blank-imports in the
// consuming program:
//
//	import (
//	    _ "github.com/felixgeelhaar/mnemos/internal/store/memory"
//	    _ "github.com/felixgeelhaar/mnemos/internal/store/sqlite"
//	    _ "github.com/felixgeelhaar/mnemos/internal/store/postgres"
//	)
//
// Without at least one provider blank-imported, [New] cannot open the
// default storage and will return an error. For most callers, importing
// the sqlite provider is sufficient.
package mnemos

import (
	"context"
	"time"
)

// ClaimItem is a pre-built claim an agent runtime can hand to Mnemos
// directly, bypassing the extraction pipeline. Use this when the
// agent has already derived structured assertions (with its own model,
// from parsed structured data, or through any other path) and wants
// Mnemos to persist them as-is.
//
// This is the third of Mnemos's three input modes:
//
//  1. [Item] + [Memory.Remember] → rule-based extraction (passive mode)
//     or LLM-driven extraction (shared / enhanced mode).
//  2. Same Item path with WithSharedProvider / WithEnhancedMode →
//     extraction uses the configured language model.
//  3. ClaimItem + [Memory.RememberClaim] → no extraction at all;
//     Mnemos stores the supplied claim verbatim and (optionally) links
//     it to source events the caller has already persisted via
//     [Memory.RememberEvent].
type ClaimItem struct {
	// Text is the claim content. Required.
	Text string

	// Type classifies the claim. Mnemos recognises "fact",
	// "hypothesis", "decision", and "test_result" natively. Defaults
	// to "fact" when empty.
	Type string

	// Confidence is the agent's confidence in [0, 1]. Defaults to 1.0
	// when zero (the agent is asserting the claim with full
	// confidence). Mnemos will recompute the trust score from this
	// confidence × corroboration × freshness at query time.
	Confidence float64

	// EventIDs links the claim back to source events. Optional but
	// strongly recommended: claims without evidence cannot be ranked
	// by corroboration and skip the freshness factor. Each id must
	// already exist in the event store (use [Memory.RememberEvent] to
	// add them first).
	EventIDs []string

	// ValidFrom is when the claim's content first became true. Zero
	// value means "valid since before the system started tracking";
	// for fresh claims, set this to time.Now() or the time the agent
	// observed the fact.
	ValidFrom time.Time

	// ValidUntil is when the claim stopped being true (a successor
	// claim took its place). Zero value means "currently valid".
	ValidUntil time.Time

	// RunID groups related claims together for scoped recall.
	// Optional; matches the RunID used on the linked events when
	// present.
	RunID string
}

// Item is a single piece of knowledge to remember. Type and Content are
// the only required fields; everything else is optional.
type Item struct {
	// Type classifies the knowledge. Mnemos recognises "fact",
	// "hypothesis", and "decision" natively; other strings are stored
	// verbatim and can be filtered on later.
	Type string

	// Content is the human-readable text Mnemos stores and later
	// retrieves. The extraction pipeline may derive multiple claims
	// from a single Item when the content contains several assertions.
	Content string

	// Metadata is free-form key/value context attached to the source
	// event. Searchable; not used for ranking by default.
	Metadata map[string]string

	// Source is an optional human-readable label for where the
	// knowledge came from (a file path, a URL, a person's name). Stored
	// as a Mnemos Input so downstream queries can group by origin.
	Source string

	// RunID groups related items together for scoped recall. Optional;
	// when empty, the item is associated with the implicit default run.
	RunID string
}

// Query describes what to recall.
type Query struct {
	// Text is the natural-language question or keyword.
	Text string

	// RunID, when set, narrows the answer to items remembered under
	// that run. Empty means "across all runs".
	RunID string

	// Hops controls graph expansion. 0 (the default) returns directly
	// retrieved claims. 1 follows one supports/contradicts edge and
	// brings the connected claims in. Higher values expand further.
	Hops int

	// Limit caps the number of [Result]s returned. 0 means "use the
	// engine default" (typically 10).
	Limit int

	// AsOf, when set, returns the answer as it would have been at that
	// instant in time. Useful for incident timelines and audits. Zero
	// value disables the filter (the common case).
	AsOf time.Time

	// IncludeHistory makes the engine also return superseded claim
	// versions for the items that match. False by default.
	IncludeHistory bool
}

// Result is one item Mnemos returned for a [Query].
type Result struct {
	// ClaimID is the stable identifier of the underlying claim.
	ClaimID string

	// Text is the claim's content.
	Text string

	// Type is the claim's classification ("fact", "hypothesis",
	// "decision", or a custom string).
	Type string

	// Confidence is the [0, 1] confidence score the extractor assigned
	// when the claim was created.
	Confidence float64

	// TrustScore is the [0, 1] composite score combining confidence,
	// corroboration, and freshness. Computed by Mnemos at query time.
	TrustScore float64

	// HopDistance is how many supports/contradicts edges Mnemos walked
	// from the directly-retrieved set to reach this claim. 0 means
	// the claim was a direct hit; 1+ means it was reached via graph
	// expansion (see [Query.Hops]).
	HopDistance int

	// Provenance is a human-readable origin label: "local" for claims
	// sourced from this project's events, or a registry URL for claims
	// imported via federation. Empty when unknown.
	Provenance string
}

// Event is a temporal entry to remember. Unlike an [Item], an Event is
// always anchored to a wall-clock time and is intended for the timeline
// (incident timelines, audit trails, deployment history, decision logs).
//
// Events are stored both as Mnemos events (immutable knowledge) and fed
// to the bundled Chronos engine for temporal pattern detection.
type Event struct {
	// ID, when non-empty, sets the stable identifier; otherwise Mnemos
	// generates a UUID.
	ID string

	// At is the wall-clock time the event occurred. Required.
	At time.Time

	// Type classifies the event. Examples: "deployment", "incident",
	// "decision", "release", "alert". Used by [TimelineQuery.Types]
	// for filtering.
	Type string

	// Content is the human-readable description.
	Content string

	// Metadata is free-form key/value context.
	Metadata map[string]string

	// RunID groups related events. Optional; empty means default run.
	RunID string
}

// TimelineQuery selects a slice of [Event]s from the timeline.
type TimelineQuery struct {
	// From bounds the range below. Zero means "no lower bound".
	From time.Time

	// To bounds the range above. Zero means "no upper bound".
	To time.Time

	// Types, when non-empty, restricts the result to events whose
	// [Event.Type] is in the list.
	Types []string

	// RunID, when set, narrows the timeline to a single run.
	RunID string

	// Limit caps the result size. 0 means "use the engine default".
	Limit int
}

// Memory is the public API of the Mnemos evidence layer. Implementations
// are returned by [New]; all methods are safe for concurrent use.
type Memory interface {
	// Remember stores an item of knowledge. Extraction runs the item
	// through the configured pipeline (rule-based in passive mode,
	// LLM-augmented when a [TextGenerator] is configured) and persists
	// any derived claims with full evidence links back to the source
	// event.
	Remember(ctx context.Context, item Item) error

	// RememberClaim stores a pre-built claim directly, without running
	// extraction. Use this when an agent runtime has already derived a
	// structured assertion (with its own model, parsed structured data,
	// etc.) and wants Mnemos to persist it verbatim. Linked source
	// events (referenced via [ClaimItem.EventIDs]) must already exist
	// in the event store — add them with [Memory.RememberEvent] first
	// when they're new.
	//
	// Returns the generated claim ID so the caller can reference the
	// claim in future relationship edges or evidence links.
	RememberClaim(ctx context.Context, claim ClaimItem) (string, error)

	// Recall answers a query against the stored knowledge. The result
	// is ranked by trust score (or token overlap in passive mode when
	// no embeddings are configured) and may include claims reached by
	// graph expansion when [Query.Hops] > 0.
	Recall(ctx context.Context, q Query) ([]Result, error)

	// RememberEvent stores a temporal event. The event is appended to
	// the Mnemos event log and forwarded to the bundled Chronos engine
	// for pattern detection. Events with the same ID are idempotent.
	RememberEvent(ctx context.Context, e Event) error

	// Timeline returns events matching the query in chronological order.
	// Source events live in Mnemos; signals derived by Chronos are
	// surfaced as additional events with [Event.Type] reflecting the
	// detected pattern.
	Timeline(ctx context.Context, q TimelineQuery) ([]Event, error)

	// Close releases the underlying storage handle. Safe to call more
	// than once. Returns the first error encountered.
	Close() error
}
