package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/extract"
	"go.klarlabs.de/mnemos/internal/llm"
)

// Capture-time durability (ADR 0023).
//
// Recall demotes session-local beliefs below durable ones (demoteSessionLocal),
// but until this step only the bulk `classify-durability` pass and
// `prune --session-noise` ever set the verdict. Measured on a real brain, 40,851
// of 46,246 live beliefs (88%) had none, so the demotion had almost nothing to
// act on. Classifying at capture gives every new session belief a verdict the
// moment it exists.
//
// Measured against 34 classified beliefs a human had labelled false
// (2026-11-25, qwen2.5:3b, the extraction model on that machine): 27 were
// classified session-local, and no belief labelled true was. That matches the
// asymmetry ADR 0023 relies on — SESSION verdicts are reliable, DURABLE ones
// are over-called — and the only consumer, recall's demotion, keys on SESSION,
// so a wrong DURABLE verdict leaves a belief exactly where it was.

// durabilityClassifier labels each text, index-aligned. Anything it does not
// answer for stays extract.DurabilityUnknown.
type durabilityClassifier func(ctx context.Context, texts []string) ([]extract.Durability, error)

// newCaptureDurabilityClassifier builds the classifier capture runs over a
// session's beliefs: the configured LLM, with verdicts cached beside the bulk
// pass's so neither path classifies the same text twice. A variable so a test
// can stand in one that needs no LLM.
var newCaptureDurabilityClassifier = func() (durabilityClassifier, error) {
	cfg, err := llm.ConfigFromEnv()
	if err != nil {
		return nil, err
	}
	client, err := llm.NewClient(cfg)
	if err != nil {
		return nil, err
	}
	dir := durabilityCacheDir()
	return func(ctx context.Context, texts []string) ([]extract.Durability, error) {
		return extract.ClassifyDurabilityCached(ctx, client, texts, dir)
	}, nil
}

// captureDurabilityEnabled reports whether capture classifies durability. On by
// default; MNEMOS_CAPTURE_DURABILITY=off turns it off.
func captureDurabilityEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MNEMOS_CAPTURE_DURABILITY"))) {
	case "off", "false", "0", "no":
		return false
	}
	return true
}

// durabilityWarnings receives capture's warning that classification could not
// run. A variable so a test can read it.
var durabilityWarnings io.Writer = os.Stderr

// stampSessionDurability sets each claim's durability from the classifier, in
// place.
//
// It fails open like the narration judge: a classifier that cannot be built,
// errors, times out, or skips a claim leaves that claim unclassified, which
// every reader treats as "do not act". A capture never loses or delays a belief
// over this step beyond its budget, which judgeBudget takes from what the
// capture has left.
func stampSessionDurability(ctx context.Context, newClassifier func() (durabilityClassifier, error), claims []domain.Claim) {
	budget := judgeBudget(ctx)
	if len(claims) == 0 || budget == 0 {
		return
	}
	classify, err := newClassifier()
	if err != nil {
		_, _ = fmt.Fprintf(durabilityWarnings, "warning: captured %d claims without durability: %v\n", len(claims), err)
		return
	}
	cctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	texts := make([]string, len(claims))
	for i, c := range claims {
		texts[i] = c.Text
	}
	// A deadline or cancellation still returns the verdicts reached so far.
	verdicts, _ := classify(cctx, texts)
	for i := range claims {
		if i < len(verdicts) && verdicts[i] != extract.DurabilityUnknown {
			claims[i].Durability = domain.Durability(verdicts[i])
		}
	}
}
