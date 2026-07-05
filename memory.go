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
	"errors"
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

	// CreatedBy attributes the claim to a specific author (agent/worker id),
	// overriding the store-wide actor ([WithActor]) for this write. Empty falls
	// back to the store actor. Per-author attribution is what powers independence-
	// aware corroboration, per-source calibration, and the who-knows-what directory
	// in a multi-worker store — set it to the worker that produced the claim.
	CreatedBy string
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

// RecallSufficiencyFloor is the aggregate-confidence threshold at or above
// which a recall is considered strong enough to answer on: below it (or with
// no claims) the memory is too thin and the caller should gather more evidence
// or abstain rather than confabulate. It mirrors the floor Mnemos uses
// internally to gate its own corrective-retrieval pass, so the "feeling of
// knowing" a caller reads is the same signal Mnemos acts on. The aggregate
// confidence tops out around 0.7, so a genuinely grounded answer clears this
// comfortably.
const RecallSufficiencyFloor = 0.35

// Sufficiency is Mnemos's "feeling of knowing" for a recall — a deterministic
// (no-LLM) read on whether the memory backing an answer is strong enough to
// rely on. It lets an agent runtime abstain, hedge, or go seek first-hand
// evidence when its memory is thin, instead of stating a weakly-supported
// conclusion as established fact.
type Sufficiency struct {
	// Confidence is the aggregate answer confidence in [0, 1] — a weighted
	// blend of the recalled claims' trust, citation density, contradiction
	// penalty, and freshness. Distinct from any single claim's Confidence.
	Confidence float64

	// ClaimCount is the number of claims the recall returned.
	ClaimCount int

	// Sufficient reports whether the recall clears the bar: Confidence at or
	// above [RecallSufficiencyFloor] with at least one claim. When false, the
	// caller is holding thin memory and should seek evidence or abstain.
	Sufficient bool

	// Floor is the threshold applied ([RecallSufficiencyFloor]), surfaced so
	// callers can render the margin without hard-coding the constant.
	Floor float64
}

// Effort-budgeting constants govern [Memory.RecallWithEffort]. The escalation is
// deliberately bounded: at most one extra pass, at most a few more hops and a
// bounded breadth increase, so a high-stakes query works harder but can never run
// away.
const (
	// EffortEscalationThreshold is the budget at or above which RecallWithEffort
	// spends a second, wider+deeper pass. Below it the first pass stands.
	EffortEscalationThreshold = 0.3

	// EffortMaxExtraHops is the most additional graph-expansion hops the escalated
	// pass may add on top of the caller's requested hops.
	EffortMaxExtraHops = 2

	// EffortMaxExtraBreadth is the most additional claims the escalated pass may
	// request on top of the first pass's breadth.
	EffortMaxExtraBreadth = 10
)

// Spreading-activation constants govern [Memory.RecallWithContext].
const (
	// ActivationHops is how far activation spreads from the context over the
	// epistemic graph (supports/contradicts edges) — 1–2 in the literature.
	ActivationHops = 2

	// ActivationDecay is the per-hop decay of activation: a claim one hop from the
	// context contributes ActivationDecay, two hops ActivationDecay², and so on.
	ActivationDecay = 0.5

	// ActivationBoost is the most positions a fully-activated (hop-0) query result
	// is promoted. The re-rank preserves the base order and only lifts results that
	// connect to the current train of thought — a gentle bias, not a reshuffle.
	ActivationBoost = 3.0
)

// EffortReport records what one [Memory.RecallWithEffort] call spent, so callers
// (and tests) can see the budget decision without inferring it.
type EffortReport struct {
	// Budget is the Expected-Value-of-Control budget computed from the first
	// pass: stakes × (1 − first-pass confidence). See [EffortBudget].
	Budget float64

	// Passes is the number of retrieval passes actually run: 1 when the budget
	// did not warrant escalation, 2 when it did.
	Passes int

	// Hops and Breadth are the graph depth and claim breadth of the ADOPTED
	// result — the escalated pass's when it was kept, else the first pass's.
	Hops    int
	Breadth int
}

// EffortBudget is the Expected-Value-of-Control budget in [0, 1] for grounding an
// answer harder: how much extra retrieval effort is warranted given the stakes and
// the confidence already achieved. It is simply stakes × (1 − confidence) —
// high stakes AND low confidence warrant investment; low stakes OR high confidence
// do not. Both inputs are clamped to [0, 1]. Pure and deterministic.
func EffortBudget(stakes, confidence float64) float64 {
	s := clampUnit(stakes)
	c := clampUnit(confidence)
	return s * (1 - c)
}

