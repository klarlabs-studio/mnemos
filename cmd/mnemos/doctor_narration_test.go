package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCheckNarrationJudge(t *testing.T) {
	installed := func(names ...string) func(context.Context, string) ([]string, error) {
		return func(context.Context, string) ([]string, error) { return names, nil }
	}
	cases := []struct {
		name   string
		env    map[string]string
		list   func(context.Context, string) ([]string, error)
		status string
		detail string
	}{
		{"default model missing", map[string]string{"MNEMOS_LLM_PROVIDER": "ollama"},
			installed("qwen2.5:3b", "gemma3:12b"), "warn", `"llama3.1" is not installed`},
		{"untagged name matches latest", map[string]string{"MNEMOS_LLM_PROVIDER": "ollama"},
			installed("llama3.1:latest"), "ok", "model=llama3.1"},
		{"configured model installed", map[string]string{"MNEMOS_LLM_PROVIDER": "ollama", "MNEMOS_NARRATION_MODEL": "gemma3:12b"},
			installed("gemma3:12b"), "ok", "model=gemma3:12b"},
		{"ollama unreachable", map[string]string{"MNEMOS_LLM_PROVIDER": "ollama"},
			func(context.Context, string) ([]string, error) { return nil, errors.New("connection refused") }, "warn", "could not list"},
		{"hosted provider not probed", map[string]string{"MNEMOS_LLM_PROVIDER": "anthropic", "MNEMOS_LLM_API_KEY": "k"},
			nil, "ok", "not probed"},
		{"filter off", map[string]string{"MNEMOS_LLM_PROVIDER": "ollama", "MNEMOS_NARRATION_FILTER": "off"},
			nil, "skipped", "off"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, k := range []string{"MNEMOS_LLM_PROVIDER", "MNEMOS_LLM_API_KEY", "MNEMOS_LLM_MODEL", "MNEMOS_LLM_BASE_URL", "MNEMOS_NARRATION_MODEL", "MNEMOS_NARRATION_FILTER"} {
				t.Setenv(k, tc.env[k])
			}
			got := checkNarrationJudge(context.Background(), tc.list)
			if got.Status != tc.status || !strings.Contains(got.Detail, tc.detail) {
				t.Fatalf("got %s %q, want %s containing %q", got.Status, got.Detail, tc.status, tc.detail)
			}
		})
	}
}
