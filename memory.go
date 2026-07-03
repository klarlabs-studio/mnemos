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
//	    _ "go.klarlabs.de/mnemos/internal/store/memory"
//	    _ "go.klarlabs.de/mnemos/sqlite"
//	    _ "go.klarlabs.de/mnemos/internal/store/postgres"
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

	// EventIDs links the claim back to source events. REQUIRED: claims
	// require evidence, so [Memory.RememberClaim] rejects a ClaimItem
	// whose EventIDs is empty (or contains only blank ids) before any
	// write runs. Each id must already exist in the event store (use
	// [Memory.RememberEvent] to add them first).
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

	// Lifecycle is the promotion state of the claim: a proposal the
	// system surfaced ([ClaimLifecycleCandidate]), a fact a human has
	// endorsed as durable organisational knowledge
	// ([ClaimLifecyclePromoted]), or one retired by a successor
	// ([ClaimLifecycleSuperseded]). Empty means the distinction does not
	// apply — an ordinary claim that was never routed through a
	// candidate→promoted review. Query with [AnswerOptions.Lifecycle] to
	// recall only promoted knowledge.
	Lifecycle ClaimLifecycle
}

// ClaimLifecycle is the human-promotion state of a claim. It is
// orthogonal to [Claim] validity (valid-time) and to the internal claim
// status: a claim can be currently valid yet still only a candidate
// awaiting review. Empty means the claim was never routed through a
// candidate→promoted review and the distinction does not apply.
type ClaimLifecycle string

const (
	// ClaimLifecycleCandidate is a claim the system proposed as worth
	// promoting to durable knowledge, pending human review.
	ClaimLifecycleCandidate ClaimLifecycle = "candidate"

	// ClaimLifecyclePromoted is a claim a human endorsed as durable
	// organisational knowledge.
	ClaimLifecyclePromoted ClaimLifecycle = "promoted"

	// ClaimLifecycleSuperseded is a promoted claim retired by a
	// successor; kept for history. It is not excluded from recall by
	// default — filter it out with [Query.Lifecycle] (or invalidate it
	// via ValidUntil) when a query should see only current knowledge.
	ClaimLifecycleSuperseded ClaimLifecycle = "superseded"
)

// Decision is an agent decision audit record: the belief -> plan -> outcome
// chain behind an action, recorded via [Memory.RecordDecision] and read back
// via [Memory.GetDecision] / [Memory.ListDecisions]. It is the durable "why
// did we do this / were we wrong?" trail.
type Decision struct {
	// ID is assigned by RecordDecision when left empty.
	ID string
	// Statement is the decision made (required).
	Statement string
	// Plan is the intended course of action.
	Plan string
	// Reasoning explains why this decision was chosen.
	Reasoning string
	// RiskLevel is one of "low", "medium", "high", "critical". Empty
	// defaults to "medium" at write time.
	RiskLevel string
	// Beliefs are the claim ids that were load-bearing inputs.
	Beliefs []string
	// Alternatives are the human-readable options considered but not chosen.
	Alternatives []string
	// OutcomeID links to an observed outcome once known (optional).
	OutcomeID string
	// ChosenAt is when the decision was made; defaults to now when zero.
	ChosenAt time.Time
	// CreatedBy / CreatedAt are stamped by the store (read-only on write).
	CreatedBy string
	CreatedAt time.Time
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
	// instant in time. This is the VALID-TIME axis: only claims that were
	// in force in the world at that instant are returned. Zero value
	// disables the filter (the common case).
	AsOf time.Time

	// RecordedAsOf, when set, returns the answer as the store KNEW it at
	// that instant. This is the TRANSACTION-TIME axis (the second axis of
	// the bitemporal model): claims recorded after RecordedAsOf are
	// excluded, so a query can ask "what did the agent believe at time T
	// about facts from period P" by pairing RecordedAsOf (when believed)
	// with AsOf (valid time of the facts). Zero value disables the filter
	// (the common case — "use the latest knowledge").
	RecordedAsOf time.Time

	// IncludeHistory makes the engine also return superseded claim
	// versions for the items that match. False by default.
	IncludeHistory bool

	// Lifecycle, when set, restricts the answer to claims in that
	// human-promotion state — most usefully [ClaimLifecyclePromoted] to
	// recall only durable, human-endorsed organisational knowledge. Empty
	// (the default) disables the filter: ordinary claims that were never
	// routed through a candidate→promoted review still appear.
	Lifecycle ClaimLifecycle
}

