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