func clampUnit(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

// ErrExpectationsUnsupported is returned by the expectation methods on a backend
// with no expectation store.
var ErrExpectationsUnsupported = errors.New("mnemos: structured expectations not supported on this backend")

// Reconciliation is the result of closing one structured expectation against its
// observed value — the prediction-error event.
type Reconciliation struct {
	// ClaimID is the claim whose expectation was reconciled.
	ClaimID string
	// Predicted / Observed are the expected and arrived values.
	Predicted float64
	Observed  float64
	// Surprise is the precision-weighted prediction error: |predicted − observed|
	// divided by the tolerance (or the scale of the prediction when tolerance is 0).
	// 0 is a perfect prediction; ≥ 1 means the miss exceeded the tolerance.
	Surprise float64
	// Confirmed is true when the observation fell within tolerance (a validates
	// verdict); false means a refutation.
	Confirmed bool
}

// WorkingMemoryBlockLimit is the default character cap on a single working-memory
// block. The cap IS the attention budget: [Memory.SetBlock] rejects an over-limit
// value and [Memory.AppendBlock] evicts the oldest lines to stay within it.
const WorkingMemoryBlockLimit = 2000

// ErrBlocksUnsupported is returned by the working-memory block methods on a
// backend that has no block store (the repository is nil).
var ErrBlocksUnsupported = errors.New("mnemos: working-memory blocks not supported on this backend")

// ErrBlockTooLarge is returned by [Memory.SetBlock] when the value exceeds
// [WorkingMemoryBlockLimit]. AppendBlock never returns it — it evicts instead.
var ErrBlockTooLarge = errors.New("mnemos: working-memory block exceeds size limit")

// Conflict is one edge of the "contested frontier": a recalled claim that a
// currently-valid claim contradicts. Surfaced by [Memory.RecallWithConflicts] so
// a recall carries its own live counter-evidence — retrieval-as-reasoning that
// never hides a dispute it knows about.
type Conflict struct {
	// ClaimID / ClaimText identify the recalled claim that is contested.
	ClaimID   string
	ClaimText string
	// ContradictingID / ContradictingText / ContradictingTrust identify the
	// currently-valid claim that contradicts it, and how trusted that challenger is.
	ContradictingID    string
	ContradictingText  string
	ContradictingTrust float64
}

// Block is one of an agent's working-memory blocks — a bounded, labeled, mutable
// piece of "core memory" the agent keeps always-loaded (persona, open threads,
// current focus), distinct from the queried claim archive.
type Block struct {
	// Owner is the agent the block belongs to.
	Owner string
	// Label is the block's name (e.g. "persona", "open_threads", "working_context").
	Label string
	// Value is the block's text, bounded by [WorkingMemoryBlockLimit].
	Value string
	// UpdatedAt is the last-write timestamp.
	UpdatedAt time.Time
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

// ConsolidateOptions tunes a [Memory.Consolidate] pass.
type ConsolidateOptions struct {
	// DedupeThreshold is the cosine-similarity floor for treating two claims as
	// near-duplicates and collapsing them into one canonical claim (gist
	// extraction) — evidence from the duplicates is repointed to the survivor,
	// so nothing is lost. In (0, 1]; a sensible default (~0.92) is used when <= 0.
	DedupeThreshold float64

	// ForgetBelowTrust actively forgets claims whose recomputed trust score is
	// strictly below this floor — the brain's offline pruning: a memory that has
	// decayed (low confidence × corroboration × freshness) and is no longer
	// reinforced stops surfacing. Forgetting is reduced retrievability, NOT
	// erasure: the claim is marked deprecated (excluded from recall) with its
	// history preserved. PROMOTED (human-endorsed) claims are NEVER forgotten,
	// regardless of trust. 0 (the default) disables forgetting — dedupe only.
	ForgetBelowTrust float64

	// ForgetRefuted actively forgets claims that an observed outcome later REFUTED
	// (a refutes edge, minted when a decision's failing outcome is attached) — the
	// prediction-error loop closing on itself: a belief reality contradicted should
	// stop surfacing, not linger at whatever trust it had. Like ForgetBelowTrust it
	// invalidates (valid-time closed, history kept), never erases; PROMOTED claims
	// are exempt (a refuted human belief surfaces as a Hypercorrection for review
	// instead). Off by default. Independent of ForgetBelowTrust — a claim can be
	// refuted while still nominally trusted.
	ForgetRefuted bool

	// Synthesize, when true, re-derives the skill layer (lessons from
	// actions-with-outcomes, then playbooks from lessons) during the sleep pass —
	// the auto-trigger arrow of the skill loop, so skills stay current without a
	// manual CLI run. Idempotent; a no-op without the action/lesson/playbook layer.
	// Runs before ReinforcePlaybooks so freshly-derived playbooks tune in the same
	// pass.
	Synthesize bool

	// ReinforceValidated, when true, freshens claims whose latest outcome verdict
	// CONFIRMED them — routing the confirmation half of prediction-error into the
	// freshness knob so borne-out beliefs resist forgetting (the flashbulb effect).
	// The mirror of ForgetRefuted; together they route the surprise signal both ways.
	ReinforceValidated bool

	// ReplayTopK, when > 0, rehearses the K most important currently-valid claims
	// during the sleep pass — prioritized experience replay. Claims are ranked by
	// salience × recency (the surprise term folds in once the prediction loop
	// lands) and the top K have their freshness bumped (a re-verification), so the
	// memories that matter most resist the decay that prunes the mundane tail. The
	// SWS rehearsal stage, fed by a scheduler instead of a uniform scan. 0 disables.
	ReplayTopK int

	// ReinforcePlaybooks bends each playbook's confidence toward the observed
	// success rate of the outcomes recorded against the actions its lessons were
	// derived from — the skill-learning half of the sleep pass. Synthesize writes
	// a playbook's confidence from its lessons once; this closes the loop so real
	// outcomes reinforce or decay it over time (a playbook whose underlying
	// actions keep failing decays; one whose actions keep succeeding is
	// reinforced), turning the skill store from write-only into self-tuning. A
	// deterministic, no-LLM blend (see [ReinforcementRetention]). Off by default;
	// a no-op on backends without the skill layer or with too few outcomes.
	ReinforcePlaybooks bool

	// DryRun plans the pass without mutating — it reports what WOULD be merged
	// and forgotten so an operator (or a scheduler) can preview before applying.
	DryRun bool
}

// ConsolidateResult reports what one [Memory.Consolidate] pass did.
type ConsolidateResult struct {
	// ClaimsScanned is the number of embedded claims considered.
	ClaimsScanned int
	// DuplicateGroups is the number of near-duplicate groups found.
	DuplicateGroups int
	// Merged is the number of duplicate claims collapsed into their canonical
	// (the gist) — 0 on a dry run.
	Merged int
	// TrustRefreshed is the number of claims whose trust score was recomputed
	// after the merge (evidence counts changed, so freshness/corroboration shift).
	TrustRefreshed int
	// Forgotten is the number of stale, low-trust, non-promoted claims marked
	// deprecated (excluded from recall, history kept) — 0 unless ForgetBelowTrust
	// was set, and 0 on a dry run.
	Forgotten int
	// Refuted is the number of non-promoted claims invalidated because an observed
	// outcome refuted them — 0 unless ForgetRefuted was set, and 0 on a dry run.
	Refuted int
	// Validated is the number of claims freshened because their latest outcome
	// verdict confirmed them — 0 unless ReinforceValidated was set, and 0 on a dry run.
	Validated int
	// PlaybooksReinforced is the number of playbooks whose confidence was moved by
	// observed outcomes — 0 unless ReinforcePlaybooks was set, and 0 on a dry run.
	PlaybooksReinforced int
	// Replayed is the number of high-priority claims rehearsed (freshness bumped) —
	// 0 unless ReplayTopK was set, and 0 on a dry run.
	Replayed int
	// LessonsSynthesized / PlaybooksSynthesized count the skills (re-)derived — 0
	// unless Synthesize was set, and 0 on a dry run.
	LessonsSynthesized   int
	PlaybooksSynthesized int
}

// SynthesizeResult reports what one [Memory.Synthesize] pass derived.
type SynthesizeResult struct {
	// LessonsDerived is the number of lessons emitted or refreshed from
	// actions-with-outcomes.
	LessonsDerived int
	// PlaybooksDerived is the number of playbooks emitted or refreshed from lessons.
	PlaybooksDerived int
}

// Hypercorrection is one metacognitive alert: a currently-valid contradiction of
// a well-established claim. Named for the hypercorrection effect — high-confidence
// errors, once caught, are corrected most strongly — it flags exactly those
// high-stakes moments where new evidence challenges an entrenched belief.
type Hypercorrection struct {
	// ContradictedClaimID / ContradictedText identify the ESTABLISHED claim being
	// challenged (the higher-trust or promoted side of the contradiction).
	ContradictedClaimID string
	ContradictedText    string
	// ContradictedTrust is that claim's trust score at detection time, and
	// ContradictedPromoted reports whether it is human-endorsed knowledge. Together
	// they rank the alert: promoted + high-trust conflicts surface first.
	ContradictedTrust    float64
	ContradictedPromoted bool
	// ChallengingClaimID / ChallengingText identify the newer claim that
	// contradicts it — the evidence prompting a re-examination.
	ChallengingClaimID string
	ChallengingText    string
	// DetectedAt is when the contradicts edge was recorded.
	DetectedAt time.Time
}

// Calibration reports how well stated confidence matches reality, computed only
// from claims that an observed outcome later validated or refuted.
type Calibration struct {
	// Samples is the number of outcome-adjudicated claims the curve is built from.
	// Small early on — the calibration is only as trustworthy as this count.
	Samples int
	// Buckets partitions those claims by stated confidence (deciles), low→high,
	// omitting empty ranges.
	Buckets []CalibrationBucket
	// ECE is the Expected Calibration Error: the sample-weighted mean gap between
	// a bucket's accuracy and its mean stated confidence. 0 = perfectly
	// calibrated; larger means confidence is a less reliable signal.
	ECE float64
	// Brier is the mean squared error of confidence against the {0,1} outcome
	// (0 best, 1 worst) — a single-number scoring of the confidence estimates.
	Brier float64
	// Sources breaks calibration down by the claim author (CreatedBy),
	// most-adjudicated first. It answers "which source is over- or under-confident?"
	// — so a chronically over-confident source can be discounted without a human
	// touching config. Empty when no claims carry a distinct author.
	Sources []SourceCalibration
}

// SourceCalibration is one author's track record: how their stated confidence has
// matched reality, over the claims of theirs an outcome later adjudicated.
type SourceCalibration struct {
	// Source is the claim author (CreatedBy) — e.g. a worker id.
	Source string
	// Samples is how many of this source's claims have an outcome verdict.
	Samples int
	// MeanConfidence / Accuracy are what this source predicted vs what happened.
	MeanConfidence float64
	Accuracy       float64
	// Gap is MeanConfidence − Accuracy: positive = over-confident (says 0.9, right
	// less often), negative = under-confident. The single number to route on.
	Gap float64
	// Brier is this source's mean squared error (0 best, 1 worst).
	Brier float64
}

// CalibrationBucket is one stated-confidence band and how claims in it fared.
type CalibrationBucket struct {
	// Lower/Upper is the confidence range [Lower, Upper) the bucket covers.
	Lower float64
	Upper float64
	// Count is the number of adjudicated claims whose stated confidence fell here.
	Count int
	// MeanConfidence is their mean stated confidence (what was predicted).
	MeanConfidence float64
	// Accuracy is the fraction that were actually borne out (what happened).
	Accuracy float64
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

	// RecallWithSufficiency is Recall plus a "feeling of knowing": a
	// deterministic verdict on whether the memory Mnemos holds is strong
	// enough to answer confidently, or whether the caller should seek more
	// evidence (or abstain) rather than confabulate. The [Sufficiency]
	// carries the aggregate answer confidence, the claim count, and the
	// Sufficient flag (confidence at or above [RecallSufficiencyFloor] with
	// at least one claim). The []Result is identical to what Recall returns;
	// no extra work beyond surfacing the confidence Recall already computes.
	RecallWithSufficiency(ctx context.Context, q Query) ([]Result, Sufficiency, error)

	// RecallWithEffort recalls under an Expected-Value-of-Control budget: it
	// runs a first pass, then invests MORE retrieval breadth and depth only
	// when the caller's stakes and the uncertainty of that first pass justify
	// it (budget = stakes × (1 − confidence), see [EffortBudget]). A low-stakes
	// query stays cheap even when memory is thin; a high-stakes one widens and
	// deepens until it is grounded or the bounded ceiling is hit. stakes is a
	// [0, 1] signal the caller supplies from how much it matters that this
	// answer is right (e.g. mapped from the pending action's blast radius). The
	// escalation is a single bounded pass and non-regressive — it never returns
	// a weaker answer than the first. The [EffortReport] says what was spent.
	RecallWithEffort(ctx context.Context, q Query, stakes float64) ([]Result, Sufficiency, EffortReport, error)

	// RecallWithContext ranks a query's results with spreading activation from the
	// caller's current context (typically its working memory). The context is
	// expanded up to [ActivationHops] over the epistemic (supports/contradicts)
	// graph, and each query result connected to that context is promoted by a
	// decaying activation term — biasing retrieval toward the current train of
	// thought (coherence). An empty context, or a result set too small to reorder,
	// is exactly Recall. Deterministic and best-effort: if the spread fails, the
	// plain recall order is returned.
	RecallWithContext(ctx context.Context, q Query, context string) ([]Result, error)

	// Recombinations is the REM-like recombination detector: it finds pairs of
	// high-salience, currently-valid claims that are topically related yet NOT
	// directly connected in the epistemic graph — novel juxtapositions the waking
	// graph never made — ranked by similarity. Deterministic detection; naming the
	// emergent schema/hypothesis is left to a human or an LLM (proposals, never
	// auto-promoted).
	Recombinations(ctx context.Context, limit int) ([]Recombination, error)

	// ClassifyClaim routes a candidate claim by fit to established knowledge: if the
	// store already holds sufficient knowledge on the topic it is assimilation
	// (cheap to learn, against an existing schema), otherwise novel accommodation.
	// The assimilation-vs-accommodation routing signal. Deterministic.
	ClassifyClaim(ctx context.Context, text string) (Assimilation, error)

	// Expect attaches a structured forward prediction to a claim: the value is
	// expected to be `predicted` within `tolerance` by `horizon`. One expectation
	// per claim (re-calling replaces it). Returns [ErrExpectationsUnsupported] on a
	// backend without the expectation store.
	Expect(ctx context.Context, claimID string, predicted, tolerance float64, horizon time.Time) error

	// RecordObservation reports the value that actually arrived for a claim's
	// expectation, so the next reconciliation can close it. Errors if the claim has
	// no expectation.
	RecordObservation(ctx context.Context, claimID string, observed float64) error

	// ReconcileExpectations closes every open expectation that has an observation:
	// it computes a surprise scalar (precision-weighted |predicted − observed|),
	// emits a validates (within tolerance) or refutes (beyond) verdict edge — so the
	// existing surprise routing (reinforce / forget) picks it up — marks the
	// expectation resolved, and returns the reconciliations. This is the mechanism
	// that mints prediction-error as a first-class signal. Deterministic.
	ReconcileExpectations(ctx context.Context) ([]Reconciliation, error)

	// KnowledgeGaps is the curiosity / knowledge-gap detector: it scans the store
	// for its weak spots — unresolved hypotheses (predicted, never validated or
	// refuted) and contested claims (multiple live contradictions) — and ranks them
	// by expected information gain (salience × uncertainty × staleness), returning
	// the top ones as an agenda of "what to seek next". Turns memory from reactive
	// to agenda-setting: mnemos proposes, the agent disposes. Deterministic.
	KnowledgeGaps(ctx context.Context, limit int) ([]Gap, error)

	// Synthesize derives the skill layer from experience: it clusters the store's
	// actions-with-outcomes into lessons, then groups lessons into playbooks. Both
	// steps are idempotent (upsert by cluster), so it is safe to run on a schedule —
	// the auto-trigger that turns the skill loop from CLI-only into self-maintaining
	// (also invokable inline via [ConsolidateOptions.Synthesize]). A no-op on
	// backends without the action / lesson / playbook layer.
	Synthesize(ctx context.Context) (SynthesizeResult, error)

	// WhoKnows is the transactive "who-knows-what" directory: it ranks the workers
	// whose memory is most relevant to a query by affinity (how strongly their
	// claims cover the topic) × reliability (the mean trust of those claims), so a
	// knowledge gap routes to the right expert-memory instead of a flat search.
	// Unattributed claims are ignored; empty when no attributed claim matches.
	WhoKnows(ctx context.Context, query string, limit int) ([]Expert, error)

	// AnalogousClaims finds claims whose surrounding typed subgraph is structurally
	// similar to the one around claimID — retrieval by relational SHAPE
	// (Weisfeiler-Lehman fingerprinting over the epistemic graph), not content. It
	// surfaces past situations with the same causal skeleton even when the claims,
	// services, or metrics differ — "we've seen this shape before", something a
	// pure-vector store cannot do. One representative per neighbourhood, strongest
	// structural similarity first; empty when the anchor has no local structure.
	AnalogousClaims(ctx context.Context, claimID string, limit int) ([]Analogy, error)

	// RecallIterative runs recall as a bounded retrieve↔reason fixpoint instead of a
	// single shot: it retrieves, checks coverage (the sufficiency / CRAG signal),
	// and while memory is still thin it reasons a follow-up — expanding the query
	// toward the strongest new finding and deepening a graph hop — then retrieves
	// again, accumulating results. It stops when coverage is reached, when a round
	// adds nothing new (saturation), or at maxRounds (budget; a sensible default
	// when <= 0). Returns the accumulated results (trust-ranked) and the rounds run.
	// Deterministic controller — no LLM.
	RecallIterative(ctx context.Context, q Query, maxRounds int) ([]Result, int, error)

	// RecallWithConflicts is Recall plus the "contested frontier": for each recalled
	// claim, any currently-valid claim that CONTRADICTS it over the epistemic graph.
	// So a recall carries its own live counter-evidence — the caller sees that a
	// claim is disputed, and by what, before relying on it. Only unresolved
	// contradictions surface (the contradicting claim must be valid and not
	// superseded). The []Result is exactly what Recall returns; deterministic,
	// best-effort (a graph-read failure yields the results with no conflicts).
	RecallWithConflicts(ctx context.Context, q Query) ([]Result, []Conflict, error)

	// Blocks returns an owner's working-memory blocks (its "core memory":
	// bounded, labeled, mutable text), label-ordered. An owner with none gets an
	// empty slice. Returns [ErrBlocksUnsupported] on a backend without the block
	// store. Reading blocks is how a caller always-loads them ahead of retrieval.
	Blocks(ctx context.Context, owner string) ([]Block, error)

	// SetBlock replaces (or creates) an owner's block under label. The value must
	// fit [WorkingMemoryBlockLimit] — working memory is bounded on purpose; an
	// over-limit value is [ErrBlockTooLarge]. An empty value deletes the block.
	SetBlock(ctx context.Context, owner, label, value string) error

	// AppendBlock appends text to an owner's block, keeping it within
	// [WorkingMemoryBlockLimit] by evicting the OLDEST lines when it would
	// overflow (the size cap IS the attention budget). Creates the block if absent.
	AppendBlock(ctx context.Context, owner, label, text string) error

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

	// Consolidate runs a maintenance "sleep" pass over the semantic layer: it
	// finds near-duplicate claims (by embedding similarity) and collapses each
	// group into one canonical claim — repointing the duplicates' evidence onto
	// the survivor (nothing is lost) — then recomputes trust so the freshly
	// merged evidence counts are reflected. This is the gist-extraction /
	// renormalisation the brain does offline: many overlapping traces become one
	// consolidated memory, keeping recall sharp and the store from bloating.
	//
	// It mutates derived structure, not user assertions, so it is a maintenance
	// operation (run it off the hot path, on a schedule). DryRun previews the
	// plan without changing anything. Idempotent — a second pass with nothing new
	// to merge is a no-op.
	Consolidate(ctx context.Context, opts ConsolidateOptions) (ConsolidateResult, error)

	// Hypercorrections surfaces the metacognitive alerts: currently-valid
	// contradictions where the challenged claim is well-established (high trust or
	// human-promoted). When new evidence contradicts a belief the store was
	// confident in, that is the epistemic event most worth a human's attention —
	// either the established belief is now wrong and must be updated, or the new
	// claim is suspect. Results are ordered most-established-first, so the caller
	// can route the highest-stakes conflicts to the front of the curation queue.
	// It reads the epistemic graph (contradicts edges, populated on write); a
	// contradiction of a low-trust, unvetted claim is not an alert and is omitted.
	Hypercorrections(ctx context.Context) ([]Hypercorrection, error)

	// Calibration measures whether stated confidence is itself trustworthy — the
	// accuracy half of metacognition. It groups every claim that was later BORNE
	// OUT or REFUTED by an observed outcome (a validates / refutes edge, minted
	// when a decision's outcome is attached) into stated-confidence buckets, and
	// compares the predicted confidence in each bucket to how often those claims
	// actually turned out right. A well-calibrated store's 0.9-confidence claims
	// are right about 90% of the time; a gap means confidence is over- or
	// under-stated. Only outcome-adjudicated claims count, so Samples is small
	// until outcomes accumulate — read it before trusting the curve. Pure read.
	Calibration(ctx context.Context) (Calibration, error)

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
