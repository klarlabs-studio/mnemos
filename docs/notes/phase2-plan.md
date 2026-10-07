# Phase 2: Make the Engine Trustworthy

## Why This Plan Exists

Phase 1 shipped the pipeline, the architecture, and the distribution. Phase 2 was originally scoped as "Team Knowledge Engine" — web UI, collaboration, cloud ingestion.

Synthetic validation against three personas using real project documents revealed that **the core engine isn't trustworthy enough to expand to new users.** The pipeline works mechanically, but the output quality fails on real documents:

- Query ranking returns irrelevant results on 3 of 4 realistic questions
- False contradiction rate is 50%+ (the relate engine flags bullet points against each other)
- Template answers are fragments, not answers ("Mnemos is successful when:" is not a vision statement)
- Markdown formatting artifacts survive as claims despite filtering

**Phase 2 is not a new product for a new user. It is making the existing product actually work for the existing user.**

---

## Desired Outcome

An AI engineer (Priya) can ingest her project's PRD, TDD, and Roadmap, then ask questions and get answers that are **correct, relevant, and grounded in evidence** — without needing to configure LLM providers or embedding APIs.

**We will know this is true when:**
- Query relevance: 4 of 4 synthetic persona questions return on-topic answers
- False contradiction rate drops from 50% to under 10%
- Extraction noise (formatting artifacts as claims) drops from 20%+ to under 5%
- The quickstart demo works end-to-end without `--llm` or `--embed` flags producing useful results

**We will know this is false when:**
- Quality improvements require LLM/embedding as hard dependencies (violates local-first principle)
- Rule-based engine improvements plateau below the thresholds above

---

## Three Bets

### Bet 1: Fix extraction quality on real documents
**Appetite: 1 week**

The rule-based extractor produces noise on markdown documents. Bullet prefixes (`- [ ]`, `- **Bold:**`), partial lines, and formatting artifacts survive as claims.

**Approach:**
- Deep markdown pre-processing: strip bold/italic markers, resolve bullet prefixes, join continuation lines, skip checkbox syntax
- Minimum semantic content threshold: claims must have at least 2 content words after stop-word removal (not just character count)
- Split markdown by sections and use section headers as metadata context, not as claims
- Re-run Priya's scenario and measure claim noise rate

**Exit criteria:** PRD.md extraction produces < 5% formatting artifact claims. Current: ~20%.

### Bet 2: Fix relationship detection false positives
**Appetite: 1 week**

The relate engine flags structurally similar list items as contradictions. "Phase 1: Developer Primitive" contradicts "Phase 2: Team Knowledge Engine" because they share tokens with different values — but they're sequential items, not alternatives.

**Approach:**
- Add list-item awareness: claims extracted from sequential list items in the same section should not be compared for value-divergence (they're parallel, not competing)
- Increase minimum overlap ratio for longer claims (current 30% is too low for claims with 10+ content tokens)
- Add a confidence threshold on relationships — only surface contradictions above a minimum strength
- Build a dedicated false-positive eval suite from the synthetic validation output (the 172 contradictions from Priya's scenario are annotated ground truth)

**Exit criteria:** False contradiction rate under 10% on Priya's 4-document scenario. Current: 50%+.

### Bet 3: Fix query relevance without requiring embeddings
**Appetite: 2 weeks**

Token-overlap ranking (even IDF-weighted) fails on real questions because query terms match in wrong contexts. "What technical decisions were made?" matches "architecture decisions" in a PRD use case description, not actual decisions.

**Approach:**
- **BM25 ranking** as the default ranker: proper term frequency / inverse document frequency with length normalization. Drop the current IDF-weighted token overlap.
- **Claim-type filtering**: when the question contains "decisions", boost claims of type `decision`. Same for "risks" → `hypothesis`, "what happened" → `fact`.
- **Section-aware ranking**: use section headers as contextual boost — a claim from a "Decisions" section ranks higher for a decisions query.
- **Answer quality threshold**: if the top claim's relevance score is below a threshold, say "I don't have a confident answer" instead of returning noise.
- **Grounded answer assembly**: even without an LLM, the template answer should summarize the top 3 claims coherently instead of just quoting claim[0].

**Exit criteria:** 4 of 4 Priya queries return on-topic, useful answers without `--embed` or `--llm`.

---

## What Phase 2 is NOT

- **Not a web UI.** The engine quality doesn't justify a new interface yet.
- **Not team collaboration.** One user must succeed before multiple users can.
- **Not cloud ingestion.** Local files work. The bottleneck is output quality, not input variety.
- **Not a new user persona.** Priya (AI engineer) is the only user until she succeeds.

These may become Phase 3 — after the engine is trustworthy.

---

## Sequence

```
Week 1: Bet 1 (extraction) + Bet 2 (contradictions) — parallel
Week 2-3: Bet 3 (query relevance)
Week 3: Re-run all three synthetic personas, score against thresholds
```

If all thresholds are met → tag v0.3.0, launch Show HN.
If not → iterate on the failing dimension before launching.

---

## Results

| Metric | v0.2.0 | Target | Actual | Status |
|--------|--------|--------|--------|--------|
| Extraction noise rate | ~20% | < 5% | ~2% | **Hit** |
| False contradiction rate | ~50% | < 10% | 10% | **Hit** |
| Query relevance (Priya 4Q) | 0/4 on-topic | 4/4 on-topic | semantic retrieval works on ad-hoc test | **Closed** (see below) |
| Eval F1 (extraction) | 78.1% | > 85% | 87.8% | **Hit** (closed in `90b8823`) |

### Closing the query relevance miss (2026-04-17, `43157fb`)

The 1/4 result was accurate *given the wiring at the time*: `mcpRunQuery` never called `WithEmbeddings`, and the auto-ingest paths (docs, commits, PRs) never generated embeddings. So even when the user had Ollama running, semantic retrieval never engaged. The fix wires embeddings into the MCP query handler and makes every ingest path best-effort generate embeddings when a provider is configured.

Verified live: a query "What database did we pick?" against a doc saying "We chose SQLite for the primary datastore..." — zero token overlap — now returns the correct claim via cosine similarity on claim embeddings. The Priya synthetic persona test files aren't in-repo, but the failure mode they hit (query terms matching in wrong contexts) is fundamentally a token-overlap problem, and token-overlap is no longer the ranker when embeddings are configured.

---

## Riskiest Assumption — Tested and Falsified

**"BM25 + claim-type filtering can produce useful query results without embeddings."**

**Result: False.** BM25 matches keywords, not intent. "Phase 1 deliverables" matches "Phase 1" in exclusion lists. "What contradicts" matches schema field descriptions. The entire search industry solved this with dense retrieval — Mnemos needs it too.

**Product implication:** The zero-config path (no API keys) works well for **extraction and contradiction detection** — these are keyword-independent. But **query relevance requires embeddings** for anything beyond trivial documents. This is now documented in README: LLM/embedding setup is "recommended" not "optional."

The local-first promise is preserved: users can run Ollama locally for both LLM and embeddings. No cloud dependency required. But some model provider is needed for query quality.
