package main

import (
	"slices"
	"testing"
)

func TestParseFlagsDB(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantDB   string
		wantRest []string
	}{
		{
			name:     "space form",
			args:     []string{"metrics", "--db", "sqlite:///tmp/a.db"},
			wantDB:   "sqlite:///tmp/a.db",
			wantRest: []string{"metrics"},
		},
		{
			name:     "equals form",
			args:     []string{"query", "--db=postgres://h/x", "hello"},
			wantDB:   "postgres://h/x",
			wantRest: []string{"query", "hello"},
		},
		{
			name:     "absent leaves DB empty",
			args:     []string{"metrics"},
			wantDB:   "",
			wantRest: []string{"metrics"},
		},
		{
			// A trailing --db (no value) is ignored rather than erroring,
			// matching --config; the DSN falls back to the normal chain.
			name:     "trailing db without value is ignored",
			args:     []string{"metrics", "--db"},
			wantDB:   "",
			wantRest: []string{"metrics"},
		},
		{
			// --db consumed globally must not survive into a subcommand's
			// positional args (this is what un-breaks `metrics --db X`).
			name:     "db is stripped from positional args",
			args:     []string{"hook", "recall", "--db", "sqlite:///tmp/b.db"},
			wantDB:   "sqlite:///tmp/b.db",
			wantRest: []string{"hook", "recall"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, rest := ParseFlags(tt.args)
			if f.DB != tt.wantDB {
				t.Errorf("DB = %q, want %q", f.DB, tt.wantDB)
			}
			if !slices.Equal(rest, tt.wantRest) {
				t.Errorf("rest = %v, want %v", rest, tt.wantRest)
			}
		})
	}
}
