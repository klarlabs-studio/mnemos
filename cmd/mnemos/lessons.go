package main

import (
	"context"
	"fmt"
	"strconv"

	"go.klarlabs.de/mnemos/internal/synthesize"
)

// handleSynthesize routes `mnemos synthesize ...`. Runs one full
// synthesis pass over actions+outcomes and emits Lessons. The CLI
// surface is intentionally minimal — power flags belong on a config
// file rather than every invocation.
func handleSynthesize(args []string, _ Flags) {
	var minCorrob int
	var minConf float64
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--min-corroboration":
			if i+1 >= len(args) {
				exitWithMnemosError(false, NewUserError("--min-corroboration requires a value"))
				return
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 1 {
				exitWithMnemosError(false, NewUserError("--min-corroboration must be a positive integer"))
				return
			}
			minCorrob = n
			i++
		case "--min-confidence":
			if i+1 >= len(args) {
				exitWithMnemosError(false, NewUserError("--min-confidence requires a value"))
				return
			}
			f, err := strconv.ParseFloat(args[i+1], 64)
			if err != nil || f < 0 || f > 1 {
				exitWithMnemosError(false, NewUserError("--min-confidence must be a float in [0, 1]"))
				return
			}
			minConf = f
			i++
		default:
			exitWithMnemosError(false, NewUserError("unknown flag %q", args[i]))
			return
		}
	}
	ctx := context.Background()
	conn, err := openConn(ctx)
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "open store"))
		return
	}
	defer closeConn(conn)
	res, err := synthesize.Synthesize(ctx, conn.Actions, conn.Outcomes, conn.Lessons, synthesize.Options{
		MinCorroboration: minCorrob,
		MinConfidence:    minConf,
	})
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "synthesize"))
		return
	}
	emitJSON(map[string]any{
		"clusters":        res.Clusters,
		"lessons_emitted": res.LessonsEmitted,
		"skipped":         res.Skipped,
		"lesson_ids":      res.LessonIDs,
	})
}

// handleLessons routes `mnemos lessons <subcommand> ...`. Today only
// `list` is supported; structured for `show <id>` and `verify` later.
func handleLessons(args []string, _ Flags) {
	if len(args) == 0 {
		// Default: list all.
		handleLessonsList(nil)
		return
	}
	switch args[0] {
	case "list":
		handleLessonsList(args[1:])
	default:
		// Treat as a flag-only invocation of list.
		handleLessonsList(args)
	}
}

func handleLessonsList(args []string) {
	var service, trigger string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--service":
			if i+1 >= len(args) {
				exitWithMnemosError(false, NewUserError("--service requires a value"))
				return
			}
			service = args[i+1]
			i++
		case "--trigger":
			if i+1 >= len(args) {
				exitWithMnemosError(false, NewUserError("--trigger requires a value"))
				return
			}
			trigger = args[i+1]
			i++
		default:
			exitWithMnemosError(false, NewUserError("unknown flag %q", args[i]))
			return
		}
	}
	ctx := context.Background()
	conn, err := openConn(ctx)
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "open store"))
		return
	}
	defer closeConn(conn)
	switch {
	case service != "":
		ls, err := conn.Lessons.ListByService(ctx, service)
		if err != nil {
			exitWithMnemosError(false, NewSystemError(err, "list lessons by service"))
			return
		}
		emitJSON(ls)
	case trigger != "":
		ls, err := conn.Lessons.ListByTrigger(ctx, trigger)
		if err != nil {
			exitWithMnemosError(false, NewSystemError(err, "list lessons by trigger"))
			return
		}
		emitJSON(ls)
	default:
		ls, err := conn.Lessons.ListAll(ctx)
		if err != nil {
			exitWithMnemosError(false, NewSystemError(err, "list all lessons"))
			return
		}
		emitJSON(ls)
	}
}

var _ = fmt.Sprint // keep fmt import non-empty if helpers grow
