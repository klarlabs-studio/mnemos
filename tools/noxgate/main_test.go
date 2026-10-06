package main

import (
	"testing"
	"time"
)

func f(fp, sev string) finding {
	x := finding{RuleID: "R", Severity: sev, Fingerprint: fp}
	x.Location.FilePath = "a.go"
	return x
}

// Only critical and high block, and only when the baseline does not list them.
func TestNetNewBlocking(t *testing.T) {
	base := baseline{Entries: []entry{{Fingerprint: "known-crit"}}}
	got := netNewBlocking(base, []finding{
		f("known-crit", "critical"), f("new-crit", "critical"), f("new-high", "high"),
		f("new-medium", "medium"), f("new-low", "low"),
	})
	if len(got) != 2 || got[0].Fingerprint != "new-crit" || got[1].Fingerprint != "new-high" {
		t.Fatalf("blocking = %+v, want new-crit and new-high only", got)
	}
}

// A rebase keeps surviving entries as recorded, drops the stale, adds the new
// once each, and is ordered so re-running it is a no-op diff.
func TestRebase(t *testing.T) {
	now := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	base := baseline{SchemaVersion: "1.0.0", Entries: []entry{
		{Fingerprint: "kept", RuleID: "R", FilePath: "a.go", Reason: "kept reason", CreatedAt: "2026-01-01T00:00:00Z"},
		{Fingerprint: "stale", RuleID: "R", FilePath: "a.go"},
	}}
	out := rebase(base, []finding{f("new", "high"), f("kept", "high"), f("new", "high")}, now)
	if out.SchemaVersion != "1.0.0" || len(out.Entries) != 2 {
		t.Fatalf("rebase = %+v", out)
	}
	byFP := map[string]entry{}
	for _, e := range out.Entries {
		byFP[e.Fingerprint] = e
	}
	if byFP["kept"].Reason != "kept reason" || byFP["kept"].CreatedAt != "2026-01-01T00:00:00Z" {
		t.Errorf("surviving entry rewritten: %+v", byFP["kept"])
	}
	if _, ok := byFP["stale"]; ok {
		t.Error("stale entry survived")
	}
	if e := byFP["new"]; e.RuleID != "R" || e.FilePath != "a.go" || e.Severity != "high" || e.CreatedAt == "" {
		t.Errorf("new entry incomplete: %+v", e)
	}
	again := rebase(out, []finding{f("kept", "high"), f("new", "high")}, now.Add(time.Hour))
	for i := range out.Entries {
		if again.Entries[i] != out.Entries[i] {
			t.Fatalf("rebase is not stable: %+v vs %+v", again.Entries, out.Entries)
		}
	}
}