// Claim is the stable, read-only projection of a stored claim returned by
// [Memory.Get] and [Memory.Scan]. It mirrors the spec's Claim shape:
// Statement is the asserted fact, ValidFrom/ValidUntil are the valid-time
// bounds (when the fact was true in the world), and RecordedAt is the
// transaction time (when mnemos stored the claim). Unlike the write-side
// [ClaimItem], it carries the assigned ID and derived TrustScore.
type Claim struct {
	// ID is the stable identifier of the claim.
	ID string

	// Statement is the asserted fact in natural language.
	Statement string

	// Type classifies the claim ("fact", "hypothesis", "decision",
	// "test_result", or a custom string).
	Type string

	// Confidence is the [0, 1] confidence recorded with the claim.
	Confidence float64

	// TrustScore is the [0, 1] composite score (confidence ×
	// corroboration × freshness) computed by mnemos. Zero when the claim
	// has not been scored.
	TrustScore float64

	// ValidFrom is when the claim's content became true in the world.
	ValidFrom time.Time

	// ValidUntil is when the claim stopped being true (a successor
	// superseded it). Nil means "still valid".
	ValidUntil *time.Time

	// RecordedAt is the transaction time: when mnemos stored the claim.
	RecordedAt time.Time

	// Lifecycle is the human-promotion state of the claim
	// (candidate/promoted/superseded), or empty when the claim was never
	// routed through a candidate→promoted review. See [ClaimLifecycle].
	Lifecycle ClaimLifecycle
}

