package extract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"

	"go.klarlabs.de/mnemos/internal/llm"
)

// Verdict caching for ClassifyDurability.
//
// Without it, "re-run to continue" is a lie. The pass classifies claims in a
// stable order, so a run that exhausts its budget after N claims and a re-run
// start from the same place: the second pass pays for the first N all over
// again and reaches barely further. On a brain whose backlog needs an hour of
// local-model time that means it never finishes.
//
// The key covers what produced the verdict — prompt, provider, model, claim
// text — so changing any of them re-classifies rather than serving a verdict
// the current configuration would not produce. Same reasoning as the
// extraction cache keying on PromptVersion and MNEMOS_EXTRACT_MODEL.

// durabilityPromptVersion invalidates cached verdicts when the prompt changes.
// Bump it with any edit to durabilityPrompt.
const durabilityPromptVersion = "v1"

// DurabilityCacheDir is the default location for cached verdicts, alongside the
// extraction cache.
var DurabilityCacheDir = filepath.Join("data", "cache", "durability")

// ClassifyDurabilityCached is ClassifyDurability with verdicts persisted under
// cacheDir. Claims already classified cost nothing, so an interrupted pass
// genuinely resumes where it stopped.
//
// An empty cacheDir disables caching. Cache I/O never fails the pass: an
// unreadable entry is a miss and an unwritable one is dropped, because a
// caching problem must not cost the caller a classification it paid for.
func ClassifyDurabilityCached(ctx context.Context, client llm.Client, texts []string, cacheDir string) ([]Durability, error) {
	if cacheDir == "" {
		return ClassifyDurability(ctx, client, texts)
	}
	out := make([]Durability, len(texts))
	var missIdx []int
	var missTexts []string
	for i, t := range texts {
		if v, ok := readDurability(cacheDir, t); ok {
			out[i] = v
			continue
		}
		missIdx = append(missIdx, i)
		missTexts = append(missTexts, t)
	}
	if len(missTexts) == 0 {
		return out, nil
	}
	verdicts, err := ClassifyDurability(ctx, client, missTexts)
	for j, v := range verdicts {
		if v == DurabilityUnknown {
			continue // never cache "we didn't get an answer"
		}
		out[missIdx[j]] = v
		writeDurability(cacheDir, missTexts[j], v)
	}
	return out, err
}

func durabilityCacheKey(text string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(durabilityPromptVersion))
	_, _ = h.Write([]byte("\n"))
	_, _ = h.Write([]byte(strings.TrimSpace(os.Getenv("MNEMOS_LLM_PROVIDER"))))
	_, _ = h.Write([]byte("\n"))
	_, _ = h.Write([]byte(strings.TrimSpace(os.Getenv("MNEMOS_LLM_MODEL"))))
	_, _ = h.Write([]byte("\n"))
	_, _ = h.Write([]byte(text))
	return hex.EncodeToString(h.Sum(nil))
}

func readDurability(dir, text string) (Durability, bool) {
	data, err := os.ReadFile(filepath.Join(dir, durabilityCacheKey(text)+".txt"))
	if err != nil {
		return DurabilityUnknown, false
	}
	switch Durability(strings.TrimSpace(string(data))) {
	case DurabilityDurable:
		return DurabilityDurable, true
	case DurabilitySessionLocal:
		return DurabilitySessionLocal, true
	}
	return DurabilityUnknown, false
}

func writeDurability(dir, text string, v Durability) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, durabilityCacheKey(text)+".txt"), []byte(v), 0o600)
}
