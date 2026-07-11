package main

import (
	"strings"
	"testing"
)

func TestParseSetupArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    setupOpts
		wantErr bool
	}{
		{
			name: "defaults to claude-code user scope",
			args: nil,
			want: setupOpts{target: "claude-code"},
		},
		{
			name: "project flag",
			args: []string{"--project"},
			want: setupOpts{target: "claude-code", project: true},
		},
		{
			name: "explicit target and db",
			args: []string{"claude-code", "--db", "postgres://x/y"},
			want: setupOpts{target: "claude-code", dsn: "postgres://x/y"},
		},
		{
			name: "print flag",
			args: []string{"--print"},
			want: setupOpts{target: "claude-code", print: true},
		},
		{
			name:    "db without value",
			args:    []string{"--db"},
			wantErr: true,
		},
		{
			name:    "unknown flag",
			args:    []string{"--nope"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSetupArgs(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (%+v)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("parseSetupArgs(%v) = %+v, want %+v", tt.args, got, tt.want)
			}
		})
	}
}

func TestSQLiteFilePath(t *testing.T) {
	tests := []struct {
		dsn      string
		wantPath string
		wantOK   bool
	}{
		{"sqlite:///Users/x/.local/share/mnemos/mnemos.db", "/Users/x/.local/share/mnemos/mnemos.db", true},
		{"sqlite3:///tmp/a.db", "/tmp/a.db", true},
		{"sqlite:///tmp/a.db?_journal=WAL", "/tmp/a.db", true},
		{"file:///tmp/b.db", "/tmp/b.db", true},
		{"sqlite://:memory:", "", false},
		{"memory://", "", false},
		{"postgres://host/db", "", false},
		{"mysql://host/db", "", false},
	}
	for _, tt := range tests {
		gotPath, gotOK := sqliteFilePath(tt.dsn)
		if gotPath != tt.wantPath || gotOK != tt.wantOK {
			t.Errorf("sqliteFilePath(%q) = (%q, %v), want (%q, %v)",
				tt.dsn, gotPath, gotOK, tt.wantPath, tt.wantOK)
		}
	}
}

func TestResolveBinPath(t *testing.T) {
	// A Homebrew install launches via the stable /opt/homebrew/bin/mnemos
	// symlink. os.Executable() returns that absolute symlink path; resolveBinPath
	// must keep it as-is rather than following it to a versioned Cellar path that
	// `brew upgrade` deletes.
	tests := []struct {
		name string
		exe  string
		err  error
		want string
	}{
		{"homebrew symlink kept intact", "/opt/homebrew/bin/mnemos", nil, "/opt/homebrew/bin/mnemos"},
		{"plain absolute path", "/usr/local/bin/mnemos", nil, "/usr/local/bin/mnemos"},
		{"error falls back to bare name", "", errStub, "mnemos"},
		{"empty falls back to bare name", "", nil, "mnemos"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveBinPath(tt.exe, tt.err); got != tt.want {
				t.Errorf("resolveBinPath(%q, %v) = %q, want %q", tt.exe, tt.err, got, tt.want)
			}
		})
	}
}

var errStub = errStubType("boom")

type errStubType string

func (e errStubType) Error() string { return string(e) }

func TestMCPJSONSnippetIsValidShape(t *testing.T) {
	snippet := mcpJSONSnippet("/usr/local/bin/mnemos", "sqlite:///tmp/a.db", true)
	for _, want := range []string{`"type": "stdio"`, `"command": "/usr/local/bin/mnemos"`, `"args": ["mcp"]`, `"MNEMOS_DB_URL": "sqlite:///tmp/a.db"`} {
		if !strings.Contains(snippet, want) {
			t.Errorf("snippet missing %q:\n%s", want, snippet)
		}
	}
}
