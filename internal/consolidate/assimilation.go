// This file implements ADR 0013 §5: schema-consistency fast-assimilation — the
// POSITIVE complement to the contradiction engine.
//
// Dissonance (the negative side of schema fit) is already first-class in Mnemos:
// contradictions are detected and routed to the dissonance-resolution path (see
// promote.go gate 3, and internal/relate). The Piaget / Tse et al.
// schema-consistency effect — that information CONSISTENT with an existing
// schema integrates *faster* (assimilation) while INCONSISTENT information must
// update the schema or be rejected (accommodation) — was missing.
//
// AssessAssimilation is a pure, I/O-free classifier. Given a new item's
// statement (a claim or a candidate lesson) and the existing schemas, it
// reports one of three fits:
//
//   - Consistent: the statement matches an existing schema (Jaccard ≥ threshold,
//     reusing exactly the tokenization + similarity the promotion clustering
//     uses) AND does not contradict it. It earns a BOUNDED, ADDITIVE boost that
//     accelerates consolidation / raises promotion priority.
//   - Dissonant: the statement contradicts an existing schema (detected by
//     internal/relate's contradiction detection — the same engine promote.go
//     uses). It earns NO boost and is flagged for the accommodation path so the
//     conflict is resolved rather than silently assimilated.
//   - Neutral: no matching schema. Treated normally, no boost.
//
// The boost is deliberately small and capped (MaxAssimilationBoost): it SPEEDS
// integration of schema-consistent information, it does NOT bypass the ADR-0012
// subject-eligibility gate or the ADR-0007 no-leak (cross-tenant corroboration /
// de-identification) gates — those live in promote.go and are untouched. A
// belief that is schema-consistent but about an individual subject, or seen in
// only one tenant, still cannot promote.

package consolidate

import (
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/relate"
)

// MaxAssimilationBoost caps the additive consolidation/priority boost a
// schema-consistent item may earn. It is intentionally small: assimilation
// accelerates integration of information that fits an established schema, it
// never overwhelms the evidence-based confidence signal (corroboration ×
// consistency × recency) it is added to, and it can never push a candidate past
// the eligibility or no-leak gates.
const MaxAssimilationBoost = 0.15

// SchemaFit classifies a new item's relationship to the existing schemas.
type SchemaFit string

const (
	// FitNeutral means no existing schema matches the item — consolidate it
	// normally, with no assimilation boost.
	FitNeutral SchemaFit = "neutral"
	// FitConsistent means the item matches an existing schema and does not
	// contradict it — assimilation. Accelerate its consolidation (bounded boost).
	FitConsistent SchemaFit = "consistent"
	// FitDissonant means the item contradicts an existing schema — accommodation.
	// It earns no boost and is routed to the dissonance-resolution path.
	FitDissonant SchemaFit = "dissonant"
)

// AssimilationSignal is the result of assessing a new item against the existing
// schemas. It is a pure value: no tenant identity, no I/O, safe to log.
type AssimilationSignal struct {
	// Fit is the schema-fit classification.
	Fit SchemaFit
	// Similarity is the Jaccard similarity of the item to its nearest schema
	// (0 when there is no schema to compare against, or the item has no content
	// tokens).
	Similarity float64
	// MatchStatement is the nearest / conflicting schema's statement, retained for
	// audit and provenance. Empty when Fit is FitNeutral.
	MatchStatement string
	// Boost is the bounded, additive consolidation/priority boost to apply. It is
	// > 0 only when Fit is FitConsistent, and always in [0, MaxBoost]. Callers add
	// it to an evidence-based confidence/priority score; it never replaces that
	// score and never bypasses a downstream gate.
	Boost float64
}

// Consistent reports whether the item assimilates into an existing schema (and
// should therefore consolidate faster).
func (s AssimilationSignal) Consistent() bool { return s.Fit == FitConsistent }

// NeedsAccommodation reports whether the item contradicts an existing schema and
// must be routed to the dissonance-resolution path rather than assimilated.
func (s AssimilationSignal) NeedsAccommodation() bool { return s.Fit == FitDissonant }

// AssimilationOptions tunes AssessAssimilation. The zero value reproduces the
// project defaults (the same equivalence threshold the promotion clustering uses,
// and MaxAssimilationBoost).
type AssimilationOptions struct {
	// Jaccard is the statement-equivalence threshold at which the item is treated
	// as matching a schema. Defaults to DefaultEquivalenceJaccard — the SAME
	// threshold the cross-tenant promotion clustering uses, so the positive
	// (assimilation) and cross-tenant (corroboration) paths never drift apart.
	Jaccard float64
	// MaxBoost caps the additive boost. Defaults to MaxAssimilationBoost.
	MaxBoost float64
}

