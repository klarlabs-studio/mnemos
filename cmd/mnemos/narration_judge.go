package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/extract"
	"go.klarlabs.de/mnemos/internal/llm"
	"go.klarlabs.de/mnemos/internal/narration"
	"go.klarlabs.de/mnemos/internal/pipeline"
)

// captureActor is the actor the capture hook writes as. Claims it created are
// what the narration judge reviews: they came from a session transcript, not
// from a document or a deliberate record.
const captureActor = "claude-code"

// defaultNarrationModel is the Ollama model the judge prompt was measured on.
// The extraction model on the same machine is often a 3B one, which answered
// "keep" to 84 of 87 narration claims in the same evaluation — so the judge
// does not inherit MNEMOS_LLM_MODEL on Ollama.
const defaultNarrationModel = "llama3.1"

// narrationJudgeFromEnv builds the narration judge on the configured provider.
// MNEMOS_NARRATION_MODEL picks its model; otherwise Ollama uses
// defaultNarrationModel and a hosted provider keeps its configured model.
func narrationJudgeFromEnv() (*narration.Judge, error) {
	cfg, err := llm.ConfigFromEnv()
	if err != nil {
		return nil, err
	}
	if m := strings.TrimSpace(os.Getenv("MNEMOS_NARRATION_MODEL")); m != "" {
		cfg.Model = m
	} else if cfg.Provider == llm.ProviderOllama {
		cfg.Model = defaultNarrationModel
	}
	client, err := llm.NewClient(cfg)
	if err != nil {
		return nil, err
	}
	return narration.New(client, narrationCacheDir()), nil
}

// newCaptureNarrationJudge builds the judge process_text runs over a session
// capture. A variable so a test can stand in a judge that needs no LLM.
var newCaptureNarrationJudge = func() (narrationClassifier, error) {
	return narrationJudgeFromEnv()
}

// newProcessTextExtractor builds process_text's claim extractor. A variable so
// a test can drive the LLM path of process_text without an LLM.
var newProcessTextExtractor = pipeline.NewExtractor

// narrationCacheDir is where judge verdicts are cached, beside the durability
// cache (see cache_dirs.go for why both resolve against dataDir).
func narrationCacheDir() string {
	return filepath.Join(dataDir(), "cache", "narration")
}

// narrationFilterEnabled reports whether capture runs the judge over the claims
// it extracts from a session. On by default; MNEMOS_NARRATION_FILTER=off turns
// it off.
func narrationFilterEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MNEMOS_NARRATION_FILTER"))) {
	case "off", "false", "0", "no":
		return false
	}
	return true
}

// narrationCaptureMax bounds the judge's share of one capture. Capture runs
// inside the SessionEnd hook's budget, and extraction usually spends most of it.
const narrationCaptureMax = 60 * time.Second

// narrationCaptureMin is the least budget worth starting a judge request with.
// Below it the request would only time out; the claims are kept instead.
const narrationCaptureMin = 20 * time.Second

// judgeBudget returns how long the capture-time judge may run under ctx, or 0
// when there is too little left to try.
func judgeBudget(ctx context.Context) time.Duration {
	budget := narrationCaptureMax
	if deadline, ok := ctx.Deadline(); ok {
		// Leave half of what remains for relating, embedding and the write.
		if half := time.Until(deadline) / 2; half < budget {
			budget = half
		}
	}
	if budget < narrationCaptureMin {
		return 0
	}
	return budget
}

// filterSessionNarration drops the claims the judge reads as session narration
// before a capture persists them, together with their evidence links and
// entities.
//
// It fails open. Any judge error, an answer that does not cover a claim, or a
// budget too short to ask all keep the claim: a capture must never lose
// knowledge because the judge was slow or unreachable.
func filterSessionNarration(
	ctx context.Context,
	judge narrationClassifier,
	claims []domain.Claim,
	links []domain.ClaimEvidence,
	entities map[string][]extract.ExtractedEntity,
) ([]domain.Claim, []domain.ClaimEvidence, map[string][]extract.ExtractedEntity) {
	budget := judgeBudget(ctx)
	if judge == nil || len(claims) == 0 || budget == 0 {
		return claims, links, entities
	}
	jctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	texts := make([]string, len(claims))
	for i, c := range claims {
		texts[i] = c.Text
	}
	verdicts, _ := judge.Classify(jctx, texts)

	dropped := make(map[string]struct{})
	kept := claims[:0:0]
	for i, c := range claims {
		if i < len(verdicts) && verdicts[i] == narration.Drop {
			dropped[c.ID] = struct{}{}
			continue
		}
		kept = append(kept, c)
	}
	if len(dropped) == 0 {
		return claims, links, entities
	}
	keptLinks := links[:0:0]
	for _, l := range links {
		if _, gone := dropped[l.ClaimID]; !gone {
			keptLinks = append(keptLinks, l)
		}
	}
	for id := range dropped {
		delete(entities, id)
	}
	return kept, keptLinks, entities
}
