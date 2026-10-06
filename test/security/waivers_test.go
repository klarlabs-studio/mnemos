// Package security holds the guards on the security gate's own configuration.
package security

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// maxWaiverHorizon bounds how far out an expiry may be set, so "accepted" can
// not quietly mean "forever".
const maxWaiverHorizon = 366 * 24 * time.Hour

type waiver struct {
	ID      string   `yaml:"id"`
	Rules   []string `yaml:"rules"`
	Paths   []string `yaml:"paths"`
	Owner   string   `yaml:"owner"`
	Expires string   `yaml:"expires"`
	Reason  string   `yaml:"reason"`
}

type baselineEntry struct {
	Fingerprint string `json:"fingerprint"`
	RuleID      string `json:"rule_id"`
	FilePath    string `json:"file_path"`
	Severity    string `json:"severity"`
}

func repoFile(t *testing.T, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func loadWaivers(t *testing.T) []waiver {
	t.Helper()
	var doc struct {
		Waivers []waiver `yaml:"waivers"`
	}
	dec := yaml.NewDecoder(strings.NewReader(string(repoFile(t, ".nox/waivers.yaml"))))
	dec.KnownFields(true) // a misspelt key ("expiry:") must not read as "no expiry"
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf(".nox/waivers.yaml: %v", err)
	}
	if len(doc.Waivers) == 0 {
		t.Fatal(".nox/waivers.yaml has no waivers; the guard would check nothing")
	}
	return doc.Waivers
}

func loadBaseline(t *testing.T) []baselineEntry {
	t.Helper()
	var doc struct {
		Entries []baselineEntry `json:"entries"`
	}
	if err := json.Unmarshal(repoFile(t, ".nox/baseline.json"), &doc); err != nil {
		t.Fatalf(".nox/baseline.json: %v", err)
	}
	return doc.Entries
}

// Every suppressed finding has an owner, a reason and an expiry. The baseline
// alone recorded none of that: 143 of 144 entries had no reason, 47 no longer
// matched any finding, and nothing would ever ask whether one was still
// acceptable. Each entry must now be covered by a waiver in .nox/waivers.yaml,
// and a waiver fails this test the day it expires.
func TestNoxWaivers_EveryBaselineEntryIsOwnedAndCurrent(t *testing.T) {
	for _, p := range checkWaivers(loadWaivers(t), loadBaseline(t), time.Now().UTC()) {
		t.Error(p)
	}
}

// checkWaivers returns every way the register fails to account for the
// baseline at time now.
func checkWaivers(waivers []waiver, entries []baselineEntry, now time.Time) []string {
	var problems []string
	errorf := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	used := map[string]bool{}
	ids := map[string]bool{}
	for _, w := range waivers {
		if w.ID == "" || ids[w.ID] {
			errorf("waiver %q: id is empty or duplicated", w.ID)
		}
		ids[w.ID] = true
		if strings.TrimSpace(w.Owner) == "" {
			errorf("waiver %q has no owner", w.ID)
		}
		if len(strings.Fields(w.Reason)) < 8 {
			errorf("waiver %q: reason %q does not say why the finding is acceptable", w.ID, w.Reason)
		}
		if len(w.Rules) == 0 || len(w.Paths) == 0 {
			errorf("waiver %q must name its rules and paths", w.ID)
		}
		exp, err := time.Parse("2006-01-02", w.Expires)
		switch {
		case err != nil:
			errorf("waiver %q: expires %q is not a YYYY-MM-DD date", w.ID, w.Expires)
		case !now.Before(exp):
			errorf("waiver %q (owner %s) expired on %s: re-decide it — fix the findings, or renew it with a reason that is still true", w.ID, w.Owner, w.Expires)
		case exp.Sub(now) > maxWaiverHorizon:
			errorf("waiver %q expires %s, more than a year out", w.ID, w.Expires)
		}
	}

	seen := map[string]bool{}
	var uncovered []string
	for _, e := range entries {
		if seen[e.Fingerprint] {
			errorf("baseline lists fingerprint %s twice", e.Fingerprint)
		}
		seen[e.Fingerprint] = true
		covered := false
		for _, w := range waivers {
			if contains(w.Rules, e.RuleID) && contains(w.Paths, e.FilePath) {
				covered = true
				used[w.ID] = true
			}
		}
		if !covered {
			uncovered = append(uncovered, e.Severity+" "+e.RuleID+" "+e.FilePath)
		}
	}
	sort.Strings(uncovered)
	for _, u := range uncovered {
		errorf("baseline entry with no waiver: %s — say why it is acceptable in .nox/waivers.yaml, or fix it", u)
	}
	for _, w := range waivers {
		if !used[w.ID] {
			errorf("waiver %q covers no baseline entry; its finding is gone, so delete it", w.ID)
		}
	}
	return problems
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// The checks themselves, against synthetic input: each failure mode is
// reported, so a green run of the test above means something.
func TestNoxWaivers_CheckRejectsEachFailureMode(t *testing.T) {
	now := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	good := waiver{ID: "w", Rules: []string{"R"}, Paths: []string{"a.go"}, Owner: "@o",
		Expires: "2027-01-01", Reason: "a fixture credential that names no reachable server"}
	entry := baselineEntry{Fingerprint: "f1", RuleID: "R", FilePath: "a.go", Severity: "high"}

	mutate := func(f func(*waiver)) []waiver { w := good; f(&w); return []waiver{w} }
	cases := map[string]struct {
		waivers []waiver
		entries []baselineEntry
	}{
		"expired":          {mutate(func(w *waiver) { w.Expires = "2026-10-06" }), []baselineEntry{entry}},
		"too far out":      {mutate(func(w *waiver) { w.Expires = "2028-01-01" }), []baselineEntry{entry}},
		"no owner":         {mutate(func(w *waiver) { w.Owner = "" }), []baselineEntry{entry}},
		"no reason":        {mutate(func(w *waiver) { w.Reason = "fp" }), []baselineEntry{entry}},
		"uncovered entry":  {[]waiver{good}, []baselineEntry{entry, {Fingerprint: "f2", RuleID: "R", FilePath: "b.go"}}},
		"other rule":       {[]waiver{good}, []baselineEntry{{Fingerprint: "f1", RuleID: "Q", FilePath: "a.go"}}},
		"unused waiver":    {[]waiver{good, func() waiver { w := good; w.ID = "x"; w.Paths = []string{"z.go"}; return w }()}, []baselineEntry{entry}},
		"duplicate entry":  {[]waiver{good}, []baselineEntry{entry, entry}},
		"unparseable date": {mutate(func(w *waiver) { w.Expires = "soon" }), []baselineEntry{entry}},
	}
	for name, c := range cases {
		if len(checkWaivers(c.waivers, c.entries, now)) == 0 {
			t.Errorf("%s: not reported", name)
		}
	}
	if p := checkWaivers([]waiver{good}, []baselineEntry{entry}, now); len(p) != 0 {
		t.Errorf("a valid waiver covering its entry was rejected: %v", p)
	}
}
