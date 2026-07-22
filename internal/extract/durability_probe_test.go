package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"go.klarlabs.de/mnemos/internal/llm"
)

// TestDurabilityProbe is a measurement harness, not a gate: it runs the REAL
// prompt over a sample of claim texts and writes the verdicts alongside it, so
// a prompt change can be SCORED against hand labels instead of guessed at.
// Skipped unless DURABILITY_SAMPLE points at a JSON array of strings.
//
// It exists because a prompt rewrite that looked like a large improvement
// (48% -> 89% durable precision) turned out to be overfitting to the sample it
// was written against; on held-out data it was indistinguishable from the
// original. Two cautions when using this, both learned the hard way: score a
// HELD-OUT sample, never the one used for tuning, and run each variant more
// than once — agreement moves 5-10 points between identical runs, and further
// when anything else is using the same model.
func TestDurabilityProbe(t *testing.T) {
	path := os.Getenv("DURABILITY_SAMPLE")
	if path == "" {
		t.Skip("set DURABILITY_SAMPLE")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	if err := json.Unmarshal(raw, &texts); err != nil {
		t.Fatal(err)
	}
	// Reads MNEMOS_LLM_* from the environment, so the same harness can score the
	// local model or a hosted one without a code change.
	cfg, err := llm.ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	client, err := llm.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ClassifyDurability(context.Background(), client, texts)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	out := make([]string, len(got))
	for i, v := range got {
		out[i] = string(v)
	}
	blob, _ := json.Marshal(out)
	_ = os.WriteFile(path+".pred", blob, 0o600)
	fmt.Printf("wrote %d verdicts to %s.pred\n", len(out), path)
}
