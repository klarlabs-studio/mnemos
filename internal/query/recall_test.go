package query

import (
	"context"
	"errors"
	"testing"
)

// Recall is the one recall entry point (CLI, MCP, REST, gRPC and the library
// all call it), so its validation is the validation every surface applies.
func TestEngineRecall_ValidatesOnce(t *testing.T) {
	e := NewEngine(nil, nil, nil)
	cases := []struct {
		name string
		req  RecallRequest
	}{
		{"empty question", RecallRequest{Question: "   "}},
		{"negative hops", RecallRequest{Question: "q", Options: AnswerOptions{Hops: -1}}},
		{"hops above the bound", RecallRequest{Question: "q", Options: AnswerOptions{Hops: MaxHops + 1}}},
		{"min trust above one", RecallRequest{Question: "q", Options: AnswerOptions{MinTrust: 1.5}}},
		{"negative min trust", RecallRequest{Question: "q", Options: AnswerOptions{MinTrust: -0.1}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.Recall(context.Background(), tc.req)
			if !errors.Is(err, ErrInvalidRecall) {
				t.Fatalf("err = %v, want ErrInvalidRecall", err)
			}
		})
	}
}
