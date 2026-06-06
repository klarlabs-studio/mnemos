package pipeline

import (
	"strings"

	"go.klarlabs.de/mnemos/internal/domain"
)

// DedupeAgainstExisting collapses newly-extracted claims that match
// already-stored claims by normalized text. Same fact restated across
// chunks (issue #24) was producing one claim per chunk; this gates the
// persistence layer so each fact ends up as a single claim with the
// union of its evidence links.
//
// Behavior:
//   - When a new claim's normalized text matches an existing claim's,
//     the new claim is dropped and any evidence links pointing to its
//     temporary id are rewritten to the existing claim's id (so we
//     accumulate evidence rather than orphan it).
//   - Within the new batch we also collapse exact-text duplicates that
//     escaped the LLM engine's per-call dedup (e.g. when the cache
//     returned overlapping fragments).
//   - The order of returned claims is stable for deterministic output.
//
// Returns the deduped (claims, links).
func DedupeAgainstExisting(newClaims []domain.Claim, links []domain.ClaimEvidence, existing []domain.Claim) ([]domain.Claim, []domain.ClaimEvidence) {
	if len(newClaims) == 0 {
		return newClaims, links
	}

	existingByNorm := make(map[string]string, len(existing))
	for _, c := range existing {
		existingByNorm[normalizeForDedupe(c.Text)] = c.ID
	}

	idRemap := make(map[string]string)    // newID -> canonical (existing or first-seen) ID
	keptByNorm := make(map[string]string) // norm -> canonical ID for new claims kept this round
	deduped := make([]domain.Claim, 0, len(newClaims))

	for _, c := range newClaims {
		norm := normalizeForDedupe(c.Text)
		if norm == "" {
			continue
		}
		if existingID, ok := existingByNorm[norm]; ok {
			idRemap[c.ID] = existingID
			continue
		}
		if firstID, ok := keptByNorm[norm]; ok {
			idRemap[c.ID] = firstID
			continue
		}
		keptByNorm[norm] = c.ID
		deduped = append(deduped, c)
	}

	if len(idRemap) == 0 {
		return deduped, links
	}

	rewritten := make([]domain.ClaimEvidence, 0, len(links))
	seenLink := make(map[string]struct{}, len(links))
	for _, l := range links {
		if mapped, ok := idRemap[l.ClaimID]; ok {
			l.ClaimID = mapped
		}
		key := l.ClaimID + "\x00" + l.EventID
		if _, dup := seenLink[key]; dup {
			continue
		}
		seenLink[key] = struct{}{}
		rewritten = append(rewritten, l)
	}
	return deduped, rewritten
}

// normalizeForDedupe mirrors the helper in internal/extract — kept
// here as a private duplicate to avoid pulling the extract package
// into pipeline (and the import cycle that would create with the
// pipeline-uses-extract direction).
func normalizeForDedupe(text string) string {
	normalized := strings.ToLower(strings.TrimSpace(text))
	replacer := strings.NewReplacer(",", "", ".", "", ";", "", ":", "", "!", "", "?", "", "\t", " ")
	normalized = replacer.Replace(normalized)
	normalized = strings.Join(strings.Fields(normalized), " ")
	return normalized
}
