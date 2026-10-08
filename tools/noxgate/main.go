// Command noxgate decides the security gate from a nox scan: it fails when the
// scan reports a critical or high finding that .nox/baseline.json does not
// list. With -write it instead rewrites the baseline to exactly the current
// findings — dropping entries whose finding is gone, adding new ones — keeping
// each surviving entry's original record.
//
// It is run by scripts/nox-gate.sh, which pins the nox binary; fingerprints
// are only comparable when produced by the same nox version.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"
)

type finding struct {
	RuleID      string
	Severity    string
	Fingerprint string
	Message     string
	Location    struct {
		FilePath  string
		StartLine int
	}
}

type entry struct {
	Fingerprint string `json:"fingerprint"`
	RuleID      string `json:"rule_id"`
	FilePath    string `json:"file_path"`
	Severity    string `json:"severity"`
	Reason      string `json:"reason,omitempty"`
	Owner       string `json:"owner,omitempty"`
	CreatedAt   string `json:"created_at"`
}

type baseline struct {
	SchemaVersion string  `json:"schema_version"`
	Entries       []entry `json:"entries"`
}

func main() {
	findingsPath := flag.String("findings", "", "nox findings.json")
	baselinePath := flag.String("baseline", ".nox/baseline.json", "baseline file")
	write := flag.Bool("write", false, "rewrite the baseline to the current findings instead of gating")
	flag.Parse()
	if err := run(*findingsPath, *baselinePath, *write); err != nil {
		fmt.Fprintln(os.Stderr, "noxgate:", err)
		os.Exit(1)
	}
}

func run(findingsPath, baselinePath string, write bool) error {
	findings, err := readFindings(findingsPath)
	if err != nil {
		return err
	}
	base, err := readBaseline(baselinePath)
	if err != nil {
		return err
	}
	if write {
		return writeBaseline(baselinePath, rebase(base, findings, time.Now().UTC()))
	}
	blocking := netNewBlocking(base, findings)
	for _, f := range blocking {
		fmt.Fprintf(os.Stderr, "  %s %s %s:%d  %s\n", f.Severity, f.RuleID, f.Location.FilePath, f.Location.StartLine, f.Message)
	}
	if len(blocking) > 0 {
		return fmt.Errorf("%d net-new critical/high finding(s); fix them, or baseline a verified false positive and cover it in .nox/waivers.yaml", len(blocking))
	}
	fmt.Printf("noxgate: %d findings, 0 net-new critical/high\n", len(findings))
	return nil
}

// netNewBlocking is every critical or high finding the baseline does not list.
func netNewBlocking(base baseline, findings []finding) []finding {
	known := map[string]bool{}
	for _, e := range base.Entries {
		known[e.Fingerprint] = true
	}
	var out []finding
	for _, f := range findings {
		if (f.Severity == "critical" || f.Severity == "high") && !known[f.Fingerprint] {
			out = append(out, f)
		}
	}
	return out
}

// rebase returns a baseline holding one entry per current finding: the
// existing entry where there is one, a new entry stamped now otherwise.
func rebase(base baseline, findings []finding, now time.Time) baseline {
	old := map[string]entry{}
	for _, e := range base.Entries {
		old[e.Fingerprint] = e
	}
	seen := map[string]bool{}
	out := baseline{SchemaVersion: base.SchemaVersion}
	for _, f := range findings {
		if seen[f.Fingerprint] {
			continue
		}
		seen[f.Fingerprint] = true
		e, ok := old[f.Fingerprint]
		if !ok {
			e = entry{Fingerprint: f.Fingerprint, RuleID: f.RuleID, FilePath: f.Location.FilePath,
				Severity: f.Severity, CreatedAt: now.Format(time.RFC3339Nano)}
		}
		out.Entries = append(out.Entries, e)
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		a, b := out.Entries[i], out.Entries[j]
		if a.FilePath != b.FilePath {
			return a.FilePath < b.FilePath
		}
		if a.RuleID != b.RuleID {
			return a.RuleID < b.RuleID
		}
		return a.Fingerprint < b.Fingerprint
	})
	return out
}

func readFindings(path string) ([]finding, error) {
	data, err := os.ReadFile(path) //nolint:gosec // operator-supplied scan output path
	if err != nil {
		return nil, err
	}
	var doc struct {
		Findings []finding `json:"findings"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return doc.Findings, nil
}

func readBaseline(path string) (baseline, error) {
	var b baseline
	data, err := os.ReadFile(path) //nolint:gosec // operator-supplied baseline path
	if err != nil {
		return b, err
	}
	if err := json.Unmarshal(data, &b); err != nil {
		return b, fmt.Errorf("%s: %w", path, err)
	}
	return b, nil
}

func writeBaseline(path string, b baseline) error {
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}
