// Command configdoc regenerates docs/reference/configuration.md from
// internal/config: every setting's environment variable, mnemos.yaml key and
// description, read from the Config struct itself.
//
//	go run ./tools/configdoc            # write docs/reference/configuration.md
//	go run ./tools/configdoc -o -       # print to stdout
//
// TestReference_CommittedFileIsCurrent fails when the committed file differs
// from what this would write.
package main

import (
	"flag"
	"fmt"
	"os"

	"go.klarlabs.de/mnemos/internal/config"
)

func main() {
	out := flag.String("o", "docs/reference/configuration.md", `output path ("-" for stdout)`)
	flag.Parse()
	doc, err := config.Reference()
	if err != nil {
		fmt.Fprintln(os.Stderr, "configdoc:", err)
		os.Exit(1)
	}
	if *out == "-" {
		fmt.Print(doc)
		return
	}
	if err := os.WriteFile(*out, []byte(doc), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "configdoc:", err)
		os.Exit(1)
	}
}
