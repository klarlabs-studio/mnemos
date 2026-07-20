package relate

import "testing"

// These pairs were all recorded as `contradicts` in a production brain, where
// 47,683 of 48,725 contradiction edges (97.9%) linked claims from different
// sessions — different projects, months apart. At that volume the recall
// hook's "N contradictions recorded on this topic" warning fires on every
// prompt, which trains the reader to ignore it and destroys the signal when a
// contradiction is real.
//
// Each case names the detector that produced it.
func TestNoContradiction_ProductionFalsePositives(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		why  string
	}{
		{
			name: "sentence-initial capitals are not competing names",
			a:    "Now the overlay",
			b:    "Clear picture now",
			why:  "entity path: 'Now' and 'Clear' were read as proper nouns diverging over the shared token 'now'",
		},
		{
			name: "a shared digit is not a shared topic",
			a:    "269 → 9",
			b:    "Removed the four services from compose (9 → 6: consul, postgres, ai-services, redis, dev)",
			why:  "numeric path: overlap counted the numerals themselves, so {9, →} scored 0.67 topical agreement",
		},
		{
			name: "unrelated claims from different projects",
			a:    "Release build is still running",
			b:    "Demo vault (GitHub, Netflix, Chase, Google, Amazon) with placeholder passwords is hardcoded",
			why:  "no shared subject at all",
		},
		{
			name: "narration fragments",
			a:    "Let me check:",
			b:    "Let me look:",
			why:  "no factual content to disagree about",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			aTok, aNeg := contentTokensAndPolarity(tc.a)
			bTok, bNeg := contentTokensAndPolarity(tc.b)
			bothEnum := isEnumeratedItem(tc.a) && isEnumeratedItem(tc.b)

			if rel, ok := inferRelationshipWithContext(aTok, aNeg, bTok, bNeg, bothEnum); ok && rel == "contradicts" {
				t.Errorf("overlap path flagged a contradiction — %s", tc.why)
			}
			if !bothEnum && detectNumericDivergence(tc.a, tc.b, aTok, bTok) {
				t.Errorf("numeric path flagged a contradiction — %s", tc.why)
			}
			if !bothEnum && detectEntityRoleDivergence(tc.a, tc.b, aTok, bTok) {
				t.Errorf("entity path flagged a contradiction — %s", tc.why)
			}
			if detectTemporalDivergence(tc.a, tc.b, aTok, bTok) {
				t.Errorf("temporal path flagged a contradiction — %s", tc.why)
			}
		})
	}
}

// The genuine detections must survive the tightening. Each of these is the
// canonical case for the path it exercises.
func TestStillDetects_GenuineContradictions(t *testing.T) {
	cases := []struct {
		name string
		a, b string
	}{
		{"numeric disagreement on a shared subject", "the customer had 12 prior refunds", "the customer had 0 prior refunds"},
		{"role assigned to different names", "The CEO is Alice", "The CEO is Bob"},
		{"same, with the names sentence-initial", "Alice is the CEO", "Bob is the CEO"},
		{"aspect conflict on a shared subject", "migration completed Tuesday", "migration is still running"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			aTok, aNeg := contentTokensAndPolarity(tc.a)
			bTok, bNeg := contentTokensAndPolarity(tc.b)
			bothEnum := isEnumeratedItem(tc.a) && isEnumeratedItem(tc.b)

			rel, ok := inferRelationshipWithContext(aTok, aNeg, bTok, bNeg, bothEnum)
			flagged := (ok && rel == "contradicts") ||
				detectNumericDivergence(tc.a, tc.b, aTok, bTok) ||
				detectEntityRoleDivergence(tc.a, tc.b, aTok, bTok) ||
				detectTemporalDivergence(tc.a, tc.b, aTok, bTok)
			if !flagged {
				t.Errorf("genuine contradiction no longer detected: %q vs %q", tc.a, tc.b)
			}
		})
	}
}

