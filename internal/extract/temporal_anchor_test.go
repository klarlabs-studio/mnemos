package extract

import (
	"strings"
	"testing"
	"time"
)

func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 13, 56, 0, 0, time.UTC)
}

// Each case is a belief as the extractor wrote it, the time it was said, and
// the annotation it must carry. The references are LoCoMo session dates.
func TestAnchorRelativeDates_ResolvesAgainstTheTimeItWasSaid(t *testing.T) {
	cases := []struct {
		name string
		text string
		ref  time.Time
		want string
	}{
		{"yesterday", "Melanie signed up for a pottery class yesterday", day(2023, time.July, 3),
			"Melanie signed up for a pottery class yesterday (2 July 2023)"},
		{"days ago in words", "Caroline went to an LGBTQ conference two days ago", day(2023, time.July, 12),
			"Caroline went to an LGBTQ conference two days ago (10 July 2023)"},
		{"days ago in digits", "Deborah's dad passed away 2 days ago", day(2023, time.January, 27),
			"Deborah's dad passed away 2 days ago (25 January 2023)"},
		{"day before yesterday", "Jolene bought a new aquarium the day before yesterday", day(2023, time.June, 26),
			"Jolene bought a new aquarium the day before yesterday (24 June 2023)"},
		{"abbreviated weekday", "Last Fri I took my kids to a pottery workshop", day(2023, time.July, 15),
			"Last Fri (14 July 2023) I took my kids to a pottery workshop"},
		{"weekday said on that weekday is the week before", "Dave had a card night last Friday", day(2023, time.August, 25),
			"Dave had a card night last Friday (18 August 2023)"},
		{"last week", "Caroline had a picnic last week", day(2023, time.July, 6),
			"Caroline had a picnic last week (the week before 6 July 2023)"},
		{"last weekend is not last week", "Maria went hiking last weekend", day(2023, time.July, 22),
			"Maria went hiking last weekend (the weekend before 22 July 2023)"},
		{"weeks ago", "Jon opened the studio three weeks ago", day(2023, time.July, 22),
			"Jon opened the studio three weeks ago (around 1 July 2023)"},
		{"this month", "Gina lost her job at Door Dash this month", day(2023, time.January, 20),
			"Gina lost her job at Door Dash this month (January 2023)"},
		{"last month", "Evan went skiing in Banff last month", day(2023, time.August, 19),
			"Evan went skiing in Banff last month (July 2023)"},
		{"last month across a year", "John went to a convention last month", day(2023, time.January, 18),
			"John went to a convention last month (December 2022)"},
		{"months ago", "Tim started the violin four months ago", day(2023, time.December, 6),
			"Tim started the violin four months ago (around August 2023)"},
		{"last year", "James visited Italy last year", day(2022, time.April, 20),
			"James visited Italy last year (2021)"},
		{"month last year", "Calvin met Frank Ocean at a festival in August last year", day(2023, time.October, 19),
			"Calvin met Frank Ocean at a festival in August last year (August 2022)"},
		{"years ago", "John started surfing five years ago", day(2023, time.July, 16),
			"John started surfing five years ago (around 2018)"},
		{"a few years ago", "Deborah's mother passed away a few years ago", day(2023, time.January, 23),
			"Deborah's mother passed away a few years ago (a few years before 2023)"},
		{"day of this month", "Calvin's album dropped on the 11th", day(2023, time.September, 13),
			"Calvin's album dropped on the 11th (11 September 2023)"},
		{"day of last month", "The roof was fixed on the 28th", day(2023, time.August, 5),
			"The roof was fixed on the 28th (28 July 2023)"},
		{"tomorrow", "The move happens tomorrow", day(2023, time.March, 31),
			"The move happens tomorrow (1 April 2023)"},
		{"next week", "The audit starts next week", day(2023, time.March, 1),
			"The audit starts next week (the week after 1 March 2023)"},
		{"today", "Caroline got the call today", day(2023, time.March, 1),
			"Caroline got the call today (1 March 2023)"},
		{"two expressions", "Jon lost his job yesterday and Gina lost hers last month", day(2023, time.January, 20),
			"Jon lost his job yesterday (19 January 2023) and Gina lost hers last month (December 2022)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AnchorRelativeDates(tc.text, tc.ref); got != tc.want {
				t.Errorf("AnchorRelativeDates(%q)\n got  %q\n want %q", tc.text, got, tc.want)
			}
		})
	}
}

// Wording without a relative time expression is returned as written, so the
// rewrite can never invent a date.
func TestAnchorRelativeDates_LeavesOtherWordingAlone(t *testing.T) {
	ref := day(2023, time.July, 6)
	for _, text := range []string{
		"Caroline went to an LGBTQ support group on 7 May 2023",
		"At last the team shipped the release",
		"This was the last time Melanie painted",
		"Joanna has known these friends for years",
		"The yesterday-cache was rebuilt", // a compound, not the adverb
		"Nate won his first tournament the other day",
		"The 11th edition of the guide is out",
	} {
		if got := AnchorRelativeDates(text, ref); got != text {
			t.Errorf("AnchorRelativeDates(%q) = %q, want it unchanged", text, got)
		}
	}
}

// A zero reference time means the caller does not know when the text was
// said, so nothing is resolved.
func TestAnchorRelativeDates_ZeroReferenceIsANoOp(t *testing.T) {
	text := "Melanie signed up for a pottery class yesterday"
	if got := AnchorRelativeDates(text, time.Time{}); got != text {
		t.Errorf("got %q, want the text unchanged", got)
	}
}

// Anchoring twice must not stack annotations: a belief re-extracted or folded
// onto an existing one keeps a single date.
func TestAnchorRelativeDates_IsIdempotent(t *testing.T) {
	ref := day(2023, time.July, 15)
	once := AnchorRelativeDates("Last Fri I went to a pottery workshop, and last week a fair", ref)
	if twice := AnchorRelativeDates(once, ref); twice != once {
		t.Errorf("second pass changed the text:\n once  %q\n twice %q", once, twice)
	}
	if strings.Count(once, "(") != 2 {
		t.Errorf("got %q, want exactly two annotations", once)
	}
}

// An impossible day of the month (the 31st, said in a month whose previous
// month has 30 days) is left unresolved rather than rolled into another month.
func TestAnchorRelativeDates_ImpossibleDayIsLeftAlone(t *testing.T) {
	text := "The invoice was paid on the 31st"
	if got := AnchorRelativeDates(text, day(2023, time.May, 10)); got != text {
		t.Errorf("got %q, want it unchanged (30 April has no 31st)", got)
	}
}
