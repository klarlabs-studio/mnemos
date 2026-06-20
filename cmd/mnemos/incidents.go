package main

import (
	"context"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// handleIncident routes `mnemos incident <subcommand> ...`.
func handleIncident(args []string, _ Flags) {
	if len(args) == 0 {
		exitWithMnemosError(false, NewUserError("usage: mnemos incident <open|close|show|list>"))
		return
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "open":
		handleIncidentOpen(rest)
	case "close":
		handleIncidentClose(rest)
	case "show":
		handleIncidentShow(rest)
	case "list":
		handleIncidentList(rest)
	default:
		exitWithMnemosError(false, NewUserError("unknown incident subcommand %q", sub))
	}
}

// handleIncidentOpen implements `mnemos incident open`.
//
// Flags:
//
//	--id <id>              optional; generated as inc_<ulid> when absent
//	--title <text>         required
//	--summary <text>       optional
//	--severity <s>         sev1|sev2|sev3|sev4 (default: sev2)
//	--root-claim <id>      optional claim ID that was the root cause
//	--created-by <actor>   optional actor string
func handleIncidentOpen(args []string) {
	var (
		id        string
		title     string
		summary   string
		severity  = string(domain.IncidentSeverityMedium)
		rootClaim string
		createdBy string
	)
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--id":
			id = args[i+1]
			i++
		case "--title":
			title = args[i+1]
			i++
		case "--summary":
			summary = args[i+1]
			i++
		case "--severity":
			severity = args[i+1]
			i++
		case "--root-claim":
			rootClaim = args[i+1]
			i++
		case "--created-by":
			createdBy = args[i+1]
			i++
		default:
			exitWithMnemosError(false, NewUserError("unknown flag %q", args[i]))
			return
		}
	}
	if strings.TrimSpace(title) == "" {
		exitWithMnemosError(false, NewUserError("--title is required"))
		return
	}
	if id == "" {
		gen, err := newID("inc_")
		if err != nil {
			exitWithMnemosError(false, NewSystemError(err, "generate incident id"))
			return
		}
		id = gen
	}
	inc := domain.Incident{
		ID:               id,
		Title:            title,
		Summary:          summary,
		Severity:         domain.IncidentSeverity(severity),
		Status:           domain.IncidentStatusOpen,
		RootCauseClaimID: rootClaim,
		OpenedAt:         time.Now().UTC(),
		CreatedBy:        createdBy,
	}
	ctx := context.Background()
	w, err := openWriter(ctx)
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "open store"))
		return
	}
	defer closeWriter(w)
	if _, err := w.Incident(ctx, inc); err != nil {
		exitWithMnemosError(false, NewSystemError(err, "upsert incident"))
		return
	}
	emitJSON(map[string]any{
		"id":       inc.ID,
		"title":    inc.Title,
		"severity": inc.Severity,
		"status":   inc.Status,
	})
}

// handleIncidentClose implements `mnemos incident close <id>`.
func handleIncidentClose(args []string) {
	if len(args) == 0 {
		exitWithMnemosError(false, NewUserError("usage: mnemos incident close <id>"))
		return
	}
	ctx := context.Background()
	w, err := openWriter(ctx)
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "open store"))
		return
	}
	defer closeWriter(w)
	if err := w.ResolveIncident(ctx, args[0], time.Now().UTC()); err != nil {
		exitWithMnemosError(false, NewSystemError(err, "resolve incident"))
		return
	}
	emitJSON(map[string]string{"id": args[0], "status": string(domain.IncidentStatusResolved)})
}

// handleIncidentShow implements `mnemos incident show <id>`.
func handleIncidentShow(args []string) {
	if len(args) == 0 {
		exitWithMnemosError(false, NewUserError("usage: mnemos incident show <id>"))
		return
	}
	ctx := context.Background()
	conn, err := openConn(ctx)
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "open store"))
		return
	}
	defer closeConn(conn)
	inc, found, err := conn.Incidents.GetByID(ctx, args[0])
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "get incident"))
		return
	}
	if !found {
		exitWithMnemosError(false, NewUserError("incident %q not found", args[0]))
		return
	}
	emitJSON(inc)
}

// handleIncidentList implements `mnemos incident list [--severity <s>] [--status <s>]`.
func handleIncidentList(args []string) {
	var severity, status string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--severity":
			severity = args[i+1]
			i++
		case "--status":
			status = args[i+1]
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
	var incidents []domain.Incident
	switch {
	case severity != "":
		incidents, err = conn.Incidents.ListBySeverity(ctx, domain.IncidentSeverity(severity))
	case status != "":
		incidents, err = conn.Incidents.ListByStatus(ctx, domain.IncidentStatus(status))
	default:
		incidents, err = conn.Incidents.ListAll(ctx)
	}
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "list incidents"))
		return
	}
	emitJSON(incidents)
}
