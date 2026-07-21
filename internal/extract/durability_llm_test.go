package extract

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.klarlabs.de/mnemos/internal/llm"
)

type stubLLM struct {
	reply      string
	err        error
	calls      int
	lastPrompt string
}

func (s *stubLLM) Complete(_ context.Context, msgs []llm.Message) (llm.Response, error) {
	s.calls++
	if len(msgs) > 0 {
		s.lastPrompt = msgs[0].Content
	}
	if s.err != nil {
		return llm.Response{}, s.err
	}
	return llm.Response{Content: s.reply}, nil
}

func TestClassifyDurability_ParsesVerdicts(t *testing.T) {
	c := &stubLLM{reply: `[{"i":0,"c":"SESSION"},{"i":1,"c":"DURABLE"}]`}
	got, err := ClassifyDurability(context.Background(), c, []string{"CI passed", "the retry is by design"})
	if err != nil {
		t.Fatalf("ClassifyDurability: %v", err)
	}
	if got[0] != DurabilitySessionLocal || got[1] != DurabilityDurable {
		t.Fatalf("wrong verdicts: %v", got)
	}
}

// A model that wraps its answer in prose or a code fence is the common case,
// not an edge case.
func TestClassifyDurability_ToleratesWrappedJSON(t *testing.T) {
	c := &stubLLM{reply: "Sure!\n```json\n[{\"i\":0,\"c\":\"DURABLE\"}]\n```\nHope that helps."}
	got, err := ClassifyDurability(context.Background(), c, []string{"x"})
	if err != nil || got[0] != DurabilityDurable {
		t.Fatalf("got %v err=%v", got, err)
	}
}

// The safety property the whole design rests on: anything the classifier did
// not answer for stays Unknown, never SessionLocal. Suppression keys on
// SessionLocal, so a failed batch must suppress nothing.
func TestClassifyDurability_FailuresAreUnknownNotSession(t *testing.T) {
	for _, tc := range []struct {
		name string
		c    *stubLLM
	}{
		{"request error", &stubLLM{err: errors.New("connection refused")}},
		{"unparseable reply", &stubLLM{reply: "I'm not sure how to answer that."}},
		{"index out of range", &stubLLM{reply: `[{"i":99,"c":"SESSION"}]`}},
		{"unknown label", &stubLLM{reply: `[{"i":0,"c":"MAYBE"}]`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ClassifyDurability(context.Background(), tc.c, []string{"a", "b"})
			if err != nil {
				t.Fatalf("a bad batch must not fail the pass: %v", err)
			}
			for i, v := range got {
				if v != DurabilityUnknown {
					t.Fatalf("index %d became %q; failures must stay Unknown", i, v)
				}
			}
		})
	}
}

// One bad batch must not discard the rest of a several-thousand-claim pass.
func TestClassifyDurability_BatchesIndependently(t *testing.T) {
	texts := make([]string, durabilityBatchSize+2)
	for i := range texts {
		texts[i] = "claim"
	}
	// Answers only ever cover the batch-local indices, so a reply naming 0
	// lands on the first member of EACH batch.
	c := &stubLLM{reply: `[{"i":0,"c":"SESSION"}]`}
	got, err := ClassifyDurability(context.Background(), c, texts)
	if err != nil {
		t.Fatal(err)
	}
	if c.calls != 2 {
		t.Fatalf("expected 2 batches, got %d", c.calls)
	}
	if got[0] != DurabilitySessionLocal || got[durabilityBatchSize] != DurabilitySessionLocal {
		t.Fatalf("batch-local indices misplaced: %v", got)
	}
}

// A newline inside a claim would shift every index the model reports after it,
// silently misattributing every later verdict in the batch.
func TestClassifyDurability_FlattensNewlines(t *testing.T) {
	c := &stubLLM{reply: `[{"i":0,"c":"SESSION"}]`}
	if _, err := ClassifyDurability(context.Background(), c, []string{"line one\nline two", "second"}); err != nil {
		t.Fatal(err)
	}
	at := strings.Index(c.lastPrompt, "Sentences:")
	if at < 0 {
		t.Fatalf("prompt lost its sentence block:\n%s", c.lastPrompt)
	}
	body := c.lastPrompt[at:]
	if strings.Count(strings.TrimSpace(body), "\n") != 2 { // "Sentences:" + 2 claims
		t.Fatalf("each claim must occupy exactly one line, got:\n%s", body)
	}
}

func TestClassifyDurability_NoClientIsAnError(t *testing.T) {
	if _, err := ClassifyDurability(context.Background(), nil, []string{"x"}); err == nil {
		t.Fatal("a missing client must be an error, not silent Unknowns")
	}
}
