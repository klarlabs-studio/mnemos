// Command capdoc regenerates docs/reference/capabilities.md from the capability
// registry (internal/capability).
//
//	go run ./tools/capdoc            # write docs/reference/capabilities.md
//	go run ./tools/capdoc -o -       # print to stdout
//
// TestRender_CommittedReferenceIsCurrent fails when the committed file differs
// from what this would write, so the reference cannot drift from the registry.
package main

import (
	"flag"
	"fmt"
	"os"

	"go.klarlabs.de/mnemos/internal/capability"
)

func main() {
	out := flag.String("o", "docs/reference/capabilities.md", `output path ("-" for stdout)`)
	flag.Parse()
	if err := capability.Validate(capability.Registry); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	doc := capability.Render(capability.Registry)
	if *out == "-" {
		fmt.Print(doc)
		return
	}
	if err := os.WriteFile(*out, []byte(doc), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "capdoc:", err)
		os.Exit(1)
	}
}
