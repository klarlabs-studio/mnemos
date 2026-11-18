package query

// Ablation switches off individual recall ranking components, one field per
// component. It exists so an experiment (internal/brainbench's retrieval
// ablation) can measure what each component contributes by turning it off
// inside the shipped engine. The alternative is a copy of the ranker with the
// component removed, and a copy measures itself, not the product.
//
// The zero value is shipped recall. No surface sets these fields: they are not
// in recallParams, the MCP schemas, REST, gRPC or the library Query.
//
// Recall's ranking components, and how each one is removed:
//
//   - Dense: cosine similarity over stored embeddings, used for event
//     ranking, for claim-candidate widening and in the claim re-ranker.
//     Removed by dropping the engine's embedding client. That is the state of
//     a deployment with no embedder configured, so the ablated engine runs the
//     degradation path the product already has.
//   - Lexical: BM25 over the event and claim full-text indexes, in the same
//     three places. Removed by dropping the text searchers. With both legs
//     gone the event ranker falls back to token overlap.
//   - Curation: the admission rules that remove deprecated (retracted,
//     forgotten) and session-local claims, and claims whose valid time has
//     closed or that were archived. With it off, every stored claim is a
//     candidate. An explicit AsOf point-in-time filter is the caller's own
//     time bound, not curation, and stays in force, as does the ADR 0037
//     event-retraction filter, which removes episodes rather than ranking
//     claims.
//   - Salience: the bounded stakes term (ADR 0013 §4). Overrides
//     AnswerOptions.Salient, which WithCognitiveDefaults turns on.
//   - Inhibition: the competitive-inhibition penalty (ADR 0016). The
//     write-back that records inhibition is not affected; only the penalty is
//     no longer read at ranking.
//   - IntentBoost: the reorder that puts claims of the question's inferred
//     type (decision, hypothesis, fact) first.
//   - Trust: the bounded trust factor on relevance (ADR 0039). MinTrust, a
//     caller's filter, is not affected.
//   - Freshness: the bounded freshness factor on relevance (ADR 0039), the
//     trust package's decay measured at the read's instant. The stale-claim
//     flag is not affected.
//
// Contradiction resolution needs no field here: it is selected by
// AnswerOptions.Consumer, and ConsumerUser turns it off.
type Ablation struct {
	Dense       bool
	Lexical     bool
	Curation    bool
	Salience    bool
	Inhibition  bool
	IntentBoost bool
	Trust       bool
	Freshness   bool
}

// ablated returns the engine with the retrieval legs a removes. The ranking
// code is unchanged: it already degrades when a leg is not wired.
func (e Engine) ablated(a Ablation) Engine {
	if a.Dense {
		e.embedClient = nil
	}
	if a.Lexical {
		e.eventTextSearch = nil
		e.claimTextSearch = nil
	}
	e.noInhibition = a.Inhibition
	return e
}
