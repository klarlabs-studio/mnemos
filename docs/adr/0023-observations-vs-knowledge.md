# ADR 0023: Observations are not beliefs — route operational events to the episodic layer

- **Status:** Proposed (part 1 shipped; part 2 awaiting decision).
- **Date:** 2026-07-19
- **Deciders:** Felix Geelhaar
- **Scope:** What extraction does with a claim that asserts *current system state*
  rather than durable knowledge. Companion to the narration-filter work — both address
  one root cause: capture ingesting conversation as belief.

## Context

The belief graph is built for durable knowledge: a decision and its reasoning, a fact
about how a system behaves, a constraint. Its two load-bearing assumptions are that a
claim's truth is stable over time (trust decays slowly, 90-day half-life) and that two
claims on the same subject with opposing polarity are a contradiction worth surfacing.

A large share of captured claims violate both. They assert **current operational state**:

- `mnemos doesn't work` · `warden isn't installed` · `briefkasten is HTTP-only`
- `the full suite is green` · `all five surfaces are wired and tested` · `build is clean`

Every one was **true when written**. None is wrong at write time. But they do not *erode*
with age — they *flip* on an event (a deploy, a fix, a commit), instantly, from true to
false. Measured against a real brain: six such claims went stale in **0.3–1.0 days**, while
the 90-day (and even a 14-day) half-life left them recalling at 93–100% freshness at the
moment they were already false. They also generate false contradictions against each other
across unrelated sessions (`suite is green` vs `suite is failing`), inflating the
contradiction count that the recall warning depends on.

A census of a real 5,210-claim brain, re-measured on the non-mnemos subset after the
narration prune (so it is representative, not meta-skewed), found observations are **~4%
by the classifier floor, ~8–10% true** — dominated by transient CI/build status.

Two sub-classes, with different value:

1. **Status chatter** — `the suite is green`, `all tests pass`, `all wired`. No lasting
   value; not history anyone queries later. The larger share (~1% of active caught at
   ~100% precision, continuously regenerated every session).
2. **Operational events** — `deployed v1.2.3 at 14:00`, `released v0.104.0`, `the incident
   started 09:15`. Genuine history worth keeping — but as *episodic record*, not as a
   *belief* that recalls at full confidence a month later.

## Decision

### Part 1 — drop status chatter at extraction (SHIPPED)

Status chatter is treated like narration: dropped by the extraction junk filter
(`statusChatterRE`, subject-gated to build/test/CI/wiring nouns so a bare state word in a
durable claim — "the retry logic is working by design" — is never caught). `mnemos prune
--narration` cleans already-stored chatter through the same filter. This is the cheap,
high-precision, larger share.

### Part 2 — route operational events to the episodic layer (PROPOSED)

An operational event carries an actor, an action, and a time. Mnemos already has the
episodic/temporal layer for exactly this: `remember_episode`, `timeline_query`,
`recall_at_time`. The proposal is that extraction classify an event-observation and route
it there instead of into the belief graph, so it:

- is retrievable as **history** (`timeline_query`, "what happened around the release?"),
- never competes with knowledge at belief recall,
- never feeds pairwise contradiction detection.

**Sketch of the mechanism:**

- An `eventObservationRE`/classifier in `internal/extract` returning
  `knowledge | event | (chatter→already dropped)`. Built from the same lexicon discipline
  as `systemStateRE`: an action verb (`deployed|released|shipped|merged|rolled back`) plus
  an artifact/version, with the same under-catch bias — a miss routes to belief (status
  quo), never loses data.
- In the pipeline, an event-classified claim becomes an `Event`/episode with its timestamp
  rather than (or in addition to) a `Claim`. Open question: dual-write (episode + a
  short-half-life claim) vs episode-only.
- Recall reads beliefs; a separate timeline surface reads episodes. No change to the belief
  recall path.

## Open questions (why this is Proposed, not Accepted)

1. **Episode-only or dual-write?** Episode-only keeps the belief graph clean but means an
   event never surfaces in ordinary recall, only in explicit timeline queries. Dual-write
   keeps it recallable but reintroduces the aging problem (mitigated by the volatility
   half-life). Leaning episode-only.
2. **Classifier precision bar.** Mis-routing a durable claim to episodic hides it from
   belief recall — an invisible loss, the same failure mode the volatility work guarded
   against. The bar must be high and the bias toward "leave it a belief."
3. **Is the ROI worth it?** At ~8–10% observations of which events are the *smaller* part,
   the event-routing addresses maybe 2–4% of claims. Part 1 (chatter drop) plus the shipped
   contested-demotion and volatility half-life may be sufficient, with this deferred until
   a non-meta corpus shows events are a real recall problem in practice.

## Consequences

- **Part 1** removes a continuous source of noise at ~100% precision and closes a false-
  contradiction generator, at the cost of a small filter surface.
- **Part 2**, if built, makes "what was the state of X at time T" a first-class query and
  stops operational snapshots masquerading as settled belief — at the cost of a new
  classifier (a precision risk), pipeline routing, and the episode/belief split becoming
  load-bearing.
- Declining Part 2 leaves operational events as beliefs with a volatility half-life —
  workable, since the half-life plus contested-demotion reduce their prominence, but it
  keeps the model impure.

## Guardrails (when built)

- Classifier precision measured against a real brain before shipping, same as every filter
  in this line of work; under-catch (leave as belief) is the safe failure direction.
- A test asserting operational events route to episodes and durable claims do not.
- Re-census on a non-mnemos corpus to confirm events are a real share before investing.