// A shared numeral must not, on its own, establish that two claims are about
// the same thing — the property the numeric guard's own comment promises.
func TestWordTokens_ExcludesNumeralsAndSymbols(t *testing.T) {
	tok, _ := contentTokensAndPolarity("269 → 9")
	if got := wordTokens(tok); len(got) != 0 {
		t.Errorf("expected no word tokens in a numerals-and-arrows claim, got %v", got)
	}
	tok, _ = contentTokensAndPolarity("the customer had 12 prior refunds")
	words := wordTokens(tok)
	if _, ok := words["12"]; ok {
		t.Error("numeral retained as a word token")
	}
	if len(words) < 2 {
		t.Errorf("word tokens over-filtered: %v", words)
	}
}

// Round two, from a production brain where the three divergence paths produced
// 38,198 contradiction edges across 16,863 claims (2.2 per belief, ~15x the
// health grader's CRIT threshold). All three shared one blind spot: the overlap
// ratio was measured against the SHORTER claim, so a two-token claim trivially
// scored 1.0 by sharing both its tokens with a long, unrelated one.
func TestNoContradiction_ShortClaimAgainstLongUnrelated(t *testing.T) {
	cases := []struct{ name, a, b string }{
		{"polarity: short claim swallowed by a long one",
			"Found the gap",
			"The scoped check immediately found a real, actionable gap: no test at all"},
		{"polarity: the two claims actually agree",
			"pre_push: [rebase, vet, test]",
			"Warden ran its full pre-push gate (vet + complete test suite)"},
		{"temporal: unrelated completion vs unrelated presence",
			"Storage layer done",
			"Both keys present — the PAT preserved for the release workflow"},
		{"temporal: shared token is incidental",
			"Both agents finished",
			"Both keys present — the PAT preserved for the release workflow"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			aTok, aNeg := contentTokensAndPolarity(tc.a)
			bTok, bNeg := contentTokensAndPolarity(tc.b)
			if rel, ok := inferRelationshipWithContext(aTok, aNeg, bTok, bNeg, false); ok && rel == "contradicts" {
				t.Errorf("polarity path flagged a short-vs-long unrelated pair")
			}
			if detectTemporalDivergence(tc.a, tc.b, aTok, bTok) {
				t.Errorf("temporal path flagged a short-vs-long unrelated pair")
			}
		})
	}
}

// Issue and PR numbers are identifiers, not measurements: "PR #225" and
// "PR #257" name different things, they do not disagree about a quantity.
// This was the largest single source of false numeric edges.
func TestNoContradiction_IdentifiersAreNotMeasurements(t *testing.T) {
	cases := [][2]string{
		{"PR #225", "PR #257 needs a rebase before it can merge"},
		{"Issue #1 (gRPC auth) is crisp and self-contained", "PR #257"},
		{"PRs #1 and #2 merged cleanly", "PRs #1 and #2 are green and unmerged so far"},
	}
	for _, c := range cases {
		aTok, _ := contentTokensAndPolarity(c[0])
		bTok, _ := contentTokensAndPolarity(c[1])
		if detectNumericDivergence(c[0], c[1], aTok, bTok) {
			t.Errorf("numeric path treated identifiers as diverging values: %q vs %q", c[0], c[1])
		}
	}
}

// The genuine contradictions the tightening must preserve — all observed in
// real data, all still detected.
func TestStillDetects_GenuineContradictions_RoundTwo(t *testing.T) {
	cases := [][2]string{
		{"Revenue decreased after launch", "Revenue did not decrease after launch"},
		{"All pushed — remote and local at 007ddf2", "Push didn't land — remote still at 3ca2416, 8 commits unpushed"},
		{"There's an Agent OS memory system here", "Briefkasten doesn't have the Agent OS memory layer scaffolded"},
		{"the customer had 12 prior refunds", "the customer had 0 prior refunds"},
	}
	for _, c := range cases {
		aTok, aNeg := contentTokensAndPolarity(c[0])
		bTok, bNeg := contentTokensAndPolarity(c[1])
		rel, ok := inferRelationshipWithContext(aTok, aNeg, bTok, bNeg, false)
		flagged := (ok && rel == "contradicts") ||
			detectNumericDivergence(c[0], c[1], aTok, bTok) ||
			detectTemporalDivergence(c[0], c[1], aTok, bTok)
		if !flagged {
			t.Errorf("genuine contradiction lost: %q vs %q", c[0], c[1])
		}
	}
}
