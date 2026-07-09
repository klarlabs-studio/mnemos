package main

import "strings"

// Flags holds parsed CLI flags.
type Flags struct {
	Help     bool
	Version  bool
	Verbose  bool
	Human    bool
	JSON     bool
	LLM      bool
	Embed    bool
	NoRelate bool
	Force    bool
	DryRun   bool
	Yes      bool
	// Actor is the user id to attribute writes to ("--as <id>"). Empty
	// means "fall back to MNEMOS_USER_ID, then to the <system> sentinel".
	Actor string
	// Config is an explicit config-file path ("--config <path>" or
	// "--config=<path>"). Empty falls back to MNEMOS_CONFIG and then to
	// walk-up / XDG discovery.
	Config string
}

// ParseFlags extracts known CLI flags from args and returns the remaining positional arguments.
func ParseFlags(args []string) (Flags, []string) {
	var f Flags
	filtered := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		// --config=<path> form: strip the value before the switch so the
		// remaining logic only handles the space-separated form.
		if v, ok := strings.CutPrefix(arg, "--config="); ok {
			f.Config = v
			continue
		}
		switch strings.ToLower(arg) {
		case "-h", "--help":
			f.Help = true
		case "--version":
			f.Version = true
		case "-v", "--verbose":
			f.Verbose = true
		case "--human", "-o":
			f.Human = true
		case "--json":
			f.JSON = true
		case "--llm":
			f.LLM = true
		case "--embed":
			f.Embed = true
		case "--no-relate":
			f.NoRelate = true
		case "--force":
			f.Force = true
		case "--dry-run":
			f.DryRun = true
		case "--yes", "-y":
			f.Yes = true
		case "--as":
			// --as wants the next positional as its value. If the flag
			// is trailing or followed by another flag, leave Actor empty
			// and let the resolver fall back to env / SystemUser.
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				f.Actor = args[i+1]
				i++
			}
		case "--config":
			// --config wants the next arg as its path. A trailing or
			// flag-followed --config leaves Config empty, falling back to
			// MNEMOS_CONFIG / discovery.
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				f.Config = args[i+1]
				i++
			}
		default:
			filtered = append(filtered, arg)
		}
	}
	return f, filtered
}
