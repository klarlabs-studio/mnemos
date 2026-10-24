package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"strings"
	"syscall"
)

// ExitCode represents a process exit status returned by the CLI.
type ExitCode int

// Exit codes used by the mnemos CLI.
const (
	ExitSuccess  ExitCode = 0
	ExitError    ExitCode = 1
	ExitUsage    ExitCode = 2
	ExitNotFound ExitCode = 3
	ExitConfig   ExitCode = 4
)

// MnemosError is a structured error carrying an exit code, user message, and optional hint.
type MnemosError struct {
	Code    ExitCode
	Message string
	Cause   error
	Hint    string
}

func (e *MnemosError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Cause)
	}
	return e.Message
}

// FullMessage returns the error message followed by the hint, if present.
func (e *MnemosError) FullMessage() string {
	msg := e.Error()
	if e.Hint != "" {
		msg = msg + "\n\n" + e.Hint
	}
	return msg
}

// Unwrap returns the underlying cause, implementing the errors.Unwrap interface.
func (e *MnemosError) Unwrap() error {
	return e.Cause
}

// NewUserError returns a usage error with ExitUsage and a help hint.
func NewUserError(format string, args ...any) *MnemosError {
	return &MnemosError{Code: ExitUsage, Message: fmt.Sprintf(format, args...), Hint: "See 'mnemos --help' for usage"}
}

// NewNotFoundError returns a not-found error with ExitNotFound and an ingest hint.
func NewNotFoundError(format string, args ...any) *MnemosError {
	return &MnemosError{Code: ExitNotFound, Message: fmt.Sprintf(format, args...), Hint: "Tip: Run 'mnemos ingest' first to add content"}
}

// NewSystemError returns an internal error wrapping the given cause.
func NewSystemError(cause error, format string, args ...any) *MnemosError {
	return &MnemosError{Code: ExitError, Message: fmt.Sprintf(format, args...), Cause: cause}
}

// WrapError wraps a cause into a MnemosError with the given exit code and message.
func WrapError(code ExitCode, format string, cause error) *MnemosError {
	return &MnemosError{Code: code, Message: format, Cause: cause}
}

var _ error = (*MnemosError)(nil)

func exitWithMnemosError(verbose bool, err error) {
	if err == nil {
		os.Exit(int(ExitSuccess))
	}
	code, out := renderError(verbose, err)
	fmt.Fprint(os.Stderr, out)
	os.Exit(int(code))
}

// verboseHint closes every system error whose raw cause was withheld.
const verboseHint = "Re-run with --verbose for the full cause."

// renderError formats err for stderr. A system error's raw cause can carry
// file paths and driver internals, so it is printed only under --verbose;
// without it the cause is reduced to a class the operator can act on (#440).
// Twice that dropped cause was the whole diagnosis, and nothing said
// --verbose existed.
func renderError(verbose bool, err error) (ExitCode, string) {
	var me *MnemosError
	if !errors.As(err, &me) {
		return ExitError, fmt.Sprintf("error: %s\n", err.Error())
	}

	msg := me.Message
	withheld := false
	class := classifyCause(me.Cause)
	switch {
	case verbose || me.Code == ExitUsage:
		msg = me.Error()
	case class.label != "":
		msg = me.Message + ": " + class.label
		withheld = true
	case me.Cause != nil:
		withheld = true
	}

	var hints []string
	if class.advice != "" && me.Code != ExitUsage {
		hints = append(hints, class.advice)
	}
	if me.Hint != "" {
		hints = append(hints, me.Hint)
	}
	if withheld {
		hints = append(hints, verboseHint)
	}

	out := fmt.Sprintf("error: %s\n", msg)
	if len(hints) > 0 {
		out += "\n" + strings.Join(hints, "\n") + "\n"
	}
	return me.Code, out
}

// causeClass is what an operator can do about a cause, without its internals.
type causeClass struct {
	label  string
	advice string
}

var (
	classLocked = causeClass{"database is locked", "Another process is writing to this brain; wait for it to finish, then re-run."}
	classFull   = causeClass{"disk full", "Free disk space, then re-run."}
	classPerm   = causeClass{"permission denied", "Check that this user can read and write the database and its directory."}
	classTime   = causeClass{"timed out", "The operation ran past its time budget; MNEMOS_JOB_TIMEOUT raises it."}
	classNet    = causeClass{"network unreachable", "Check that the database or provider endpoint is reachable."}
)

// classifyCause maps a cause to a causeClass, or the zero value when it fits
// none. Typed checks come first; driver errors (SQLite, libSQL) only expose
// their result code in the message, so those are matched on text.
func classifyCause(err error) causeClass {
	if err == nil {
		return causeClass{}
	}
	text := strings.ToLower(err.Error())
	switch {
	case containsAny(text, "sqlite_busy", "sqlite_locked", "database is locked", "database table is locked"):
		return classLocked
	case errors.Is(err, syscall.ENOSPC), containsAny(text, "sqlite_full", "database or disk is full", "no space left on device"):
		return classFull
	case errors.Is(err, fs.ErrPermission), containsAny(text, "permission denied", "readonly database"):
		return classPerm
	case errors.Is(err, context.DeadlineExceeded):
		return classTime
	case isNetworkError(err), containsAny(text, "connection refused", "no such host", "network is unreachable"):
		return classNet
	}
	return causeClass{}
}

func isNetworkError(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENETUNREACH)
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
