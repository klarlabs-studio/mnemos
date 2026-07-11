package main

import (
	"context"
	"strconv"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/synthesize"
)

// handlePlaybook routes `mnemos playbook <subcommand>` and the
// shorthand `mnemos playbook <trigger>` for direct lookup.
func handlePlaybook(args []string, _ Flags) {
	if len(args) == 0 {
		handlePlaybookList(nil)
		return
	}
	switch args[0] {
	case "list":
		handlePlaybookList(args[1:])
	case "show":
		handlePlaybookShow(args[1:])
	case "synthesize":
		handlePlaybookSynthesize(args[1:])
	default:
		// Treat the bare arg as a trigger lookup.
		handlePlaybookByTrigger(args[0])
	}
}

func handlePlaybookList(args []string) {
	var service string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--service":
			if i+1 >= len(args) {
				exitWithMnemosError(false, NewUserError("--service requires a value"))
				return
			}
			service = args[i+1]
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
	var ps []domain.Playbook
	if service != "" {
		ps, err = conn.Playbooks.ListByService(ctx, service)
	} else {
		ps, err = conn.Playbooks.ListAll(ctx)
	}
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "list playbooks"))
		return
	}
	emitJSON(ps)
}

func handlePlaybookShow(args []string) {
	if len(args) == 0 {
		exitWithMnemosError(false, NewUserError("playbook show requires an id"))
		return
	}
	ctx := context.Background()
	conn, err := openConn(ctx)
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "open store"))
		return
	}
	defer closeConn(conn)
	p, err := conn.Playbooks.GetByID(ctx, args[0])
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "get playbook"))
		return
	}
	emitJSON(p)
}

func handlePlaybookByTrigger(trigger string) {
	ctx := context.Background()
	conn, err := openConn(ctx)
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "open store"))
		return
	}
	defer closeConn(conn)
	ps, err := conn.Playbooks.ListByTrigger(ctx, trigger)
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "list by trigger"))
		return
	}
	emitJSON(ps)
}

func handlePlaybookSynthesize(args []string) {
	var minLessons int
	var minConf float64
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--min-lessons":
			if i+1 >= len(args) {
				exitWithMnemosError(false, NewUserError("--min-lessons requires a value"))
				return
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 1 {
				exitWithMnemosError(false, NewUserError("--min-lessons must be a positive integer"))
				return
			}
			minLessons = n
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
	res, err := synthesize.Playbooks(ctx, conn.Lessons, conn.Playbooks, synthesize.PlaybookOptions{
		MinLessons:    minLessons,
		MinConfidence: minConf,
	})
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "synthesize playbooks"))
		return
	}
	emitJSON(map[string]any{
		"trigger_clusters":  res.TriggerClusters,
		"playbooks_emitted": res.PlaybooksEmitted,
		"skipped":           res.Skipped,
		"playbook_ids":      res.PlaybookIDs,
	})
}
