package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/store"
)

// auditWhoExport is the on-the-wire shape of `mnemos audit who`.
// Distinct from auditExport so the dump-everything snapshot's schema
// can evolve independently from the principal-scoped report.
type auditWhoExport struct {
	SchemaVersion string                 `json:"schema_version"`
	GeneratedAt   string                 `json:"generated_at"`
	Principal     string                 `json:"principal"`
	Since         string                 `json:"since,omitempty"`
	Counts        auditWhoCounts         `json:"counts"`
	Events        []auditWhoEvent        `json:"events"`
	Claims        []auditWhoClaim        `json:"claims"`
	Relationships []auditWhoRelationship `json:"relationships"`
	Embeddings    []auditWhoEmbedding    `json:"embeddings"`
	Transitions   []auditWhoTransition   `json:"status_transitions"`
}

type auditWhoCounts struct {
	Events        int `json:"events"`
	Claims        int `json:"claims"`
	Relationships int `json:"relationships"`
	Embeddings    int `json:"embeddings"`
	Transitions   int `json:"status_transitions"`
}

type auditWhoEvent struct {
	ID        string `json:"id"`
	RunID     string `json:"run_id"`
	Content   string `json:"content"`
	Timestamp string `json:"timestamp"`
}

type auditWhoClaim struct {
	ID         string  `json:"id"`
	Text       string  `json:"text"`
	Type       string  `json:"type"`
	Status     string  `json:"status"`
	Confidence float64 `json:"confidence"`
	CreatedAt  string  `json:"created_at"`
}

type auditWhoRelationship struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	FromClaimID string `json:"from_claim_id"`
	ToClaimID   string `json:"to_claim_id"`
	CreatedAt   string `json:"created_at"`
}

type auditWhoEmbedding struct {
	EntityID   string `json:"entity_id"`
	EntityType string `json:"entity_type"`
	Model      string `json:"model"`
	Dimensions int    `json:"dimensions"`
	CreatedAt  string `json:"created_at"`
}

type auditWhoTransition struct {
	ClaimID    string `json:"claim_id"`
	FromStatus string `json:"from_status"`
	ToStatus   string `json:"to_status"`
	ChangedAt  string `json:"changed_at"`
	Reason     string `json:"reason"`
}

const auditWhoSchemaVersion = "audit_who.v1"

// handleAuditWho implements `mnemos audit who <principal-id>
// [--since <duration>] [--human]`. The principal can be any string
// recorded in created_by / changed_by — a user id (usr_*), an agent
// id (agt_*), or the <system> sentinel.
func handleAuditWho(args []string, f Flags) {
	if len(args) == 0 {
		exitWithMnemosError(false, NewUserError("audit who <principal-id> [--since <duration>]"))
		return
	}
	principal := args[0]
	args = args[1:]

	var since time.Time
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--since":
			if i+1 >= len(args) {
				exitWithMnemosError(false, NewUserError("--since requires a duration like 24h"))
				return
			}
			d, err := time.ParseDuration(args[i+1])
			if err != nil {
				exitWithMnemosError(false, NewUserError("invalid --since: %v", err))
				return
			}
			since = time.Now().UTC().Add(-d)
			i++
		default:
			exitWithMnemosError(false, NewUserError("unknown audit who flag %q", args[i]))
			return
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := openConn(ctx)
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "open database"))
		return
	}
	defer closeConn(conn)

	export, err := buildAuditWhoExport(ctx, conn, principal, since)
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "build audit-who export"))
		return
	}

	if f.Human {
		printAuditWhoHuman(export)
		return
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(export); err != nil {
		exitWithMnemosError(false, NewSystemError(err, "encode audit-who export"))
		return
	}
}

