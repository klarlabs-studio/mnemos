package main

import (
	"slices"
	"testing"
)

// TestParseFlagsNoValueSplitting guards against the corruption the review
// caught: the removed splitEqualsFlags pass used to rewrite ANY "--x=y" token —
// including flag VALUES — before parsing, tearing a spaced value like
// `--text "--db=… is the default"` into `--db` + `… is the default` and letting
// ParseFlags consume it as the global DSN. With that pass gone, a value is never
// split, so an ordinary value (even one containing "=") passes through verbatim.
//
// (Residual, unchanged from every other global flag: a value that is itself a
// single bare `--db=…`/`--config=…` token is still recognized as that global
// flag — a long-standing property of global flags, not introduced here.)
func TestParseFlagsNoValueSplitting(t *testing.T) {
	// A value containing "=" (not a bare global-flag token) is untouched.
	f, rest := ParseFlags([]string{"query", "what is x=y and a=b?"})
	if f.DB != "" || f.Config != "" {
		t.Errorf("a value must not be consumed: DB=%q Config=%q", f.DB, f.Config)
	}
	if !slices.Equal(rest, []string{"query", "what is x=y and a=b?"}) {
		t.Errorf("value corrupted: rest=%v", rest)
	}

	// The global value-flags accept the equals form when the user means them.
	fa, _ := ParseFlags([]string{"process", "--as=felix"})
	if fa.Actor != "felix" {
		t.Errorf("--as=felix should set Actor; got %q", fa.Actor)
	}
}

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
