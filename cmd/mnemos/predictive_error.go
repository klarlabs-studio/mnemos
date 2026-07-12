package main

import (
	"context"
	"fmt"

	mnemos "go.klarlabs.de/mnemos"
)

// handlePredictiveError runs the read-only hierarchical prediction-error surface
// (ADR 0017): it reports the brain's prediction error at each level of the memory
// hierarchy plus a single aggregate (the free-energy proxy) and the hotspot level.
// Writes nothing.
func handlePredictiveError(args []string, f Flags) {
	for _, a := range args {
		exitWithMnemosError(false, NewUserError("unknown argument %q", a))
		return
	}
	ctx := context.Background()
	mem, err := newLibraryMemory(ctx, "")
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "open store"))
		return
	}
	defer func() { _ = mem.Close() }()

	pe, err := mem.PredictiveError(ctx)
	if err != nil {
		exitWithMnemosError(f.Verbose, err)
		return
	}

	if f.Human {
		printPredictiveErrorHuman(pe)
		return
	}
	emitJSON(pe)
}

func printPredictiveErrorHuman(pe mnemos.PredictiveError) {
	fmt.Println("Prediction error — where the model is most wrong (ADR 0017)")
	fmt.Println()
	for _, l := range pe.Levels {
		if l.Samples == 0 {
			fmt.Printf("  %-12s     —   (%s)\n", l.Level, l.Basis)
			continue
		}
		marker := "  "
		if l.Level == pe.Hotspot {
			marker = "→ " // the hotspot
		}
		fmt.Printf("%s%-12s  %5.3f   (%s)\n", marker, l.Level, l.Error, l.Basis)
	}
	fmt.Println()
	if pe.Hotspot == "" {
		fmt.Println("  total          —   (no level has data yet)")
		return
	}
	fmt.Printf("  total         %5.3f   — most wrong at: %s\n", pe.Total, pe.Hotspot)
}
