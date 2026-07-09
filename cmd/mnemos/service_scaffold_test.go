package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServiceScaffoldWritesBundle(t *testing.T) {
	dir := t.TempDir()

	written, err := scaffoldService(dir, false)
	if err != nil {
		t.Fatalf("scaffoldService: %v", err)
	}
	if len(written) != 4 {
		t.Fatalf("expected 4 result lines, got %d: %v", len(written), written)
	}
	for _, line := range written {
		if !strings.HasPrefix(line, "wrote ") {
			t.Errorf("expected all lines to be writes, got %q", line)
		}
	}

	files := []string{
		"docker-compose.yml",
		"mnemos.yaml",
		".env.example",
		"README-mnemos-service.md",
	}
	for _, name := range files {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("expected %s to exist: %v", name, err)
		}
		if info.Size() == 0 {
			t.Errorf("expected %s to be non-empty", name)
		}
	}
}

func TestServiceScaffoldContent(t *testing.T) {
	dir := t.TempDir()
	if _, err := scaffoldService(dir, false); err != nil {
		t.Fatalf("scaffoldService: %v", err)
	}

	cases := []struct {
		file     string
		contains []string
	}{
		{"docker-compose.yml", []string{"postgres:16", "MNEMOS_DB_URL", "8080:8080"}},
		{"mnemos.yaml", []string{"serve:", "port: 8080"}},
		{".env.example", []string{"MNEMOS_JWT_SECRET"}},
	}
	for _, tc := range cases {
		data, err := os.ReadFile(filepath.Join(dir, tc.file))
		if err != nil {
			t.Fatalf("read %s: %v", tc.file, err)
		}
		for _, want := range tc.contains {
			if !strings.Contains(string(data), want) {
				t.Errorf("%s: expected to contain %q", tc.file, want)
			}
		}
	}
}

func TestServiceScaffoldIdempotent(t *testing.T) {
	dir := t.TempDir()

	if _, err := scaffoldService(dir, false); err != nil {
		t.Fatalf("first scaffoldService: %v", err)
	}

	// Second run without force must skip every existing file.
	written, err := scaffoldService(dir, false)
	if err != nil {
		t.Fatalf("second scaffoldService: %v", err)
	}
	if len(written) != 4 {
		t.Fatalf("expected 4 result lines, got %d: %v", len(written), written)
	}
	for _, line := range written {
		if !strings.HasPrefix(line, "skipped existing ") {
			t.Errorf("expected skip on second run, got %q", line)
		}
	}
}

func TestServiceScaffoldForceRewrites(t *testing.T) {
	dir := t.TempDir()

	if _, err := scaffoldService(dir, false); err != nil {
		t.Fatalf("first scaffoldService: %v", err)
	}

	written, err := scaffoldService(dir, true)
	if err != nil {
		t.Fatalf("force scaffoldService: %v", err)
	}
	if len(written) != 4 {
		t.Fatalf("expected 4 result lines, got %d: %v", len(written), written)
	}
	for _, line := range written {
		if !strings.HasPrefix(line, "wrote ") {
			t.Errorf("expected rewrite on force run, got %q", line)
		}
	}
}
