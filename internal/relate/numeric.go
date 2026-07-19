package relate

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// numericValue is one number-with-optional-unit extracted from claim text.
// Two values match (i.e. don't contradict) when their normalized magnitude
// is equal up to a small relative tolerance.
type numericValue struct {
	raw   string  // exactly as it appeared, for debugging
	value float64 // the parsed magnitude, normalized to a base unit
	unit  string  // canonical unit name, "" when unitless
}

// numericTokenRE matches one numeric literal with optional currency
// prefix or unit suffix. Constraints:
//   - the number must not be preceded by a letter (so "p99" is skipped)
//   - the unit suffix, when present, ends at a word boundary so
//     "latency" doesn't get parsed as a 4-char unit "late"
//   - the percent sign is its own unit family
//   - currency symbols can lead the number ($4500) or trail as ISO
//     codes (4500 USD)
//
// Submatches: 1 = leading currency symbol (or empty),
// 2 = signed-magnitude string (commas allowed as thousands separators),
// 3 = trailing unit token (or empty).
var numericTokenRE = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_])([$€£¥])?(-?\d+(?:[.,]\d+)?)(%|\s*[a-z]{1,6})?`)

// unitFamily maps each recognized unit token to a (family, multiplier)
// pair. Two values from the same family compare via their normalized
// magnitudes; values from different families (or unknown units) are
// treated as different magnitudes regardless.
var unitFamily = map[string]struct {
	family string
	mult   float64
}{
	// Time
	"ms": {"time", 0.001}, "msec": {"time", 0.001},
	"s": {"time", 1}, "sec": {"time", 1}, "secs": {"time", 1}, "second": {"time", 1}, "seconds": {"time", 1},
	"m": {"time", 60}, "min": {"time", 60}, "mins": {"time", 60}, "minute": {"time", 60}, "minutes": {"time", 60},
	"h": {"time", 3600}, "hr": {"time", 3600}, "hour": {"time", 3600}, "hours": {"time", 3600},

	// Percent (its own family — a bare number is not the same as a percent)
	"%": {"percent", 1},

	// Currency: keep symbols apart (we don't FX-convert).
	"$":   {"usd", 1},
	"usd": {"usd", 1},
	"€":   {"eur", 1},
	"eur": {"eur", 1},
	"£":   {"gbp", 1},
	"gbp": {"gbp", 1},

	// Bytes
	"b":  {"bytes", 1},
	"kb": {"bytes", 1024}, "mb": {"bytes", 1024 * 1024},
	"gb": {"bytes", 1024 * 1024 * 1024}, "tb": {"bytes", 1024 * 1024 * 1024 * 1024},
}

// extractNumerics finds every numeric-with-optional-unit literal in the
// claim text. Returns at most a handful per claim — claims with dozens
// of numerics are usually data dumps where contradiction detection isn't
// useful anyway.
func extractNumerics(text string) []numericValue {
	matches := numericTokenRE.FindAllStringSubmatch(text, -1)
	out := make([]numericValue, 0, len(matches))
	for _, m := range matches {
		raw := m[0]
		currency := strings.ToLower(strings.TrimSpace(m[1]))
		numStr := strings.ReplaceAll(m[2], ",", "")
		v, err := strconv.ParseFloat(numStr, 64)
		if err != nil {
			continue
		}
		suffix := strings.ToLower(strings.TrimSpace(m[3]))
		// Currency prefix beats trailing unit; otherwise use the suffix.
		unitTok := suffix
		if currency != "" {
			unitTok = currency
		}
		family := ""
		mult := 1.0
		if uf, ok := unitFamily[unitTok]; ok {
			family = uf.family
			mult = uf.mult
		} else if unitTok != "" {
			// Unknown but non-empty unit — keep as a one-off family so
			// "12 widgets" and "0 widgets" still compare via the family
			// label even though we can't normalize across families.
			family = "raw:" + unitTok
		}
		out = append(out, numericValue{
			raw:   raw,
			value: v * mult,
			unit:  family,
		})
	}
	return out
}

// numericValuesAgree reports whether every value in a is matched by at
// least one value in b within the same unit family and a small relative
// tolerance — i.e. the two claims are not in numeric disagreement.
func numericValuesAgree(a, b []numericValue) bool {
	if len(a) == 0 || len(b) == 0 {
		return true
	}
	for _, av := range a {
		matched := false
		for _, bv := range b {
			if av.unit != bv.unit {
				continue
			}
			if floatsClose(av.value, bv.value) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

// floatsClose returns true when two magnitudes are equal up to a small
// relative tolerance. Strict equality is wrong (1.0 != 1.000001 from
// unit conversion) but generous tolerance hides real disagreements;
// 0.5% covers conversion rounding without swallowing meaningful diffs.
func floatsClose(a, b float64) bool {
	if a == b {
		return true
	}
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	mag := a
	if b > mag {
		mag = b
	}
	if mag < 0 {
		mag = -mag
	}
	if mag == 0 {
		return diff < 1e-9
	}
	return diff/mag < 0.005
}

// detectNumericDivergence returns true when two claims share substantial
// non-numeric content (so they're plausibly about the same thing) but
// disagree on at least one numeric value.
//
// Runs after the polarity / value-divergence paths so it only kicks in
// when those didn't already classify the pair.
func detectNumericDivergence(aText, bText string, aTokens, bTokens map[string]struct{}) bool {
	aNums := extractNumerics(aText)
	bNums := extractNumerics(bText)
	if len(aNums) == 0 || len(bNums) == 0 {
		return false
	}

	// Require non-numeric topical overlap so we don't flag every pair
	// of sentences that happen to contain different numbers.
	//
	// This has to be measured on WORD tokens only. The tokenizer keeps
	// numerals and bare symbols ("9", "269", "→") as content tokens, so
	// measuring overlap over the raw sets let the numbers themselves stand in
	// for topical agreement — the exact thing this guard exists to prevent.
	// "269 → 9" tokenizes to [9, 269, →]; against a compose-file claim
	// containing "9 → 6" it shared {9, →}, scoring 0.67 and clearing the 0.5
	// bar on nothing but a digit and an arrow.
	aWords := wordTokens(aTokens)
	bWords := wordTokens(bTokens)
	overlap := contentOverlap(aWords, bWords)
	if overlap < 1 {
		return false
	}
	shorter := min(len(aWords), len(bWords))
	if shorter == 0 {
		return false
	}
	ratio := float64(overlap) / float64(shorter)
	// Stricter than the supports-overlap threshold: numeric
	// disagreement is a strong claim, so pairs must share most of
	// their topical surface (excluding the numbers themselves).
	if ratio < 0.5 {
		return false
	}

	return !numericValuesAgree(aNums, bNums)
}

// wordTokens returns the subset of tokens that contain at least one letter.
//
// The tokenizer treats numerals and bare symbols as content tokens, which is
// right for most purposes but wrong when the question is "are these two claims
// about the same thing?". A shared "9" or "→" says nothing about topic, so any
// same-topic guard must measure agreement over words alone.
func wordTokens(tokens map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(tokens))
	for tok := range tokens {
		for _, r := range tok {
			if unicode.IsLetter(r) {
				out[tok] = struct{}{}
				break
			}
		}
	}
	return out
}
