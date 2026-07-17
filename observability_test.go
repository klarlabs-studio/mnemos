package mnemos

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"go.klarlabs.de/bolt"
)

// TestConsolidate_EmitsOperationalLog verifies the ADR-0021 consolidation log: a pass
// emits one structured "consolidation pass" line through the wired logger.
func TestConsolidate_EmitsOperationalLog(t *testing.T) {
	var buf bytes.Buffer
	mem, err := New(WithStorage("memory://?namespace=obslog"), WithPassiveMode(),
		WithLogger(bolt.New(bolt.NewJSONHandler(&buf))))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = mem.Close() }()

	if _, err := mem.Consolidate(context.Background(), ConsolidateOptions{}); err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "mnemos: consolidation pass") {
		t.Errorf("consolidation should emit its operational log, got %q", out)
	}
	if !strings.Contains(out, `"level":"info"`) || !strings.Contains(out, `"credited":0`) {
		t.Errorf("consolidation log missing expected fields, got %q", out)
	}
}

// TestConsolidate_NilLoggerSafe verifies the nil-safe accessor: a memory with no logger
// consolidates without panicking (library consumers that never call WithLogger).
func TestConsolidate_NilLoggerSafe(t *testing.T) {
	mem, err := New(WithStorage("memory://?namespace=obsnil"), WithPassiveMode())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = mem.Close() }()
	if _, err := mem.Consolidate(context.Background(), ConsolidateOptions{}); err != nil {
		t.Fatalf("Consolidate with nil logger: %v", err)
	}
}
