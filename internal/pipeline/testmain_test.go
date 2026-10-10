package pipeline

import (
	"fmt"
	"os"
	"testing"
)

// TestMain points XDG_DATA_HOME at a throwaway directory. ExtractionCacheDir
// resolves under it and otherwise defaults to the developer's real
// ~/.local/share/mnemos, so extractor tests wrote their fixtures there.
func TestMain(m *testing.M) {
	dataHome, err := os.MkdirTemp("", "mnemos-pipeline-test-xdg-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "testmain: create isolated data dir: %v\n", err)
		os.Exit(1)
	}
	_ = os.Setenv("XDG_DATA_HOME", dataHome)
	code := m.Run()
	_ = os.RemoveAll(dataHome)
	os.Exit(code)
}
