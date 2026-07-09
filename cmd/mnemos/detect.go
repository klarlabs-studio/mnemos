package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"go.klarlabs.de/mnemos/internal/llm"
)

// environment is the result of probing the host so `mnemos init` can propose a
// sensible, zero-question setup. Everything here is observed, never mutated.
type environment struct {
	OS        string
	Arch      string
	ClaudeCLI bool // the `claude` CLI is on PATH
	ClaudeDir bool // ~/.claude exists (Claude Code has run before)
	Cursor    bool // Cursor is present (~/.cursor or ./.cursor)
	Desktop   bool // Claude Desktop config directory exists
	LLM       llmDetect
}

// llmDetect describes how (and whether) an LLM/embedding provider is available.
type llmDetect struct {
	Provider string // e.g. "ollama", "anthropic"
	Model    string
	BaseURL  string
	Source   string // "env" | "ollama" | "vendor-key" | "none"
	Advisory string // human hint when a raw vendor key was found but not wired
}

func detectEnvironment() environment {
	env := environment{
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		ClaudeCLI: lookPathOK("claude"),
		Cursor:    dirExists(homeJoin(".cursor")) || dirExists(".cursor"),
		LLM:       detectLLM(),
	}
	env.ClaudeDir = dirExists(homeJoin(".claude"))
	env.Desktop = fileExists(claudeDesktopConfigPath())
	return env
}

// detectLLM decides the best available provider without asking:
//  1. an explicit MNEMOS_LLM_PROVIDER wins,
//  2. a running local Ollama gives a fully offline, key-free brain,
//  3. a raw vendor key (ANTHROPIC/OPENAI/GEMINI) is surfaced as advice,
//  4. otherwise rule-based extraction (no LLM) — Mnemos still works.
func detectLLM() llmDetect {
	if p := strings.TrimSpace(os.Getenv("MNEMOS_LLM_PROVIDER")); p != "" {
		return llmDetect{
			Provider: p,
			Model:    strings.TrimSpace(os.Getenv("MNEMOS_LLM_MODEL")),
			BaseURL:  strings.TrimSpace(os.Getenv("MNEMOS_LLM_BASE_URL")),
			Source:   "env",
		}
	}
	if llm.OllamaAvailable() {
		return llmDetect{
			Provider: "ollama",
			Model:    "llama3.2",
			BaseURL:  "http://localhost:11434",
			Source:   "ollama",
		}
	}
	for _, k := range []struct{ env, provider string }{
		{"ANTHROPIC_API_KEY", "anthropic"},
		{"OPENAI_API_KEY", "openai"},
		{"GEMINI_API_KEY", "gemini"},
		{"GOOGLE_API_KEY", "gemini"},
	} {
		if strings.TrimSpace(os.Getenv(k.env)) != "" {
			return llmDetect{
				Provider: k.provider,
				Source:   "vendor-key",
				Advisory: "found " + k.env + " — set MNEMOS_LLM_PROVIDER=" + k.provider +
					" and MNEMOS_LLM_API_KEY to enable LLM extraction",
			}
		}
	}
	return llmDetect{Source: "none"}
}

// claudeDesktopConfigPath returns the platform Claude Desktop config location.
func claudeDesktopConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json")
	case "windows":
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "Claude", "claude_desktop_config.json")
		}
		return filepath.Join(home, "AppData", "Roaming", "Claude", "claude_desktop_config.json")
	default:
		return filepath.Join(home, ".config", "Claude", "claude_desktop_config.json")
	}
}

func lookPathOK(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

func homeJoin(parts ...string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(append([]string{home}, parts...)...)
}

func dirExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