// ScanQuery selects claims by valid-time range for [Memory.Scan]. A claim
// is returned when its valid-time interval [ValidFrom, ValidUntil) overlaps
// the requested window. Both bounds are optional: a zero ValidFrom means
// "no lower bound", a zero ValidUntil means "no upper bound".
type ScanQuery struct {
	// ValidFrom bounds the window below (inclusive). Zero means no lower
	// bound.
	ValidFrom time.Time

	// ValidUntil bounds the window above (exclusive). Zero means no upper
	// bound.
	ValidUntil time.Time

	// Limit caps the number of claims returned. 0 means "no limit".
	Limit int
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

	// Features are optional numeric observations attached to this event, fed to
	// the bundled Chronos engine so it can detect patterns on real VALUES (a
	// latency, a duration, an error rate) rather than mere event presence. Empty
	// means "presence only" — mnemos forwards a single 1.0 so cadence is still
	// tracked. When set, these values ARE the temporal series Chronos analyses.
	Features []float64

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

// Signal is a temporal pattern the bundled Chronos engine detected over the
// events a run (scope) accumulated — the "what is changing / unusual?" reading
// that complements Recall's "what do we know?". Events forwarded to Chronos on
// [Memory.RememberEvent] feed the detectors; [Memory.Signals] surfaces the
// result. Zero signals is the common, healthy case (nothing notable changed).
type Signal struct {
	// Pattern names the kind of change detected: "trend", "spike", "drop",
	// "stall", "anomaly", "seasonality", "recurrence", and others Chronos
	// emits. Consumers should tolerate unknown values (Chronos may add kinds).
	Pattern string

	// RunID is the run whose event series the pattern was detected in — the
	// scope passed to [Memory.Signals] (empty = the default run).
	RunID string

	// Strength is the intensity of the pattern as observed, in [0, 1] (e.g. a
	// normalised slope for a trend, peak-to-baseline for a spike).
	Strength float64

	// Confidence is how sure the detector is the pattern is real given the
	// evidence, in [0, 1].
	Confidence float64

	// Class is a coarse qualitative grade a narrator uses to phrase the signal
	// ("possible", "clear", ...). Empty when the detector did not classify.
	Class string

	// DetectedAt is when Chronos inferred the pattern.
	DetectedAt time.Time

	// WindowStart / WindowEnd bound the period of observation the signal
	// references. Zero when the detector did not surface a window.
	WindowStart time.Time
	WindowEnd   time.Time
}

// SignalQuery selects which temporal signals [Memory.Signals] returns.
type SignalQuery struct {
	// RunID selects the run (Chronos scope) to read signals for. Empty means
	// the default run — the scope unscoped events (the common case) land in.
	RunID string

	// MinConfidence drops signals below the threshold; 0 keeps all.
	MinConfidence float64

	// Limit caps the number of signals returned. 0 means no cap.
	Limit int
}

// Memory is the public API of the Mnemos evidence layer. Implementations
// are returned by [New].
//
// # Concurrency
//
// A single Memory is safe for concurrent use. Multiple goroutines may
// call Remember, RememberClaim, RememberEvent, Recall, and Timeline at
// the same time; each write runs as its own governed kernel session with
// its own evidence chain, so concurrent writes never corrupt one
// another's audit trail. [Memory.LastWriteSession] reflects whichever
// write finished last — when several writes race, the one that completes
// last wins the slot, so callers that must correlate a specific write
// should read LastWriteSession immediately after that write returns
// rather than relying on it across concurrent calls. The cumulative
// token budget (see [WithTokenBudget]) is accounted atomically across
// concurrent writers.
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
	// Claims require evidence: a ClaimItem with no (non-blank) EventIDs
	// is rejected before any write runs, and nothing is persisted.
	//
	// Returns the generated claim ID so the caller can reference the
	// claim in future relationship edges or evidence links.
	RememberClaim(ctx context.Context, claim ClaimItem) (string, error)

	// RememberClaimWithEvidence is a convenience over RememberEvent +
	// RememberClaim for the common case where evidence is a set of plain
	// strings (source references — commit shas, urls, metric ids) rather
	// than pre-stored event ids. It records one "observation" event per
	// non-blank evidence string at validFrom, then stores a "fact" claim
	// linking them, and returns the claim id. The fail-closed rule still
	// holds: at least one non-blank evidence string is required, else it
	// returns an error and persists nothing. validFrom defaults to now
	// when zero.
	RememberClaimWithEvidence(ctx context.Context, text string, evidence []string, validFrom time.Time) (string, error)

	// SetClaimLifecycle transitions an existing claim's promotion state
	// ([ClaimLifecycleCandidate] / [ClaimLifecyclePromoted] /
	// [ClaimLifecycleSuperseded], or empty). It is the in-place counterpart
	// to setting [ClaimItem.Lifecycle] on write: the verb a human-gated
	// promotion flow uses to move a candidate to promoted, or to supersede a
	// promoted claim. Runs through the governed write path like every other
	// mutation. Returns an error when claimID does not exist or when the
	// lifecycle value is not recognised (empty is allowed). mnemos enforces
	// no transition ordering — it records the state the caller asserts.
	SetClaimLifecycle(ctx context.Context, claimID string, lifecycle ClaimLifecycle) error

	// RecordDecision persists an agent decision (belief -> plan -> outcome
	// audit record) through the governed write path and returns its id.
	// Statement is required; RiskLevel defaults to "medium" when empty.
	RecordDecision(ctx context.Context, d Decision) (string, error)

	// GetDecision returns the decision with the given id, or a not-found error.
	GetDecision(ctx context.Context, id string) (Decision, error)

	// ListDecisions returns recorded decisions (capped at limit; 0 = no cap) —
	// the read side of the decision audit trail.
	ListDecisions(ctx context.Context, limit int) ([]Decision, error)

	// Recall answers a query against the stored knowledge. The result
	// is ranked by trust score (or token overlap in passive mode when
	// no embeddings are configured) and may include claims reached by
	// graph expansion when [Query.Hops] > 0.
	Recall(ctx context.Context, q Query) ([]Result, error)

	// Get returns the stored claim with the given id. It is the stable
	// exact-lookup read on the API surface. Returns a non-nil error when
	// no claim with that id exists.
	Get(ctx context.Context, claimID string) (Claim, error)

	// Scan returns the claims whose valid-time interval overlaps the
	// window described by the [ScanQuery], ordered by ValidFrom. It is the
	// stable time-range read on the API surface, complementing the
	// similarity-ranked [Memory.Recall] and the exact [Memory.Get].
	Scan(ctx context.Context, q ScanQuery) ([]Claim, error)

	// RememberEvent stores a temporal event. The event is appended to
	// the Mnemos event log and forwarded to the bundled Chronos engine
	// for pattern detection. Events with the same ID are idempotent.
	RememberEvent(ctx context.Context, e Event) error

	// Timeline returns events matching the query in chronological order.
	// Source events live in Mnemos; signals derived by Chronos are
	// surfaced as additional events with [Event.Type] reflecting the
	// detected pattern.
	Timeline(ctx context.Context, q TimelineQuery) ([]Event, error)

	// Signals returns the temporal patterns the bundled Chronos engine detects
	// over a run's event series — trend / spike / stall / anomaly and the like
	// (see [Signal]). It is the perception counterpart to Recall: "what is
	// changing?" rather than "what do we know?". Returns no signals (nil, nil)
	// when Chronos is not wired or nothing notable was detected — callers treat
	// an empty result as "all quiet", never an error. Unlike the write methods
	// it does not go through the governance kernel: signals are derived, not
	// stored user assertions.
	Signals(ctx context.Context, q SignalQuery) ([]Signal, error)

	// LastWriteSession returns the governance session recorded by the
	// most recent write (Remember, RememberClaim, or RememberEvent) on
	// this Memory. Every write routes through an in-process axi-go
	// kernel; the returned [WriteSession] exposes the write's
	// tamper-evident evidence chain and a verifier.
	//
	// Returns nil before any write has been performed. The session
	// reflects the last write across all three write methods; callers
	// that need to correlate a specific write should read this
	// immediately after that call.
	LastWriteSession() WriteSession

	// Info reports this Memory's runtime configuration — which storage
	// backend and namespace it opened, the operating mode, the embedder,
	// and the actor id stamped on writes. Operators use it to confirm a
	// deployment is backed and isolated as intended (e.g. that recall is
	// semantic rather than lexical, or that the tenant namespace is set).
	Info() Info

	// Tenant returns a view of this Memory scoped to a single tenant WITHIN the
	// same namespace: reads only see, and writes only produce, that tenant's
	// rows. Isolation is default-deny, enforced at the database (Postgres
	// row-level security keyed on a per-connection tenant GUC), so a forgotten
	// filter cannot leak across tenants. The returned Memory owns its own storage
	// handle — Close it independently.
	//
	// Only the Postgres backend enforces tenant isolation today; on any other
	// backend Tenant returns an error rather than silently sharing data
	// (fail-closed). See ADR 0007.
	Tenant(id string) (Memory, error)

	// Close releases the underlying storage handle. Safe to call more
	// than once. Returns the first error encountered.
	Close() error
}

