package main

import (
	"testing"
	"time"
)

// Both benchmarks' session date formats parse to the instant the session took
// place, so Remember can resolve "yesterday" against it.
func TestSessionTime_ParsesBenchmarkDates(t *testing.T) {
	cases := []struct {
		in   string
		want time.Time
	}{
		{"1:56 pm on 8 May, 2023", time.Date(2023, time.May, 8, 13, 56, 0, 0, time.UTC)},
		{"12:24 am on 24 August, 2023", time.Date(2023, time.August, 24, 0, 24, 0, 0, time.UTC)},
		{"10:31 am on 13 October, 2023", time.Date(2023, time.October, 13, 10, 31, 0, 0, time.UTC)},
		{"2023/05/20 (Sat) 02:21", time.Date(2023, time.May, 20, 2, 21, 0, 0, time.UTC)},
	}
	for _, tc := range cases {
		got, ok := sessionTime(tc.in)
		if !ok || !got.Equal(tc.want) {
			t.Errorf("sessionTime(%q) = %v, %v; want %v", tc.in, got, ok, tc.want)
		}
	}
}

// An unknown or empty date is not guessed: Remember then gets no At.
func TestSessionTime_UnknownFormatIsNotGuessed(t *testing.T) {
	for _, in := range []string{"", "sometime in May", "8 May"} {
		if got, ok := sessionTime(in); ok || !got.IsZero() {
			t.Errorf("sessionTime(%q) = %v, %v; want zero, false", in, got, ok)
		}
	}
}
