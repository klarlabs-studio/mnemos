package main

import (
	"context"
	"fmt"

	mnemos "go.klarlabs.de/mnemos"
)

// handleBrainHealth runs the read-only brain-health verdict (ADR 0019): a single
// healthy/degraded/unhealthy rollup of the cognitive vitals (free-energy, calibration,
// dissonance, low-trust, staleness) and structural-integrity checks (orphan beliefs,
// dangling edges, stale-expectation backlog). `--journal` also records the snapshot to
// the cognitive journal so vital signs become a time series (read with `journal --kind health`).
//
//	mnemos health [--human] [--journal]
func handleBrainHealth(args []string, f Flags) {
	journal := false
	for _, a := range args {
		switch a {
		case "--journal":
			journal = true
		default:
			exitWithMnemosError(false, NewUserError("unknown argument %q", a))
			return
		}
	}
	ctx := context.Background()
	mem, err := newLibraryMemory(ctx, "")
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "open store"))
		return
	}
	defer func() { _ = mem.Close() }()

	var health mnemos.BrainHealth
	if journal {
		health, err = mem.SnapshotHealth(ctx)
	} else {
		health, err = mem.BrainHealth(ctx)
	}
	if err != nil {
		exitWithMnemosError(f.Verbose, err)
		return
	}

	if f.Human {
		printBrainHealthHuman(health)
		return
	}
	emitJSON(health)
}

func printBrainHealthHuman(h mnemos.BrainHealth) {
	fmt.Printf("Brain health: %s  (ADR 0019)\n\n", h.Status)
	fmt.Println("  vitals")
	for _, v := range h.Vitals {
		fmt.Printf("    %-8s %-12s %6.3f   %s\n", healthMark(v.Status), v.Name, v.Value, v.Detail)
	}
	fmt.Println("  integrity")
	for _, p := range h.Pathologies {
		fmt.Printf("    %-8s %-12s %6d   %s\n", healthMark(p.Status), p.Kind, p.Count, p.Detail)
	}
}

func healthMark(s mnemos.HealthStatus) string {
	switch s {
	case mnemos.HealthUnhealthy:
		return "[CRIT]"
	case mnemos.HealthDegraded:
		return "[warn]"
	default:
		return "[ ok ]"
	}
}
