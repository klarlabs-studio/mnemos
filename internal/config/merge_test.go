package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSetValuesCreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "config.yaml")
	if err := SetValues(path, map[string]string{
		"db.url":       "postgres://u:p@h/db",
		"llm.provider": "ollama",
	}); err != nil {
		t.Fatalf("SetValues: %v", err)
	}
	// File is 0600.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	env := cfg.EnvOverrides()
	if env["MNEMOS_DB_URL"] != "postgres://u:p@h/db" {
		t.Errorf("db.url roundtrip = %q", env["MNEMOS_DB_URL"])
	}
	if env["MNEMOS_LLM_PROVIDER"] != "ollama" {
		t.Errorf("llm.provider roundtrip = %q", env["MNEMOS_LLM_PROVIDER"])
	}
}

func TestSetValuesPreservesExistingContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// Seed with an unrelated key and a comment.
	if err := os.WriteFile(path, []byte("# my config\nserve:\n  port: 9090\nllm:\n  provider: anthropic\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetValues(path, map[string]string{"db.url": "postgres://x/y"}); err != nil {
		t.Fatalf("SetValues: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	env := cfg.EnvOverrides()
	if env["MNEMOS_SERVE_PORT"] != "9090" {
		t.Errorf("existing serve.port lost: %q", env["MNEMOS_SERVE_PORT"])
	}
	if env["MNEMOS_LLM_PROVIDER"] != "anthropic" {
		t.Errorf("existing llm.provider lost: %q", env["MNEMOS_LLM_PROVIDER"])
	}
	if env["MNEMOS_DB_URL"] != "postgres://x/y" {
		t.Errorf("db.url not set: %q", env["MNEMOS_DB_URL"])
	}
}

func TestSetValuesUpdatesExistingKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := SetValues(path, map[string]string{"db.url": "postgres://old"}); err != nil {
		t.Fatal(err)
	}
	if err := SetValues(path, map[string]string{"db.url": "postgres://new"}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := Load(path)
	if got := cfg.EnvOverrides()["MNEMOS_DB_URL"]; got != "postgres://new" {
		t.Errorf("db.url = %q, want postgres://new", got)
	}
}
