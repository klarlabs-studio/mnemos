package domain

// EvidenceRepeatWeight is how much a REPEATED piece of evidence from an
// already-seen source counts toward corroboration, relative to a fresh
// independent source (which counts 1.0). 0.5 = "a second voice saying the same
// thing corroborates more than the same voice saying it twice, but the repeat
// isn't worthless." This is the echo-chamber guard.
const EvidenceRepeatWeight = 0.5

// EffectiveEvidenceCount grades a claim's evidence for corroboration by
// INDEPENDENCE, not raw volume: each distinct source (an evidence event's author)
// counts fully, and same-source repeats count at [EvidenceRepeatWeight]. So five
// events from one author corroborate far less than five events from five authors,
// preventing a single voice from manufacturing consensus. The result is floored
// (conservative) and never below the distinct-source count. Storage backends pass
// it as the evidenceCount to the trust scorer; the trust formula is unchanged.
//
// It lives in domain (not trust) so every storage backend can call it without
// depending on the scoring package.
func EffectiveEvidenceCount(distinctSources, totalEvents int) int {
	if distinctSources < 0 {
		distinctSources = 0
	}
	if totalEvents < distinctSources {
		totalEvents = distinctSources
	}
	repeats := totalEvents - distinctSources
	return distinctSources + int(EvidenceRepeatWeight*float64(repeats))
}
