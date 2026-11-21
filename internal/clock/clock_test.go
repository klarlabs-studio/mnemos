package clock

import (
	"testing"
	"time"
)

func TestNow_IsWallClockWithoutAnOverride(t *testing.T) {
	before := time.Now()
	got := Now()
	if got.Before(before) || got.After(time.Now()) {
		t.Fatalf("Now() = %v, want a wall-clock instant", got)
	}
}

func TestInstall_RoutesNowAndRestores(t *testing.T) {
	fixed := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	restore := Install(func() time.Time { return fixed })
	if got := Now(); !got.Equal(fixed) {
		restore()
		t.Fatalf("Now() under install = %v, want %v", got, fixed)
	}
	restore()
	restore() // idempotent: a second restore must not unlock twice
	if Now().Equal(fixed) {
		t.Fatal("Now() still returns the installed clock after restore")
	}
}

func TestSimulated_IsStrictlyIncreasingAndReproducible(t *testing.T) {
	start := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	read := func() []time.Time {
		s := NewSimulated(start, time.Microsecond)
		out := []time.Time{s.Now(), s.Now()}
		s.Advance(time.Second)
		out = append(out, s.Now())
		if s.AdvanceTo(start) {
			t.Fatal("AdvanceTo moved the clock backwards")
		}
		if !s.AdvanceTo(start.Add(time.Hour)) {
			t.Fatal("AdvanceTo refused a later instant")
		}
		return append(out, s.Now())
	}
	a, b := read(), read()
	want := []time.Time{
		start,
		start.Add(time.Microsecond),
		start.Add(2*time.Microsecond + time.Second),
		start.Add(time.Hour),
	}
	for i := range want {
		if !a[i].Equal(want[i]) || !b[i].Equal(want[i]) {
			t.Fatalf("read %d = %v / %v, want %v", i, a[i], b[i], want[i])
		}
	}
}
