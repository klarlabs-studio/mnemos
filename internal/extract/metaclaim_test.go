package extract

import "testing"

// Commentary about the graph, taken from real recall blocks. Correcting a
// belief in conversation added a second claim discussing the first, which then
// recalled as though it were evidence and buried the original.
func TestIsJunkClaim_MetaCommentaryAboutBeliefs(t *testing.T) {
	meta := []string{
		"that's the fourth stale belief this session",
		"this is another outdated claim",
		"correcting the memory that said briefkasten was HTTP-only",
		"deprecating the stale belief about warden",
		"superseding the previous claim",
		"the claim that said mnemos doesn't work",
		"updating the stale claim about the endpoint",
		"that's a wrong belief",
	}
	for _, s := range meta {
		if !isJunkClaim(s) {
			t.Errorf("kept meta-commentary as a claim: %q", s)
		}
	}
}

// The failure that would matter most. A real correction is the single most
// valuable kind of claim in the brain — it overwrites something already there
// and misleading. Matching on the opener ("actually", "correcting") would drop
// exactly these, which is why the filter keys on the SUBJECT being a
// memory-system noun instead.
func TestIsJunkClaim_KeepsRealCorrections(t *testing.T) {
	corrections := []string{
		"actually the retry budget is 3 attempts, not 5",
		"correcting an earlier number: the timeout is 240s, not 90s",
		"the deploy did not fail; the health check was misconfigured",
		"warden is installed after all, at tools/warden",
		"briefkasten supports stdio as of v0.18.0, not just HTTP",
		"that estimate was wrong: the migration took six hours",
	}
	for _, s := range corrections {
		if isJunkClaim(s) {
			t.Errorf("dropped a real correction — the highest-value claim type: %q", s)
		}
	}
}

// Nouns like "entry", "record" and "fact" were deliberately excluded from the
// memory-noun list: measured against a real brain they matched things like
// "your shared/go.sum … ← stale/wrong entry", which is a fact about a lockfile.
// Ambiguous phrasing must fall through to being kept.
func TestIsJunkClaim_AmbiguousNounsAreKept(t *testing.T) {
	ambiguous := []string{
		"updating the existing record",
		"the go.sum entry is stale",
		"that database record is wrong",
	}
	for _, s := range ambiguous {
		if isJunkClaim(s) {
			t.Errorf("dropped an ambiguous-noun claim that may be about the world: %q", s)
		}
	}
}

// Ordinary facts that happen to contain memory-system vocabulary must survive.
func TestIsJunkClaim_KeepsFactsMentioningMemoryNouns(t *testing.T) {
	keep := []string{
		"claims require at least one evidence event",
		"the claim repository enforces foreign keys",
		"belief credit assignment is described in ADR 0014",
		"a memory leak in the sampler caused the restart",
	}
	for _, s := range keep {
		if isJunkClaim(s) {
			t.Errorf("dropped a real fact that mentions memory vocabulary: %q", s)
		}
	}
}
