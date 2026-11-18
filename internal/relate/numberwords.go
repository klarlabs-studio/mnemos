package relate

import (
	"regexp"
	"strings"
)

// Spelled-out cardinals ("one hundred", "twenty-five") are numbers the numeric
// detector could not see: it reads digits only, so "the rate limit is one
// hundred requests per minute" and "... one thousand requests per minute"
// shared every word but the number and never produced the edge that lets
// curation drop the superseded statement (brainbench stale_and_superseded,
// p-limit). spelledNumerics reads them alongside the digits.
//
// A lone "one" is left alone: it is far more often a determiner or pronoun
// ("one of the reviewers", "one approach") than a count, and reading it as 1
// would pair it against every "two" in a similar sentence. "one" still counts
// inside a longer number ("one hundred"). Ordinals ("tenth") are not counts and
// are not converted.

var numberUnits = map[string]int64{
	"zero": 0, "one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6,
	"seven": 7, "eight": 8, "nine": 9, "ten": 10, "eleven": 11, "twelve": 12,
	"thirteen": 13, "fourteen": 14, "fifteen": 15, "sixteen": 16,
	"seventeen": 17, "eighteen": 18, "nineteen": 19,
	"twenty": 20, "thirty": 30, "forty": 40, "fifty": 50, "sixty": 60,
	"seventy": 70, "eighty": 80, "ninety": 90,
}

var numberScales = map[string]int64{
	"thousand": 1_000, "million": 1_000_000, "billion": 1_000_000_000,
}

// numberWordRE matches one word, keeping a hyphenated compound ("twenty-five")
// together.
var numberWordRE = regexp.MustCompile(`[A-Za-z]+(?:-[A-Za-z]+)?`)

// numberWordValue parses one word of a spelled cardinal. kind is "unit" (0–99,
// including hyphenated tens), "hundred" or "scale"; ok is false for any other
// word.
func numberWordValue(w string) (value int64, kind string, ok bool) {
	w = strings.ToLower(w)
	if v, ok := numberUnits[w]; ok {
		return v, "unit", true
	}
	if w == "hundred" {
		return 100, "hundred", true
	}
	if v, ok := numberScales[w]; ok {
		return v, "scale", true
	}
	if tens, ones, found := strings.Cut(w, "-"); found {
		t, okT := numberUnits[tens]
		o, okO := numberUnits[ones]
		if okT && okO && t >= 20 && t%10 == 0 && o > 0 && o < 10 {
			return t + o, "unit", true
		}
	}
	return 0, "", false
}

// spelledNumerics returns the spelled cardinals in text as numeric values.
//
// A spelled number is almost always a count of the noun that follows it ("seven
// services", "zero restarts"), so that noun is its unit: "raw:<noun>", the same
// family the digit path gives "12 widgets", or a known unit's own family
// ("ten minutes" is time). Without it, every spelled count compared against
// every other: on a real brain, "all seven services deployed, zero restarts"
// contradicted "it will be deployed to all seven services". A count whose noun
// does not follow it directly ("five of the six", "all three are") is elided
// and ambiguous, so it yields nothing.
func spelledNumerics(text string) []numericValue {
	locs := numberWordRE.FindAllStringIndex(text, -1)
	word := func(k int) string { return strings.ToLower(text[locs[k][0]:locs[k][1]]) }
	var out []numericValue
	for i := 0; i < len(locs); {
		start := i
		end, value, words := parseSpelledNumber(text, locs, i)
		if words == 0 {
			i++
			continue
		}
		i = end
		if words == 1 && word(start) == "one" {
			continue
		}
		// The denominator of a partitive ("five of the six fail") counts a
		// noun stated elsewhere, and the word after it is not that noun.
		if partitiveDenominator(word, start) {
			continue
		}
		if end >= len(locs) || !onlySpace(text[locs[end-1][1]:locs[end][0]]) {
			continue
		}
		noun := word(end)
		if _, stop := stopWords[noun]; stop {
			continue
		}
		if _, _, isNumber := numberWordValue(noun); isNumber {
			continue // a list ("three four five"), not a count
		}
		family := "raw:" + noun
		mult := 1.0
		if uf, ok := unitFamily[noun]; ok {
			family, mult = uf.family, uf.mult
		}
		out = append(out, numericValue{
			raw:     text[locs[start][0]:locs[end][1]],
			value:   float64(value) * mult,
			unit:    family,
			counted: true,
		})
	}
	return out
}

// partitiveDenominator reports whether the number starting at word k follows
// "of" or "of the".
func partitiveDenominator(word func(int) string, k int) bool {
	switch {
	case k >= 1 && word(k-1) == "of":
		return true
	case k >= 2 && word(k-1) == "the" && word(k-2) == "of":
		return true
	}
	return false
}

// parseSpelledNumber reads the longest spelled cardinal starting at word i.
// Words must be separated by whitespace only, and "and" may join a hundred to
// what follows ("two hundred and fifty"). It returns the index after the last
// word consumed, the value and the number of number words read (0 when word i
// does not start a number).
func parseSpelledNumber(text string, locs [][]int, i int) (end int, value int64, words int) {
	var total, current, lastUnit int64
	sawUnit := false
	end = i
	for j := i; j < len(locs); j++ {
		w := text[locs[j][0]:locs[j][1]]
		if j > i && !onlySpace(text[locs[j-1][1]:locs[j][0]]) {
			break
		}
		if strings.EqualFold(w, "and") && words > 0 && j+1 < len(locs) {
			if _, kind, ok := numberWordValue(text[locs[j+1][0]:locs[j+1][1]]); ok && kind == "unit" &&
				onlySpace(text[locs[j][1]:locs[j+1][0]]) {
				continue
			}
			break
		}
		v, kind, ok := numberWordValue(w)
		if !ok {
			break
		}
		switch kind {
		case "unit":
			// "twenty five" is one number; "three four" is a list, so stop
			// before its second unit.
			if sawUnit && (lastUnit < 20 || lastUnit%10 != 0 || v == 0 || v > 9) {
				return end, total + current, words
			}
			current += v
			lastUnit = v
			sawUnit = true
		case "hundred":
			if current == 0 {
				current = 1
			}
			current *= 100
			sawUnit = false
		case "scale":
			if current == 0 {
				current = 1
			}
			total += current * v
			current = 0
			sawUnit = false
		}
		words++
		end = j + 1
	}
	return end, total + current, words
}

func onlySpace(s string) bool {
	return strings.TrimSpace(s) == "" && !strings.ContainsAny(s, "\n\r")
}
