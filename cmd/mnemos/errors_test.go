package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"strings"
	"syscall"
	"testing"
)

// The two causes #440 was filed over: each was the whole diagnosis, and each
// was dropped because the operator had not passed --verbose.
func TestRenderError_SystemErrorNamesClassifiedCauseByDefault(t *testing.T) {
	busy := errors.New("database is locked (5) (SQLITE_BUSY)")
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"open database", NewSystemError(busy, "failed to open database at /tmp/x.db"), "error: failed to open database at /tmp/x.db: database is locked"},
		{"backfill trust", NewSystemError(fmt.Errorf("list trust inputs: %w", busy), "backfill trust"), "error: backfill trust: database is locked"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out := renderError(false, tc.err)
			if code != ExitError {
				t.Fatalf("code = %d, want %d", code, ExitError)
			}
			if !strings.HasPrefix(out, tc.want+"\n") {
				t.Fatalf("output does not lead with the classified cause:\n%s", out)
			}
			if strings.Contains(out, "SQLITE_BUSY") || strings.Contains(out, "list trust inputs") {
				t.Fatalf("raw cause leaked without --verbose:\n%s", out)
			}
			if !strings.Contains(out, "Another process") {
				t.Fatalf("missing what-to-do advice:\n%s", out)
			}
			if !strings.Contains(out, verboseHint) {
				t.Fatalf("missing --verbose pointer:\n%s", out)
			}
		})
	}
}

func TestRenderError_UnclassifiedCausePointsAtVerbose(t *testing.T) {
	_, out := renderError(false, NewSystemError(errors.New("opaque internals at /home/u/secret"), "synthesize schemas"))
	if strings.Contains(out, "/home/u/secret") {
		t.Fatalf("unclassified raw cause printed without --verbose:\n%s", out)
	}
	if !strings.HasPrefix(out, "error: synthesize schemas\n") {
		t.Fatalf("unexpected message:\n%s", out)
	}
	if !strings.Contains(out, verboseHint) {
		t.Fatalf("an unclassified cause must at least say how to see it:\n%s", out)
	}
}

func TestRenderError_VerbosePrintsRawCauseAndNoPointer(t *testing.T) {
	_, out := renderError(true, NewSystemError(errors.New("database is locked (5) (SQLITE_BUSY)"), "backfill trust"))
	if !strings.HasPrefix(out, "error: backfill trust: database is locked (5) (SQLITE_BUSY)\n") {
		t.Fatalf("verbose must print the raw cause:\n%s", out)
	}
	if strings.Contains(out, verboseHint) {
		t.Fatalf("verbose output must not tell the operator to pass --verbose:\n%s", out)
	}
}

func TestRenderError_NoCauseNoPointer(t *testing.T) {
	_, out := renderError(false, &MnemosError{Code: ExitError, Message: "nothing to do"})
	if out != "error: nothing to do\n" {
		t.Fatalf("output = %q", out)
	}
}

func TestRenderError_UsageErrorUnchanged(t *testing.T) {
	code, out := renderError(false, NewUserError("missing --statement"))
	if code != ExitUsage {
		t.Fatalf("code = %d, want %d", code, ExitUsage)
	}
	if out != "error: missing --statement\n\nSee 'mnemos --help' for usage\n" {
		t.Fatalf("output = %q", out)
	}
}

func TestRenderError_KeepsCallerHint(t *testing.T) {
	err := &MnemosError{Code: ExitError, Message: "backfill trust", Cause: syscall.ENOSPC, Hint: "rows already rescored keep the new version; re-run to resume"}
	_, out := renderError(false, err)
	for _, want := range []string{"error: backfill trust: disk full\n", "Free disk space", "re-run to resume", verboseHint} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
}

func TestRenderError_PlainErrorPrintedWhole(t *testing.T) {
	code, out := renderError(false, errors.New("boom: detail"))
	if code != ExitError || out != "error: boom: detail\n" {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestClassifyCause(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"sqlite busy", errors.New("database is locked (5) (SQLITE_BUSY)"), "database is locked"},
		{"sqlite busy snapshot", errors.New("SQLITE_BUSY_SNAPSHOT"), "database is locked"},
		{"table locked", errors.New("database table is locked (6)"), "database is locked"},
		{"sqlite full", errors.New("database or disk is full (13)"), "disk full"},
		{"enospc", fmt.Errorf("write: %w", syscall.ENOSPC), "disk full"},
		{"permission", fmt.Errorf("open: %w", fs.ErrPermission), "permission denied"},
		{"readonly db", errors.New("attempt to write a readonly database (8)"), "permission denied"},
		{"deadline", fmt.Errorf("embed: %w", context.DeadlineExceeded), "timed out"},
		{"refused", fmt.Errorf("dial: %w", syscall.ECONNREFUSED), "network unreachable"},
		{"dns", &net.DNSError{Err: "no such host", Name: "db.example.invalid"}, "network unreachable"},
		{"refused text", errors.New("dial tcp 127.0.0.1:5432: connect: connection refused"), "network unreachable"},
		{"unknown", errors.New("something else"), ""},
		{"nil", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyCause(tc.err)
			if got.label != tc.want {
				t.Fatalf("classifyCause(%v) = %q, want %q", tc.err, got.label, tc.want)
			}
			if tc.want != "" && got.advice == "" {
				t.Fatalf("class %q has no advice", tc.want)
			}
		})
	}
}
