package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/autoedge"
	"go.klarlabs.de/mnemos/internal/domain"
)

// handleAction routes `mnemos action <subcommand> ...`. Today only
// `record` is supported; the dispatch shape leaves room for `list`
// and `show` without churning main.go.
func handleAction(args []string, _ Flags) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: mnemos action record --kind <kind> --subject <name> [...]")
		os.Exit(int(ExitUsage))
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "record":
		handleActionRecord(rest)
	case "list":
		handleActionList(rest)
	default:
		fmt.Fprintf(os.Stderr, "error: unknown action subcommand %q\n", sub)
		os.Exit(int(ExitUsage))
	}
}

// handleOutcome routes `mnemos outcome <subcommand> ...`.
func handleOutcome(args []string, _ Flags) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: mnemos outcome record --action <id> --result <success|failure|partial|unknown> [...]")
		os.Exit(int(ExitUsage))
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "record":
		handleOutcomeRecord(rest)
	case "list":
		handleOutcomeList(rest)
	default:
		fmt.Fprintf(os.Stderr, "error: unknown outcome subcommand %q\n", sub)
		os.Exit(int(ExitUsage))
	}
}

type actionRecordArgs struct {
	id       string
	runID    string
	kind     string
	subject  string
	actor    string
	at       time.Time
	metadata map[string]string
}

func parseActionRecordArgs(args []string) (actionRecordArgs, error) {
	out := actionRecordArgs{at: time.Now().UTC(), metadata: map[string]string{}}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--id":
			if i+1 >= len(args) {
				return out, fmt.Errorf("--id requires a value")
			}
			out.id = args[i+1]
			i++
		case "--run":
			if i+1 >= len(args) {
				return out, fmt.Errorf("--run requires a value")
			}
			out.runID = args[i+1]
			i++
		case "--kind":
			if i+1 >= len(args) {
				return out, fmt.Errorf("--kind requires a value")
			}
			out.kind = args[i+1]
			i++
		case "--subject":
			if i+1 >= len(args) {
				return out, fmt.Errorf("--subject requires a value")
			}
			out.subject = args[i+1]
			i++
		case "--actor":
			if i+1 >= len(args) {
				return out, fmt.Errorf("--actor requires a value")
			}
			out.actor = args[i+1]
			i++
		case "--at":
			if i+1 >= len(args) {
				return out, fmt.Errorf("--at requires a value")
			}
			t, err := parseTimeArg(args[i+1])
			if err != nil {
				return out, fmt.Errorf("--at: %w", err)
			}
			out.at = t
			i++
		case "--metadata":
			if i+1 >= len(args) {
				return out, fmt.Errorf("--metadata requires k=v[,k=v]")
			}
			if err := parseKVList(args[i+1], out.metadata); err != nil {
				return out, fmt.Errorf("--metadata: %w", err)
			}
			i++
		default:
			return out, fmt.Errorf("unknown flag %q", args[i])
		}
	}
	if strings.TrimSpace(out.kind) == "" {
		return out, fmt.Errorf("--kind is required")
	}
	if strings.TrimSpace(out.subject) == "" {
		return out, fmt.Errorf("--subject is required")
	}
	return out, nil
}

func handleActionRecord(args []string) {
	ra, err := parseActionRecordArgs(args)
	if err != nil {
		exitWithMnemosError(false, NewUserError("%v", err))
		return
	}
	id := ra.id
	if id == "" {
		generated, err := newID("ac_")
		if err != nil {
			exitWithMnemosError(false, NewSystemError(err, "generate action id"))
			return
		}
		id = generated
	}
	action := domain.Action{
		ID:        id,
		RunID:     ra.runID,
		Kind:      domain.ActionKind(ra.kind),
		Subject:   ra.subject,
		Actor:     ra.actor,
		At:        ra.at,
		Metadata:  ra.metadata,
		CreatedBy: ra.actor,
	}
	ctx := context.Background()
	conn, err := openConn(ctx)
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "open store"))
		return
	}
	defer closeConn(conn)
	if err := conn.Actions.Append(ctx, action); err != nil {
		exitWithMnemosError(false, NewSystemError(err, "append action"))
		return
	}
	emitJSON(map[string]string{"id": action.ID, "kind": string(action.Kind), "subject": action.Subject})
}

func handleActionList(args []string) {
	var subject, runID string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--subject":
			if i+1 >= len(args) {
				exitWithMnemosError(false, NewUserError("--subject requires a value"))
				return
			}
			subject = args[i+1]
			i++
		case "--run":
			if i+1 >= len(args) {
				exitWithMnemosError(false, NewUserError("--run requires a value"))
				return
			}
			runID = args[i+1]
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
	var rows []domain.Action
	switch {
	case subject != "":
		rows, err = conn.Actions.ListBySubject(ctx, subject)
	case runID != "":
		rows, err = conn.Actions.ListByRunID(ctx, runID)
	default:
		rows, err = conn.Actions.ListAll(ctx)
	}
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "list actions"))
		return
	}
	emitJSON(rows)
}

type outcomeRecordArgs struct {
	id         string
	actionID   string
	result     string
	metrics    map[string]float64
	notes      string
	observedAt time.Time
	source     string
}

