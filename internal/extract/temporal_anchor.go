package extract

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// AnchorRelativeDates resolves the relative time expressions in a belief
// ("yesterday", "last Friday", "two days ago", "last month") against ref, the
// time the source was said or written, and writes the resolved date in
// parentheses right after each expression:
//
//	"Melanie signed up for a pottery class yesterday"  (said 3 July 2023)
//	"Melanie signed up for a pottery class yesterday (2 July 2023)"
//
// A belief is read long after it was said. Extraction keeps the speaker's
// wording, so "yesterday" survives into the belief and means nothing without
// the date it was said on. The original wording is kept; only the date is
// added, so the rewrite never removes what the source said.
//
// Resolution is deterministic and conservative. Expressions that name a day
// resolve to that day; ones that name a period resolve to the period
// ("the week before 6 July 2023", "July 2023"); approximate ones say so
// ("around 2018"). Vague ones ("the other day", "recently") and bare weekdays
// that could point forward ("on Friday") are left alone. A zero ref returns
// the text unchanged, and a second pass adds nothing.
func AnchorRelativeDates(text string, ref time.Time) string {
	if ref.IsZero() || strings.TrimSpace(text) == "" {
		return text
	}
	type span struct {
		start, end int
		note       string
	}
	var spans []span
	taken := func(s, e int) bool {
		for _, sp := range spans {
			if s < sp.end && sp.start < e {
				return true
			}
		}
		return false
	}
	// Rules run in order and the first to claim a stretch of text keeps it,
	// so "the day before yesterday" wins over "yesterday" and "August last
	// year" over "last year".
	for _, r := range anchorRules {
		for _, m := range r.re.FindAllStringSubmatchIndex(text, -1) {
			s, e := m[0], m[1]
			// A hyphen after the match makes it part of a compound
			// ("yesterday-cache"), not the time expression.
			if taken(s, e) || alreadyAnchored(text[e:]) || strings.HasPrefix(text[e:], "-") {
				continue
			}
			groups := make([]string, len(m)/2)
			for i := range groups {
				if m[2*i] >= 0 {
					groups[i] = text[m[2*i]:m[2*i+1]]
				}
			}
			if note, ok := r.resolve(groups, ref); ok {
				spans = append(spans, span{s, e, note})
			}
		}
	}
	if len(spans) == 0 {
		return text
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	var b strings.Builder
	last := 0
	for _, sp := range spans {
		b.WriteString(text[last:sp.end])
		b.WriteString(" (")
		b.WriteString(sp.note)
		b.WriteString(")")
		last = sp.end
	}
	b.WriteString(text[last:])
	return b.String()
}

// alreadyAnchored reports whether the text after an expression already holds
// a resolved date, so a second pass does not stack another one.
func alreadyAnchored(rest string) bool {
	return anchoredNote.MatchString(rest)
}

var anchoredNote = regexp.MustCompile(`^ \((?:the |around |a few |\d)`)

type anchorRule struct {
	re      *regexp.Regexp
	resolve func(groups []string, ref time.Time) (string, bool)
}

const (
	numberWord = `(\d{1,2}|a|an|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|a couple of)`
	weekday    = `(monday|mon|tuesday|tues|tue|wednesday|wed|thursday|thurs|thur|thu|friday|fri|saturday|sat|sunday|sun)`
	monthName  = `(january|february|march|april|may|june|july|august|september|october|november|december)`
)

func rule(pattern string, resolve func([]string, time.Time) (string, bool)) anchorRule {
	return anchorRule{re: regexp.MustCompile(`(?i)\b(?:` + pattern + `)\b`), resolve: resolve}
}

// anchorRules are tried in order; see AnchorRelativeDates.
var anchorRules = []anchorRule{
	rule(`the day before yesterday`, func(_ []string, ref time.Time) (string, bool) {
		return dayNote(ref.AddDate(0, 0, -2)), true
	}),
	rule(`the day after tomorrow`, func(_ []string, ref time.Time) (string, bool) {
		return dayNote(ref.AddDate(0, 0, 2)), true
	}),
	rule(monthName+` last year`, func(g []string, ref time.Time) (string, bool) {
		return capitalize(strings.ToLower(g[1])) + " " + strconv.Itoa(ref.Year()-1), true
	}),
	rule(`a few years ago|a couple of years ago|several years ago`, func(_ []string, ref time.Time) (string, bool) {
		return "a few years before " + strconv.Itoa(ref.Year()), true
	}),
	rule(numberWord+` days? ago`, func(g []string, ref time.Time) (string, bool) {
		n, ok := count(g[1])
		if !ok {
			return "", false
		}
		return dayNote(ref.AddDate(0, 0, -n)), true
	}),
	rule(numberWord+` weeks? ago`, func(g []string, ref time.Time) (string, bool) {
		n, ok := count(g[1])
		if !ok {
			return "", false
		}
		return "around " + dayNote(ref.AddDate(0, 0, -7*n)), true
	}),
	rule(numberWord+` months? ago`, func(g []string, ref time.Time) (string, bool) {
		n, ok := count(g[1])
		if !ok {
			return "", false
		}
		return "around " + monthNote(addMonths(ref, -n)), true
	}),
	rule(numberWord+` years? ago`, func(g []string, ref time.Time) (string, bool) {
		n, ok := count(g[1])
		if !ok {
			return "", false
		}
		return "around " + strconv.Itoa(ref.Year()-n), true
	}),
	rule(`yesterday`, func(_ []string, ref time.Time) (string, bool) {
		return dayNote(ref.AddDate(0, 0, -1)), true
	}),
	rule(`tomorrow`, func(_ []string, ref time.Time) (string, bool) {
		return dayNote(ref.AddDate(0, 0, 1)), true
	}),
	rule(`today|tonight|this morning|this afternoon|this evening`, func(_ []string, ref time.Time) (string, bool) {
		return dayNote(ref), true
	}),
	rule(`(?:last|this past) `+weekday, func(g []string, ref time.Time) (string, bool) {
		wd, ok := weekdays[strings.ToLower(g[1])]
		if !ok {
			return "", false
		}
		return dayNote(previousWeekday(ref, wd)), true
	}),
	rule(`(?:last|this past) weekend`, func(_ []string, ref time.Time) (string, bool) {
		return "the weekend before " + dayNote(ref), true
	}),
	rule(`next weekend`, func(_ []string, ref time.Time) (string, bool) {
		return "the weekend after " + dayNote(ref), true
	}),
	rule(`(?:last|this past) week`, func(_ []string, ref time.Time) (string, bool) {
		return "the week before " + dayNote(ref), true
	}),
	rule(`next week`, func(_ []string, ref time.Time) (string, bool) {
		return "the week after " + dayNote(ref), true
	}),
	rule(`this week`, func(_ []string, ref time.Time) (string, bool) {
		return "the week of " + dayNote(ref), true
	}),
	rule(`last month`, func(_ []string, ref time.Time) (string, bool) {
		return monthNote(addMonths(ref, -1)), true
	}),
	rule(`this month`, func(_ []string, ref time.Time) (string, bool) {
		return monthNote(ref), true
	}),
	rule(`next month`, func(_ []string, ref time.Time) (string, bool) {
		return monthNote(addMonths(ref, 1)), true
	}),
	rule(`last year`, func(_ []string, ref time.Time) (string, bool) {
		return strconv.Itoa(ref.Year() - 1), true
	}),
	rule(`this year`, func(_ []string, ref time.Time) (string, bool) {
		return strconv.Itoa(ref.Year()), true
	}),
	rule(`next year`, func(_ []string, ref time.Time) (string, bool) {
		return strconv.Itoa(ref.Year() + 1), true
	}),
	// "on the 17th": the most recent 17th, this month or the one before.
	rule(`on the (\d{1,2})(?:st|nd|rd|th)`, func(g []string, ref time.Time) (string, bool) {
		d, err := strconv.Atoi(g[1])
		if err != nil || d < 1 || d > 31 {
			return "", false
		}
		y, m := ref.Year(), ref.Month()
		if d > ref.Day() {
			prev := addMonths(ref, -1)
			y, m = prev.Year(), prev.Month()
		}
		t := time.Date(y, m, d, 0, 0, 0, 0, ref.Location())
		if t.Month() != m { // the 31st of a 30-day month rolled over
			return "", false
		}
		return dayNote(t), true
	}),
}

var weekdays = map[string]time.Weekday{
	"monday": time.Monday, "mon": time.Monday,
	"tuesday": time.Tuesday, "tues": time.Tuesday, "tue": time.Tuesday,
	"wednesday": time.Wednesday, "wed": time.Wednesday,
	"thursday": time.Thursday, "thurs": time.Thursday, "thur": time.Thursday, "thu": time.Thursday,
	"friday": time.Friday, "fri": time.Friday,
	"saturday": time.Saturday, "sat": time.Saturday,
	"sunday": time.Sunday, "sun": time.Sunday,
}

var counts = map[string]int{
	"a": 1, "an": 1, "one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6,
	"seven": 7, "eight": 8, "nine": 9, "ten": 10, "eleven": 11, "twelve": 12, "a couple of": 2,
}

func count(word string) (int, bool) {
	w := strings.ToLower(word)
	if n, ok := counts[w]; ok {
		return n, true
	}
	n, err := strconv.Atoi(w)
	return n, err == nil && n > 0
}

// previousWeekday is the most recent wd strictly before ref: "last Friday"
// said on a Friday is the Friday a week earlier.
func previousWeekday(ref time.Time, wd time.Weekday) time.Time {
	back := (int(ref.Weekday()) - int(wd) + 7) % 7
	if back == 0 {
		back = 7
	}
	return ref.AddDate(0, 0, -back)
}

// addMonths moves by whole calendar months from the first of ref's month, so
// 31 March minus one month is February, not 3 March.
func addMonths(ref time.Time, n int) time.Time {
	return time.Date(ref.Year(), ref.Month(), 1, 0, 0, 0, 0, ref.Location()).AddDate(0, n, 0)
}

func dayNote(t time.Time) string   { return t.Format("2 January 2006") }
func monthNote(t time.Time) string { return t.Format("January 2006") }

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
