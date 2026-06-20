package main

import (
	"context"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// handleDecision routes `mnemos decision <subcommand> ...`.
func handleDecision(args []string, _ Flags) {
	if len(args) == 0 {
		exitWithMnemosError(false, NewUserError("usage: mnemos decision <record|list|show|attach-outcome>"))
		return
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "record":
		handleDecisionRecord(rest)
	case "list":
		handleDecisionList(rest)
	case "show":
		handleDecisionShow(rest)
	case "attach-outcome":
		handleDecisionAttachOutcome(rest)
	default:
		exitWithMnemosError(false, NewUserError("unknown decision subcommand %q", sub))
	}
}

type decisionRecordArgs struct {
	id           string
	statement    string
	plan         string
	reasoning    string
	risk         string
	beliefs      []string
	alternatives []string
	chosenAt     time.Time
	outcomeID    string
}

func parseDecisionRecordArgs(args []string) (decisionRecordArgs, error) {
	out := decisionRecordArgs{chosenAt: time.Now().UTC(), risk: string(domain.RiskLevelMedium)}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--id":
			out.id = args[i+1]
			i++
		case "--statement":
			out.statement = args[i+1]
			i++
		case "--plan":
			out.plan = args[i+1]
			i++
		case "--reasoning":
			out.reasoning = args[i+1]
			i++
		case "--risk":
			out.risk = args[i+1]
			i++
		case "--belief":
			out.beliefs = append(out.beliefs, args[i+1])
			i++
		case "--alternative":
			out.alternatives = append(out.alternatives, args[i+1])
			i++
		case "--chosen-at":
			t, err := parseTimeArg(args[i+1])
			if err != nil {
				return out, err
			}
			out.chosenAt = t
			i++
		case "--outcome":
			out.outcomeID = args[i+1]
			i++
		default:
			return out, errorf("unknown flag %q", args[i])
		}
	}
	if strings.TrimSpace(out.statement) == "" {
		return out, errorf("--statement is required")
	}
	return out, nil
}

func errorf(format string, args ...any) error {
	return NewUserError(format, args...)
}

func handleDecisionRecord(args []string) {
	ra, err := parseDecisionRecordArgs(args)
	if err != nil {
		exitWithMnemosError(false, err)
		return
	}
	id := ra.id
	if id == "" {
		gen, err := newID("dc_")
		if err != nil {
			exitWithMnemosError(false, NewSystemError(err, "generate decision id"))
			return
		}
		id = gen
	}
	d := domain.Decision{
		ID:           id,
		Statement:    ra.statement,
		Plan:         ra.plan,
		Reasoning:    ra.reasoning,
		RiskLevel:    domain.RiskLevel(ra.risk),
		Beliefs:      ra.beliefs,
		Alternatives: ra.alternatives,
		OutcomeID:    ra.outcomeID,
		ChosenAt:     ra.chosenAt,
	}
	ctx := context.Background()
	w, err := openWriter(ctx)
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "open store"))
		return
	}
	defer closeWriter(w)
	if _, err := w.Decision(ctx, d); err != nil {
		exitWithMnemosError(false, NewSystemError(err, "append decision"))
		return
	}
	emitJSON(map[string]any{"id": d.ID, "statement": d.Statement, "risk_level": d.RiskLevel})
}

func handleDecisionList(args []string) {
	var risk string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--risk":
			risk = args[i+1]
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
	var ds []domain.Decision
	if risk != "" {
		ds, err = conn.Decisions.ListByRiskLevel(ctx, risk)
	} else {
		ds, err = conn.Decisions.ListAll(ctx)
	}
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "list decisions"))
		return
	}
	emitJSON(ds)
}

func handleDecisionShow(args []string) {
	if len(args) == 0 {
		exitWithMnemosError(false, NewUserError("decision show requires an id"))
		return
	}
	ctx := context.Background()
	conn, err := openConn(ctx)
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "open store"))
		return
	}
	defer closeConn(conn)
	d, err := conn.Decisions.GetByID(ctx, args[0])
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "get decision"))
		return
	}
	emitJSON(d)
}

func handleDecisionAttachOutcome(args []string) {
	if len(args) < 2 {
		exitWithMnemosError(false, NewUserError("usage: decision attach-outcome <decision-id> <outcome-id>"))
		return
	}
	ctx := context.Background()
	w, err := openWriter(ctx)
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "open store"))
		return
	}
	defer closeWriter(w)
	// The governed AttachOutcome both links the outcome and fires the
	// validates/refutes edges, replacing the prior inline AttachOutcome +
	// autoedge.OnDecisionOutcomeAttached pair.
	if err := w.AttachOutcome(ctx, args[0], args[1], ""); err != nil {
		exitWithMnemosError(false, NewSystemError(err, "attach outcome"))
		return
	}
	emitJSON(map[string]string{"decision_id": args[0], "outcome_id": args[1]})
}
