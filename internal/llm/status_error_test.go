package llm

import (
	"errors"
	"fmt"
	"testing"
)

// A rejected credential, an exhausted account, a forbidden model and a wrong
// base URL do not heal on retry. #451: 1,229 extraction calls each retried a
// 402 three times before an external monitor noticed.
func TestIsPermanent(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   bool
	}{
		{401, true}, {402, true}, {403, true}, {404, true},
		{408, false}, {429, false}, {500, false}, {502, false}, {503, false},
	} {
		err := fmt.Errorf("complete: %w", &StatusError{Provider: "openai", Status: tc.status, Body: "x"})
		if got := IsPermanent(err); got != tc.want {
			t.Errorf("status %d: IsPermanent = %v, want %v", tc.status, got, tc.want)
		}
	}
	if IsPermanent(errors.New("dial tcp: connection refused")) {
		t.Error("a transport error is not permanent")
	}
}

// The message is what logs and MCP replies already show; keep it.
func TestStatusError_KeepsTheExistingMessage(t *testing.T) {
	got := (&StatusError{Provider: "anthropic", Status: 402, Body: "credit"}).Error()
	if got != "anthropic returned status 402: credit" {
		t.Errorf("Error() = %q", got)
	}
}
