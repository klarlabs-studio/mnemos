package mnemos

import "context"

// Assimilation kinds returned by [Memory.ClassifyClaim].
const (
	// AssimilationFits — the claim's topic is already covered by established
	// (sufficient) knowledge: it can be learned cheaply, against an existing schema.
	AssimilationFits = "assimilate"
	// AssimilationNovel — no established knowledge covers this: it must be learned
	// the full way (accommodation), earning corroboration from scratch.
	AssimilationNovel = "accommodate"
)

// Assimilation classifies how a candidate claim relates to what the store already
// knows well — the assimilation-vs-accommodation routing signal. A claim that
// FITS established knowledge is cheap to learn; a NOVEL one needs full
// corroboration. (A claim that CONTRADICTS a high-trust schema is surfaced by the
// existing hypercorrection path once written — that is the accommodation-by-
// conflict branch.)
type Assimilation struct {
	Kind       string // AssimilationFits | AssimilationNovel
	AnchorID   string // the best-matching established claim, when it fits
	AnchorText string
	Confidence float64 // aggregate confidence of the established knowledge on this topic
}

// ClassifyClaim implements [Memory.ClassifyClaim]: recall the claim's topic and,
// if the store already holds established (sufficient) knowledge on it, route it as
// assimilation against that anchor; otherwise as novel accommodation.
func (m *memory) ClassifyClaim(ctx context.Context, text string) (Assimilation, error) {
	results, suf, err := m.RecallWithSufficiency(ctx, Query{Text: text, Limit: 3})
	if err != nil {
		return Assimilation{}, err
	}
	if suf.Sufficient && len(results) > 0 {
		return Assimilation{
			Kind:       AssimilationFits,
			AnchorID:   results[0].ClaimID,
			AnchorText: results[0].Text,
			Confidence: suf.Confidence,
		}, nil
	}
	return Assimilation{Kind: AssimilationNovel, Confidence: suf.Confidence}, nil
}
