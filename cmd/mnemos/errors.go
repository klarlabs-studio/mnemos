package main

import (
	"errors"
	"fmt"
	"os"
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

	code := ExitError
	msg := err.Error()
	hint := ""

	var me *MnemosError
	if errors.As(err, &me) {
		code = me.Code
		if verbose || code == ExitUsage {
			msg = me.Error()
		} else {
			msg = me.Message
		}
		hint = me.Hint
	}

	fmt.Fprintf(os.Stderr, "error: %s\n", msg)
	if hint != "" {
		fmt.Fprintf(os.Stderr, "\n%s\n", hint)
	}
	os.Exit(int(code))
}
