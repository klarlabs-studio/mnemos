package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos"
)

// TestHandleConsolidate_RunsAndEmitsJSON verifies the consolidate CLI wires
// flag-parse → library Memory → Consolidate → JSON output against a real store.
func TestHandleConsolidate_RunsAndEmitsJSON(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "consolidate.db")
	t.Setenv("MNEMOS_DB_URL", "sqlite://"+dbPath)
	// Passive mode: no LLM/embedder needed for the deterministic sleep pass.
	t.Setenv("MNEMOS_LLM_PROVIDER", "")
	t.Setenv("MNEMOS_EMBED_PROVIDER", "")

	// Seed one claim so the pass has something to scan.
	seedMem, err := mnemos.New(mnemos.WithStorage("sqlite://"+dbPath), mnemos.WithPassiveMode())
	if err != nil {
		t.Fatalf("seed New: %v", err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	if err := seedMem.RememberEvent(ctx, mnemos.Event{ID: "ev1", At: now, Type: "observation", Content: "the api runs in eu-west-1"}); err != nil {
		t.Fatalf("RememberEvent: %v", err)
	}
	if _, err := seedMem.RememberClaim(ctx, mnemos.ClaimItem{Text: "the api runs in eu-west-1", EventIDs: []string{"ev1"}, ValidFrom: now}); err != nil {
		t.Fatalf("RememberClaim: %v", err)
	}
	_ = seedMem.Close()

	// Capture stdout (emitJSON writes there).
	orig := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	handleConsolidate([]string{"--forget-refuted", "--reinforce-validated"}, Flags{})
	_ = w.Close()
	os.Stdout = orig
	out, _ := io.ReadAll(r)

	if !strings.Contains(string(out), "claims_scanned") {
		t.Fatalf("expected consolidate JSON with claims_scanned; got: %s", out)
	}
	var res map[string]any
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if _, ok := res["trust_refreshed"]; !ok {
		t.Errorf("expected trust_refreshed key in output; got %v", res)
	}
}
