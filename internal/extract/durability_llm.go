package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"go.klarlabs.de/mnemos/internal/llm"
)

// LLM-backed durability classification (ADR 0023).
//
// The rule-based classifier in event_observation.go tops out around 78%
// precision, which ADR 0023 judged too low to ROUTE on — dropping a belief on
// a false positive is an invisible loss of real knowledge. This classifier is
// used for something weaker and safer: deciding whether a CONTRADICTION EDGE
// between two claims is worth surfacing. A mistake costs an alert, not a
// belief, which inverts the risk that blocked the routing form.
//
// The prompt is built around the asymmetry that makes it usable. Measured on a
// hand-labelled sample from a production brain, the model over-calls Durable
// (8 of 17 Durable verdicts were session-local) but never once called a
// genuinely durable claim SessionLocal: 18 SessionLocal verdicts, 0 errors.
// Suppression keyed on SessionLocal therefore fails safe — an over-cautious
// Durable verdict just leaves the edge in place.

// Durability says whether a claim's value outlives the conversation it came from.
type Durability string

// Durability verdicts.
const (
	// DurabilityUnknown means the classifier had no opinion — a parse failure,
	// a missing index, or a claim it never saw. Callers must treat it as
	// "do not act", never as SessionLocal.
	DurabilityUnknown Durability = ""
	// DurabilityDurable is knowledge that stays true and useful later.
	DurabilityDurable Durability = "durable"
	// DurabilitySessionLocal is tied to the moment it was written.
	DurabilitySessionLocal Durability = "session"
)

// Attempts to improve the durable side, and why the prompt still looks like
// this (measured 2026-07-21, qwen2.5:14b, against hand-labelled samples from a
// real brain):
//
//   - Rewriting the prompt around a "does it stand alone?" test plus explicit
//     few-shots for the observed failure modes scored 92% agreement (durable
//     precision 48% -> 89%) on the sample it was written against, and 60-70%
//     on a HELD-OUT sample where the original scored 57-63%. Durable precision
//     held out at 47%, against 43-50% for this prompt. The distributions
//     overlap: the gain was overfitting to the tuning sample, so the rewrite
//     was discarded rather than shipped.
//   - qwen2.5:32b was degenerate on the same task, labelling all 30 held-out
//     claims SESSION (which would demote real knowledge), and exceeded ten
//     minutes for thirty claims.
//
// Two things that make such measurements easy to misread, both hit here:
// run-to-run agreement varies by 5-10 points on an identical prompt and input,
// and it degrades further when anything else is using the same model — the
// recall fill-in worker was competing for it during the first comparison. A
// single run cannot separate two prompts.
//
// The practical consequence is that the DURABLE verdict should not be trusted
// on its own, and the design already assumes that: suppression keys on
// SessionLocal (the reliable direction), and an unknown or wrong durable
// verdict simply leaves a belief where it was.
//
// durabilityPrompt leans on the mechanism-vs-report distinction, which is what
// separates "the tests assert forget sets the status, and it does" (a durable
// statement about coverage) from "CI passed in 1m59s" (a snapshot).
const durabilityPrompt = `You classify sentences captured from an engineering chat transcript.

DURABLE = knowledge that stays true and useful weeks later: how a system behaves, why something breaks, a code or architecture fact, a decision with its reasoning, a constraint, a diagnostic conclusion.

SESSION = tied to the moment it was written: progress reports ("first test fails"), status snapshots ("CI passed in 1m59s", "both PRs are open"), completion announcements ("merged as abc123"), narration ("now the TypeScript release:"), plans, one-off measurements of a changing quantity.

When a sentence states a MECHANISM or a REASON it is DURABLE, even if it arose during work.
When it merely reports that something happened, or what state something is in right now, it is SESSION.

Reply with ONLY a JSON array of objects {"i": <index>, "c": "DURABLE"|"SESSION"}. No prose.

Sentences:
`

// durabilityBatchSize is how many claims go in one request. Small batches keep
// the model from losing track of indices; large ones amortise the prompt. 10
// was the size the sampling above was measured at.
const durabilityBatchSize = 10

// ClassifyDurability labels each text. The returned slice is index-aligned with
// texts; anything the model did not answer for stays DurabilityUnknown.
//
// A batch that fails or parses badly yields Unknown for its members rather than
// an error: this drives a maintenance pass over thousands of claims, and one
// bad batch must not discard the rest. Unknown is inert at every call site.
func ClassifyDurability(ctx context.Context, client llm.Client, texts []string) ([]Durability, error) {
	out := make([]Durability, len(texts))
	if client == nil {
		return nil, fmt.Errorf("mnemos: ClassifyDurability: no LLM client configured")
	}
	for start := 0; start < len(texts); start += durabilityBatchSize {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		end := min(start+durabilityBatchSize, len(texts))
		verdicts := classifyDurabilityBatch(ctx, client, texts[start:end])
		for i, v := range verdicts {
			out[start+i] = v
		}
	}
	return out, nil
}

func classifyDurabilityBatch(ctx context.Context, client llm.Client, texts []string) []Durability {
	out := make([]Durability, len(texts))
	var b strings.Builder
	b.WriteString(durabilityPrompt)
	for i, t := range texts {
		// One line per claim: a newline inside a claim would shift every
		// index the model reports after it.
		fmt.Fprintf(&b, "%d. %s\n", i, strings.ReplaceAll(strings.TrimSpace(t), "\n", " "))
	}
	resp, err := client.Complete(ctx, []llm.Message{{Role: llm.RoleUser, Content: b.String()}})
	if err != nil {
		return out // all Unknown
	}
	for i, v := range parseDurabilityVerdicts(resp.Content, len(texts)) {
		out[i] = v
	}
	return out
}

// parseDurabilityVerdicts pulls the JSON array out of a model reply that may be
// wrapped in prose or a code fence, and ignores any index outside the batch.
func parseDurabilityVerdicts(reply string, n int) []Durability {
	out := make([]Durability, n)
	open := strings.Index(reply, "[")
	closeAt := strings.LastIndex(reply, "]")
	if open < 0 || closeAt <= open {
		return out
	}
	var rows []struct {
		I int    `json:"i"`
		C string `json:"c"`
	}
	if err := json.Unmarshal([]byte(reply[open:closeAt+1]), &rows); err != nil {
		return out
	}
	for _, r := range rows {
		if r.I < 0 || r.I >= n {
			continue
		}
		switch strings.ToUpper(strings.TrimSpace(r.C)) {
		case "DURABLE":
			out[r.I] = DurabilityDurable
		case "SESSION":
			out[r.I] = DurabilitySessionLocal
		}
	}
	return out
}