func (o AssimilationOptions) withDefaults() AssimilationOptions {
	if o.Jaccard <= 0 {
		o.Jaccard = DefaultEquivalenceJaccard
	}
	if o.MaxBoost <= 0 {
		o.MaxBoost = MaxAssimilationBoost
	}
	return o
}

// AssessAssimilation classifies a new item's statement against the existing
// schemas and returns the assimilation signal. It is pure and deterministic:
// contradiction detection reuses internal/relate (the same engine promote.go's
// gate 3 uses) and consistency reuses relate.ContentTokens + the promotion
// clustering's Jaccard — nothing is reinvented.
//
// Order of resolution (contradiction is checked FIRST, so a token-similar but
// opposite-polarity statement is correctly routed to accommodation, never
// assimilated):
//
//  1. No content tokens, or no schemas → FitNeutral, no boost.
//  2. Contradicts any schema → FitDissonant, no boost (accommodation).
//  3. Nearest schema's Jaccard ≥ threshold → FitConsistent, bounded boost
//     scaled by the similarity.
//  4. Otherwise → FitNeutral, no boost.
//
// If contradiction detection cannot run it FAILS CLOSED to "no acceleration":
// the item is not boosted (it is treated as neutral), so an unrunnable check
// never grants a speed-up it did not earn.
func AssessAssimilation(statement string, schemas []domain.Schema, opts AssimilationOptions) AssimilationSignal {
	opts = opts.withDefaults()

	toks := relate.ContentTokens(statement)
	if len(toks) == 0 || len(schemas) == 0 {
		return AssimilationSignal{Fit: FitNeutral}
	}

	// Step 2: contradiction (accommodation) — reuse internal/relate exactly as
	// promote.go's gate 3 does. Fail closed: on a detector error we withhold the
	// boost rather than treat "unrunnable" as "consistent".
	if conflict, ok, err := contradictsSchema(statement, schemas); err == nil && ok {
		return AssimilationSignal{
			Fit:            FitDissonant,
			Similarity:     schemaSimilarity(toks, conflict),
			MatchStatement: conflict.Statement,
		}
	}

	// Step 3: nearest schema by Jaccard (the promotion-clustering similarity).
	best := -1
	bestSim := 0.0
	for i := range schemas {
		sim := schemaSimilarity(toks, schemas[i])
		if sim > bestSim {
			bestSim = sim
			best = i
		}
	}
	if best < 0 || bestSim < opts.Jaccard {
		return AssimilationSignal{Fit: FitNeutral, Similarity: bestSim}
	}

	// Consistent: bounded, additive boost scaled by similarity. bestSim ∈ (0,1],
	// so boost ∈ (0, MaxBoost] — always bounded.
	return AssimilationSignal{
		Fit:            FitConsistent,
		Similarity:     bestSim,
		MatchStatement: schemas[best].Statement,
		Boost:          opts.MaxBoost * bestSim,
	}
}

// schemaSimilarity is the Jaccard similarity between a pre-tokenized statement
// and a schema's statement, using the same tokenization + jaccard the promotion
// clustering uses.
func schemaSimilarity(toks map[string]struct{}, s domain.Schema) float64 {
	return jaccard(toks, relate.ContentTokens(s.Statement))
}

// contradictsSchema reports whether statement contradicts any of the schemas,
// reusing internal/relate's contradiction detection (the same path promote.go
// uses). It returns the first conflicting schema. It fails closed: any detector
// error is propagated so the caller withholds the boost rather than silently
// treating an unrunnable check as "no contradiction".
func contradictsSchema(statement string, schemas []domain.Schema) (domain.Schema, bool, error) {
	existing := make([]domain.Claim, len(schemas))
	byID := make(map[string]int, len(schemas))
	for i := range schemas {
		id := schemaClaimID(i)
		existing[i] = domain.Claim{ID: id, Text: schemas[i].Statement}
		byID[id] = i
	}
	cand := domain.Claim{ID: "assimilation_cand", Text: statement}
	rels, err := relate.NewEngine().DetectIncremental([]domain.Claim{cand}, existing)
	if err != nil {
		return domain.Schema{}, false, err
	}
	for _, r := range rels {
		if r.Type != domain.RelationshipTypeContradicts {
			continue
		}
		other := ""
		if r.FromClaimID == cand.ID {
			other = r.ToClaimID
		} else if r.ToClaimID == cand.ID {
			other = r.FromClaimID
		}
		if idx, ok := byID[other]; ok {
			return schemas[idx], true, nil
		}
	}
	return domain.Schema{}, false, nil
}

// schemaClaimID mints a stable, collision-free synthetic claim id for a schema at
// index i, used only inside contradiction detection.
func schemaClaimID(i int) string {
	// "sch_" prefix keeps it distinct from the candidate id and from any real
	// cl_ claim id, so byID lookups never alias.
	return "sch_" + itoa(i)
}

// itoa is a tiny non-negative int formatter to avoid importing strconv for one
// call site.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}
