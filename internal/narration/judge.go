// Package narration judges whether a claim captured from an agent session is
// knowledge or the session narrating itself.
//
// Capture stores what the assistant says, and most of what an assistant says
// during a coding session is about the work in progress: "Both PRs are green",
// "Fixing the assertion", "That leaves four". On a hand-labelled sample of 120
// claims stored from captured sessions, 87 were that narration and 33 were
// knowledge a later session could use. Neither the speaker nor the shape of the
// sentence separates them — the durable third was mostly said by the assistant
// too ("drop_console strips console.log in production") — so the decision needs
// a model reading the statement.
//
// Asking the extractor to apply these rules did not work: a 3B extraction model
// given a session-specific prompt extracted MORE in-flight state, not less. A
// separate, short keep/drop question over finished claims is an easier task for
// a small model, and it can be measured on its own. Scored once on a second,
// held-out labelled sample of 120 with llama3.1 8B, this prompt dropped 30 of 79
// narration claims and 1 of 41 durable ones; on the sample it was tuned against,
// 41 of 87 and 3 of 33. It under-drops by design.
//
// Every failure keeps the claim. A narration claim that survives costs one
// noisy recall line; a durable claim that is dropped is knowledge lost without
// anyone seeing it go. So an unreachable model, an unparsable answer and a
// missing verdict all mean Keep.
package narration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"go.klarlabs.de/mnemos/internal/extract"
	"go.klarlabs.de/mnemos/internal/llm"
)

// PromptVersion names the judge prompt. It is part of every cached verdict and
// of the reason recorded on each deprecation, so bump it with any edit to
// Prompt.
const PromptVersion = "narration/v1"

// BatchSize is how many statements one request judges. The prompt was
// measured at this size.
const BatchSize = 20

// Prompt is the judge's system prompt, verbatim as measured.
const Prompt = `You review statements that were stored as knowledge from working sessions between a user and an AI coding assistant. Many of them are not knowledge at all but the session narrating itself. For each numbered statement decide KEEP or DROP. Ask: would this statement still be true and useful to someone opening the project next month?

DROP a statement when it is one of these:
- what the assistant is doing, about to do, or just did ("Now the UI", "Fixing the assertion", "I'll script the E2E journey")
- a snapshot that was true only at the moment it was said: what is available, free, running, applied, deployed, green, merged, open or still left to do; the numbers or outcome of one particular run or incident ("v1.83.2 is available", "68 GB free", "Migration 43 is applied", "CI fully green")
- a question, offer, recommendation or proposal awaiting an answer
- an apology, acknowledgement, praise or self-assessment with nothing else in it ("That's on me", "Good catch")
- task bookkeeping ("That leaves four", "Both remaining pieces are done")
- a fragment or code line that does not say what it is about ("Sources are flat", "It is guarded by site.enabled")

KEEP a statement that says how a system, codebase, tool, API or service is built, configured or behaves; a decision, rule or preference that was adopted (including "the user chose X"); a root cause, bug mechanism or lesson. If a statement opens with an acknowledgement or with "I" but goes on to state such a fact, judge the fact: KEEP. KEEP anything you are unsure about.

Return ONLY a JSON array, one object per statement, in order: [{"n": 1, "verdict": "keep"}, ...]`

// Verdict is the judge's answer for one statement.
type Verdict uint8

const (
	// Unknown means the judge gave no usable answer. Callers treat it as Keep;
	// it is distinct only so it is never cached as if it were a decision.
	Unknown Verdict = iota
	// Keep means the statement is knowledge worth recalling.
	Keep
	// Drop means the statement is the session narrating itself.
	Drop
)

// Judge classifies statements with one LLM client.
type Judge struct {
	client   llm.Client
	identity string
	cacheDir string
}

// New returns a judge over client. Verdicts are cached under cacheDir when the
// client can name itself; an empty cacheDir, or a client that cannot say which
// model it is, disables the cache, because a verdict whose producer is unknown
// could be served to a different model.
func New(client llm.Client, cacheDir string) *Judge {
	j := &Judge{client: client}
	if id, ok := client.(llm.Identified); ok {
		j.identity = id.Identity()
		j.cacheDir = cacheDir
	}
	return j
}

// Identity names the judge for audit records: the model that produced the
// verdicts and the prompt it was given.
func (j *Judge) Identity() string {
	model := j.identity
	if model == "" {
		model = "unidentified-model"
	}
	return model + " " + PromptVersion
}

// Classify judges texts in batches of BatchSize, returning one verdict per
// text in order. Cached verdicts cost nothing, so an interrupted pass resumes
// where it stopped. Each batch's verdicts are cached as soon as they land.
//
// The only error is the context's: a failed request or unparsable answer
// leaves that batch Unknown and the pass continues.
func (j *Judge) Classify(ctx context.Context, texts []string) ([]Verdict, error) {
	out := make([]Verdict, len(texts))
	var missIdx []int
	for i, t := range texts {
		if v, ok := j.cached(t); ok {
			out[i] = v
			continue
		}
		missIdx = append(missIdx, i)
	}
	for start := 0; start < len(missIdx); start += BatchSize {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		idx := missIdx[start:min(start+BatchSize, len(missIdx))]
		batch := make([]string, len(idx))
		for k, i := range idx {
			batch[k] = texts[i]
		}
		for k, v := range j.classifyBatch(ctx, batch) {
			out[idx[k]] = v
			if v != Unknown {
				j.store(batch[k], v)
			}
		}
	}
	return out, nil
}

// classifyBatch asks the model about one batch. Any failure returns Unknown
// for the whole batch.
func (j *Judge) classifyBatch(ctx context.Context, texts []string) []Verdict {
	var b strings.Builder
	for i, t := range texts {
		fmt.Fprintf(&b, "%d. %s\n", i+1, strings.Join(strings.Fields(t), " "))
	}
	resp, err := j.client.Complete(ctx, []llm.Message{
		{Role: llm.RoleSystem, Content: Prompt},
		{Role: llm.RoleUser, Content: b.String()},
	})
	if err != nil {
		return make([]Verdict, len(texts))
	}
	return ParseVerdicts(resp.Content, len(texts))
}

// ParseVerdicts reads the model's answer for n statements. A statement the
// answer does not cover, or covers with anything but "keep" or "drop", is
// Unknown; an answer that is not JSON at all leaves every statement Unknown.
func ParseVerdicts(content string, n int) []Verdict {
	out := make([]Verdict, n)
	var rows []struct {
		N       int    `json:"n"`
		Verdict string `json:"verdict"`
	}
	cleaned := extract.SanitizeLLMJSON(stripLineComments(content))
	if err := json.Unmarshal([]byte(cleaned), &rows); err != nil {
		return out
	}
	for _, r := range rows {
		if r.N < 1 || r.N > n {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(r.Verdict)) {
		case "keep":
			out[r.N-1] = Keep
		case "drop":
			out[r.N-1] = Drop
		}
	}
	return out
}

// stripLineComments removes // comments that sit outside JSON strings. Local
// models annotate their arrays ("// statement 4 is a status line"), which made
// one measured batch in 120 unparsable.
func stripLineComments(s string) string {
	var b strings.Builder
	inString, escaped := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case escaped:
			escaped = false
		case inString && c == '\\':
			escaped = true
		case c == '"':
			inString = !inString
		case !inString && c == '/' && i+1 < len(s) && s[i+1] == '/':
			for i < len(s) && s[i] != '\n' {
				i++
			}
			if i < len(s) {
				b.WriteByte('\n')
			}
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}