// Info describes a [Memory]'s runtime configuration, returned by
// [Memory.Info]. Every field is derived at construction and never
// changes for the life of the Memory.
type Info struct {
	// Backend is the storage scheme in use: "postgres", "sqlite",
	// "libsql", "mysql", or "memory".
	Backend string
	// Namespace is the tenant/namespace the store opened — a Postgres
	// schema, a SQLite file suffix, etc. "mnemos" when unset.
	Namespace string
	// Mode is the operating mode: "passive", "shared", or "enhanced".
	Mode string
	// Embedder names the embedding source: "none" (lexical recall only),
	// "shared" (a caller-supplied embedder), "enhanced" (a model built
	// from ProviderConfig), or "custom".
	Embedder string
	// Actor is the id stamped onto every governed write.
	Actor string
}

// Store is the spec's name for the stable, embeddable core API. It is a
// type alias for [Memory] — mnemos keeps the richer Memory surface
// (Remember / RememberClaim / RememberEvent / Recall / Get / Scan /
// Timeline / LastWriteSession) as its single source of truth, and Store
// exists so spec-facing code and the delivery adapters (mcp / http / cli)
// can refer to the core port by its spec name. The spec's mandated
// capabilities — Assert (Remember/RememberClaim), Recall, Get, Scan,
// LastWriteSession, evidence-required writes, and the bitemporal
// RecordedAsOf axis — are all present on this surface.
type Store = Memory
