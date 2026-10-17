package narration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.klarlabs.de/mnemos/internal/llm"
)

// scripted answers each request with the next response, counting calls and
// recording how many statements each request carried.
type scripted struct {
	responses []string
	err       error
	calls     int
	sizes     []int
	identity  string
}

func (s *scripted) Complete(_ context.Context, msgs []llm.Message) (llm.Response, error) {
	s.calls++
	user := msgs[len(msgs)-1].Content
	s.sizes = append(s.sizes, strings.Count(user, "\n"))
	if s.err != nil {
		return llm.Response{}, s.err
	}
	if len(s.responses) == 0 {
		return llm.Response{Content: "[]"}, nil
	}
	r := s.responses[0]
	s.responses = s.responses[1:]
	return llm.Response{Content: r}, nil
}

// identifiedScript is a scripted client that can name its model, which is what
// enables the cache.
type identifiedScript struct{ *scripted }

func (c identifiedScript) Identity() string { return c.identity }

func TestParseVerdicts(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    []Verdict
	}{
		{"plain", `[{"n":1,"verdict":"keep"},{"n":2,"verdict":"drop"}]`, []Verdict{Keep, Drop}},
		{"case and fences", "```json\n[{\"n\":1,\"verdict\":\"DROP\"},{\"n\":2,\"verdict\":\" Keep \"}]\n```", []Verdict{Drop, Keep}},
		{"prose around it", "Here you go:\n[{\"n\":2,\"verdict\":\"drop\"}]\nHope that helps.", []Verdict{Unknown, Drop}},
		{"line comments", "[\n{\"n\":1,\"verdict\":\"drop\"}, // a status line\n{\"n\":2,\"verdict\":\"keep\"}\n]", []Verdict{Drop, Keep}},
		{"slashes inside a string survive", `[{"n":1,"verdict":"keep","why":"see https://x.test"}]`, []Verdict{Keep}},
		{"not json", "I cannot help with that.", []Verdict{Unknown, Unknown}},
		{"unknown word and out of range", `[{"n":1,"verdict":"maybe"},{"n":9,"verdict":"drop"},{"n":0,"verdict":"drop"}]`, []Verdict{Unknown, Unknown}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseVerdicts(tc.content, len(tc.want))
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("verdicts = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// A failed request must never drop anything: every statement in that batch
// comes back Unknown, which callers keep.
func TestClassifyKeepsEverythingWhenTheModelFails(t *testing.T) {
	j := New(&scripted{err: errors.New("connection refused")}, "")
	got, err := j.Classify(context.Background(), []string{"Both PRs are green", "drop_console strips console.log"})
	if err != nil {
		t.Fatalf("a model failure must not fail the pass: %v", err)
	}
	for i, v := range got {
		if v == Drop {
			t.Errorf("statement %d dropped although the model never answered", i)
		}
	}
}

func TestClassifyBatchesAndKeepsOrder(t *testing.T) {
	texts := make([]string, 45)
	for i := range texts {
		texts[i] = fmt.Sprintf("statement %d", i)
	}
	// Batch one drops its 3rd statement, batch two its 1st, batch three none.
	s := &scripted{responses: []string{
		`[{"n":3,"verdict":"drop"}]`,
		`[{"n":1,"verdict":"drop"}]`,
		`[{"n":1,"verdict":"keep"}]`,
	}}
	got, err := New(s, "").Classify(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	if s.calls != 3 || s.sizes[0] != BatchSize || s.sizes[1] != BatchSize || s.sizes[2] != 5 {
		t.Fatalf("requests carried %v statements over %d calls, want [20 20 5] over 3", s.sizes, s.calls)
	}
	for i, v := range got {
		want := Unknown
		switch i {
		case 2, 20:
			want = Drop
		case 40:
			want = Keep
		}
		if v != want {
			t.Errorf("statement %d: verdict %v, want %v", i, v, want)
		}
	}
}

// The cache is what makes a whole-brain pass resumable: a second run must not
// ask the model again about statements it already judged, and must not cache
// a non-answer as though it were one.
func TestClassifyResumesFromTheCache(t *testing.T) {
	dir := t.TempDir()
	texts := []string{"Both PRs are green", "drop_console strips console.log", "unanswered"}
	first := &scripted{identity: "ollama|llama3.1|http://x", responses: []string{
		`[{"n":1,"verdict":"drop"},{"n":2,"verdict":"keep"}]`,
	}}
	if _, err := New(identifiedScript{first}, dir).Classify(context.Background(), texts); err != nil {
		t.Fatal(err)
	}
	second := &scripted{identity: "ollama|llama3.1|http://x", responses: []string{`[{"n":1,"verdict":"drop"}]`}}
	got, err := New(identifiedScript{second}, dir).Classify(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	if second.calls != 1 || second.sizes[0] != 1 {
		t.Fatalf("second pass asked about %v statements in %d calls; only the unanswered one should be asked again", second.sizes, second.calls)
	}
	if got[0] != Drop || got[1] != Keep || got[2] != Drop {
		t.Errorf("verdicts = %v, want [Drop Keep Drop]", got)
	}

	// Another model must not be served these verdicts.
	other := &scripted{identity: "ollama|qwen2.5:3b|http://x"}
	if _, err := New(identifiedScript{other}, dir).Classify(context.Background(), texts); err != nil {
		t.Fatal(err)
	}
	if other.calls != 1 || other.sizes[0] != 3 {
		t.Errorf("a different model reused cached verdicts: asked about %v", other.sizes)
	}
}

// A client that cannot name its model gets no cache: its verdicts could be
// served to a different model later.
func TestUnidentifiedClientIsNotCached(t *testing.T) {
	dir := t.TempDir()
	for range 2 {
		s := &scripted{responses: []string{`[{"n":1,"verdict":"drop"}]`}}
		if _, err := New(s, dir).Classify(context.Background(), []string{"Both PRs are green"}); err != nil {
			t.Fatal(err)
		}
		if s.calls != 1 {
			t.Fatalf("unidentified client served from cache")
		}
	}
}

func TestClassifyStopsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := &scripted{}
	_, err := New(s, "").Classify(ctx, []string{"a", "b"})
	if !errors.Is(err, context.Canceled) || s.calls != 0 {
		t.Errorf("err=%v calls=%d, want context.Canceled and no request", err, s.calls)
	}
}