func parseOutcomeRecordArgs(args []string) (outcomeRecordArgs, error) {
	out := outcomeRecordArgs{
		observedAt: time.Now().UTC(),
		source:     "push",
		metrics:    map[string]float64{},
	}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--id":
			if i+1 >= len(args) {
				return out, fmt.Errorf("--id requires a value")
			}
			out.id = args[i+1]
			i++
		case "--action":
			if i+1 >= len(args) {
				return out, fmt.Errorf("--action requires a value")
			}
			out.actionID = args[i+1]
			i++
		case "--result":
			if i+1 >= len(args) {
				return out, fmt.Errorf("--result requires a value")
			}
			out.result = args[i+1]
			i++
		case "--metric":
			if i+1 >= len(args) {
				return out, fmt.Errorf("--metric requires k=v")
			}
			if err := parseFloatKV(args[i+1], out.metrics); err != nil {
				return out, fmt.Errorf("--metric: %w", err)
			}
			i++
		case "--notes":
			if i+1 >= len(args) {
				return out, fmt.Errorf("--notes requires a value")
			}
			out.notes = args[i+1]
			i++
		case "--observed-at":
			if i+1 >= len(args) {
				return out, fmt.Errorf("--observed-at requires a value")
			}
			t, err := parseTimeArg(args[i+1])
			if err != nil {
				return out, fmt.Errorf("--observed-at: %w", err)
			}
			out.observedAt = t
			i++
		case "--source":
			if i+1 >= len(args) {
				return out, fmt.Errorf("--source requires a value")
			}
			out.source = args[i+1]
			i++
		default:
			return out, fmt.Errorf("unknown flag %q", args[i])
		}
	}
	if strings.TrimSpace(out.actionID) == "" {
		return out, fmt.Errorf("--action is required")
	}
	if strings.TrimSpace(out.result) == "" {
		return out, fmt.Errorf("--result is required")
	}
	return out, nil
}

func handleOutcomeRecord(args []string) {
	ra, err := parseOutcomeRecordArgs(args)
	if err != nil {
		exitWithMnemosError(false, NewUserError("%v", err))
		return
	}
	id := ra.id
	if id == "" {
		generated, err := newID("oc_")
		if err != nil {
			exitWithMnemosError(false, NewSystemError(err, "generate outcome id"))
			return
		}
		id = generated
	}
	outcome := domain.Outcome{
		ID:         id,
		ActionID:   ra.actionID,
		Result:     domain.OutcomeResult(ra.result),
		Metrics:    ra.metrics,
		Notes:      ra.notes,
		ObservedAt: ra.observedAt,
		Source:     ra.source,
	}
	ctx := context.Background()
	conn, err := openConn(ctx)
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "open store"))
		return
	}
	defer closeConn(conn)
	if err := conn.Outcomes.Append(ctx, outcome); err != nil {
		exitWithMnemosError(false, NewSystemError(err, "append outcome"))
		return
	}
	if err := autoedge.OnOutcomeAppended(ctx, conn.EntityRels, outcome, outcome.CreatedBy); err != nil {
		exitWithMnemosError(false, NewSystemError(err, "auto-link action_of edges"))
		return
	}
	emitJSON(map[string]string{"id": outcome.ID, "action_id": outcome.ActionID, "result": string(outcome.Result)})
}

func handleOutcomeList(args []string) {
	var actionID string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--action":
			if i+1 >= len(args) {
				exitWithMnemosError(false, NewUserError("--action requires a value"))
				return
			}
			actionID = args[i+1]
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
	var rows []domain.Outcome
	if actionID != "" {
		rows, err = conn.Outcomes.ListByActionID(ctx, actionID)
	} else {
		rows, err = conn.Outcomes.ListAll(ctx)
	}
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "list outcomes"))
		return
	}
	emitJSON(rows)
}

// parseTimeArg accepts "now", an RFC3339(Nano) timestamp, or a
// YYYY-MM-DD date interpreted as UTC midnight.
func parseTimeArg(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "now" {
		return time.Now().UTC(), nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("unrecognised time format %q (want RFC3339, YYYY-MM-DD, or 'now')", s)
}

// parseKVList parses k=v[,k=v] into the destination map. Returns an
// error on malformed entries; caller decides how to surface.
func parseKVList(spec string, dst map[string]string) error {
	if strings.TrimSpace(spec) == "" {
		return nil
	}
	for _, part := range strings.Split(spec, ",") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 || strings.TrimSpace(kv[0]) == "" {
			return fmt.Errorf("bad entry %q (want k=v)", part)
		}
		dst[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
	}
	return nil
}

// parseFloatKV is parseKVList for metric values. The CLI accepts
// repeated --metric flags so the spec is a single k=v per call.
func parseFloatKV(spec string, dst map[string]float64) error {
	kv := strings.SplitN(strings.TrimSpace(spec), "=", 2)
	if len(kv) != 2 || strings.TrimSpace(kv[0]) == "" {
		return fmt.Errorf("bad metric %q (want k=v)", spec)
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(kv[1]), 64)
	if err != nil {
		return fmt.Errorf("bad metric value %q: %w", kv[1], err)
	}
	dst[strings.TrimSpace(kv[0])] = v
	return nil
}

func newID(prefix string) (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(buf), nil
}

func emitJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