// buildAuditWhoExport pulls every write-attributed table through
// ports and filters by principal (and optional since) in-process.
// In-memory filtering is fine at CLI scale and keeps the port
// surface small — every backend already implements ListAll.
func buildAuditWhoExport(ctx context.Context, conn *store.Conn, principal string, since time.Time) (auditWhoExport, error) {
	export := auditWhoExport{
		SchemaVersion: auditWhoSchemaVersion,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		Principal:     principal,
		Events:        []auditWhoEvent{},
		Claims:        []auditWhoClaim{},
		Relationships: []auditWhoRelationship{},
		Embeddings:    []auditWhoEmbedding{},
		Transitions:   []auditWhoTransition{},
	}
	if !since.IsZero() {
		export.Since = since.Format(time.RFC3339)
	}

	allEvents, err := conn.Events.ListAll(ctx)
	if err != nil {
		return export, fmt.Errorf("list events: %w", err)
	}
	for _, e := range allEvents {
		if e.CreatedBy != principal {
			continue
		}
		if !since.IsZero() && e.Timestamp.Before(since) {
			continue
		}
		export.Events = append(export.Events, auditWhoEvent{
			ID:        e.ID,
			RunID:     e.RunID,
			Content:   e.Content,
			Timestamp: e.Timestamp.UTC().Format(time.RFC3339Nano),
		})
	}

	allClaims, err := conn.Claims.ListAll(ctx)
	if err != nil {
		return export, fmt.Errorf("list claims: %w", err)
	}
	for _, c := range allClaims {
		if c.CreatedBy != principal {
			continue
		}
		if !since.IsZero() && c.CreatedAt.Before(since) {
			continue
		}
		export.Claims = append(export.Claims, auditWhoClaim{
			ID:         c.ID,
			Text:       c.Text,
			Type:       string(c.Type),
			Status:     string(c.Status),
			Confidence: c.Confidence,
			CreatedAt:  c.CreatedAt.UTC().Format(time.RFC3339Nano),
		})
	}

	allRels, err := conn.Relationships.ListAll(ctx)
	if err != nil {
		return export, fmt.Errorf("list relationships: %w", err)
	}
	for _, r := range allRels {
		if r.CreatedBy != principal {
			continue
		}
		if !since.IsZero() && r.CreatedAt.Before(since) {
			continue
		}
		export.Relationships = append(export.Relationships, auditWhoRelationship{
			ID:          r.ID,
			Type:        string(r.Type),
			FromClaimID: r.FromClaimID,
			ToClaimID:   r.ToClaimID,
			CreatedAt:   r.CreatedAt.UTC().Format(time.RFC3339Nano),
		})
	}

	allEmbeddings, err := conn.Embeddings.ListAll(ctx)
	if err != nil {
		return export, fmt.Errorf("list embeddings: %w", err)
	}
	for _, e := range allEmbeddings {
		if e.CreatedBy != principal {
			continue
		}
		if !since.IsZero() && e.CreatedAt.Before(since) {
			continue
		}
		export.Embeddings = append(export.Embeddings, auditWhoEmbedding{
			EntityID:   e.EntityID,
			EntityType: e.EntityType,
			Model:      e.Model,
			Dimensions: e.Dimensions,
			CreatedAt:  e.CreatedAt.UTC().Format(time.RFC3339Nano),
		})
	}

	allHistory, err := conn.Claims.ListAllStatusHistory(ctx)
	if err != nil {
		return export, fmt.Errorf("list status history: %w", err)
	}
	for _, t := range allHistory {
		if t.ChangedBy != principal {
			continue
		}
		if !since.IsZero() && t.ChangedAt.Before(since) {
			continue
		}
		export.Transitions = append(export.Transitions, auditWhoTransition{
			ClaimID:    t.ClaimID,
			FromStatus: string(t.FromStatus),
			ToStatus:   string(t.ToStatus),
			ChangedAt:  t.ChangedAt.UTC().Format(time.RFC3339Nano),
			Reason:     t.Reason,
		})
	}

	export.Counts = auditWhoCounts{
		Events:        len(export.Events),
		Claims:        len(export.Claims),
		Relationships: len(export.Relationships),
		Embeddings:    len(export.Embeddings),
		Transitions:   len(export.Transitions),
	}
	return export, nil
}

// printAuditWhoHuman renders the export as a chronological table.
// Sorting interleaves rows from different write surfaces by their
// natural timestamp so an operator reading top-to-bottom sees the
// agent's actual activity sequence, not five separate sub-reports.
func printAuditWhoHuman(e auditWhoExport) {
	type row struct {
		At   string
		Kind string
		ID   string
		Note string
	}
	var rows []row
	for _, ev := range e.Events {
		rows = append(rows, row{ev.Timestamp, "event", ev.ID, truncate(strings.ReplaceAll(ev.Content, "\n", " "), 60)})
	}
	for _, c := range e.Claims {
		rows = append(rows, row{c.CreatedAt, "claim", c.ID, fmt.Sprintf("[%s/%s] %s", c.Type, c.Status, truncate(c.Text, 50))})
	}
	for _, r := range e.Relationships {
		rows = append(rows, row{r.CreatedAt, "rel", r.ID, fmt.Sprintf("%s %s → %s", r.Type, r.FromClaimID, r.ToClaimID)})
	}
	for _, em := range e.Embeddings {
		rows = append(rows, row{em.CreatedAt, "embed", em.EntityID, fmt.Sprintf("%s/%dD %s", em.EntityType, em.Dimensions, em.Model)})
	}
	for _, t := range e.Transitions {
		rows = append(rows, row{t.ChangedAt, "status", t.ClaimID, fmt.Sprintf("%s → %s (%s)", t.FromStatus, t.ToStatus, t.Reason)})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].At < rows[j].At })

	fmt.Printf("audit: principal=%s", e.Principal)
	if e.Since != "" {
		fmt.Printf(" since=%s", e.Since)
	}
	fmt.Println()
	fmt.Printf("counts: events=%d claims=%d rels=%d embed=%d transitions=%d\n\n",
		e.Counts.Events, e.Counts.Claims, e.Counts.Relationships, e.Counts.Embeddings, e.Counts.Transitions)
	if len(rows) == 0 {
		fmt.Println("(no writes attributed to this principal)")
		return
	}
	fmt.Printf("%-30s %-8s %-26s %s\n", "AT", "KIND", "ID", "DETAILS")
	for _, r := range rows {
		fmt.Printf("%-30s %-8s %-26s %s\n", r.At, r.Kind, r.ID, r.Note)
	}
}
