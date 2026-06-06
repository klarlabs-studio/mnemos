package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/markdown"
	"go.klarlabs.de/mnemos/internal/query"
	"go.klarlabs.de/mnemos/internal/store"
)

// handleExport routes `mnemos export --kind=lesson|playbook|claim [--id=ID]`.
// For claims, pass --provenance to enrich the export with a full trust-score
// breakdown (requires a live engine query).
// Default writes to stdout; --out <path> writes to a file.
func handleExport(args []string, _ Flags) {
	var kind, id, out string
	var withProvenance bool
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--kind":
			kind = args[i+1]
			i++
		case "--id":
			id = args[i+1]
			i++
		case "--out":
			out = args[i+1]
			i++
		case "--provenance":
			withProvenance = true
		default:
			exitWithMnemosError(false, NewUserError("unknown flag %q", args[i]))
			return
		}
	}
	if strings.TrimSpace(kind) == "" || strings.TrimSpace(id) == "" {
		exitWithMnemosError(false, NewUserError("--kind and --id are required"))
		return
	}
	ctx := context.Background()
	conn, err := openConn(ctx)
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "open store"))
		return
	}
	defer closeConn(conn)
	var rendered string
	switch kind {
	case "lesson":
		l, err := conn.Lessons.GetByID(ctx, id)
		if err != nil {
			exitWithMnemosError(false, NewSystemError(err, "get lesson"))
			return
		}
		rendered, err = markdown.ExportLesson(l)
		if err != nil {
			exitWithMnemosError(false, NewSystemError(err, "render markdown"))
			return
		}
	case "playbook":
		p, err := conn.Playbooks.GetByID(ctx, id)
		if err != nil {
			exitWithMnemosError(false, NewSystemError(err, "get playbook"))
			return
		}
		rendered, err = markdown.ExportPlaybook(p)
		if err != nil {
			exitWithMnemosError(false, NewSystemError(err, "render markdown"))
			return
		}
	case "claim":
		rendered, err = handleExportClaim(ctx, conn, id, withProvenance)
		if err != nil {
			exitWithMnemosError(false, err)
			return
		}
	default:
		exitWithMnemosError(false, NewUserError("unknown kind %q (want lesson | playbook | claim)", kind))
		return
	}
	if out == "" {
		fmt.Print(rendered)
		return
	}
	if err := os.WriteFile(out, []byte(rendered), 0o600); err != nil { //nolint:gosec // G304: --out path is the operator's choice
		exitWithMnemosError(false, NewSystemError(err, "write file"))
		return
	}
	emitJSON(map[string]string{"path": out, "kind": kind, "id": id})
}

// handleExportClaim fetches a claim, optionally enriches it with a
// live provenance report, and renders it as markdown.
func handleExportClaim(ctx context.Context, conn *store.Conn, id string, withProvenance bool) (string, error) {
	claims, err := conn.Claims.ListByIDs(ctx, []string{id})
	if err != nil {
		return "", NewSystemError(err, "get claim %q", id)
	}
	if len(claims) == 0 {
		return "", NewUserError("claim %q not found", id)
	}
	c := claims[0]
	var report *domain.ProvenanceReport
	if withProvenance {
		eng := query.NewEngine(conn.Events, conn.Claims, conn.Relationships)
		r, pErr := eng.WhyTrustClaim(ctx, id)
		if pErr != nil {
			return "", NewSystemError(pErr, "provenance query for claim %q", id)
		}
		report = &r
	}
	rendered, err := markdown.ExportClaim(c, report)
	if err != nil {
		return "", NewSystemError(err, "render claim markdown")
	}
	return rendered, nil
}

// handleImport reads a markdown file and upserts it as the matching
// entity. Source defaults to "human" when the file has no source
// field — Mnemos treats hand-authored content as human-source so the
// trust formula can weight it differently from synthesised content.
func handleImport(args []string, _ Flags) {
	if len(args) == 0 {
		exitWithMnemosError(false, NewUserError("import requires a path"))
		return
	}
	path := args[0]
	data, err := os.ReadFile(path) //nolint:gosec // G304: caller-supplied path is the operator's choice
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "read file"))
		return
	}
	doc, err := markdown.Parse(string(data))
	if err != nil {
		exitWithMnemosError(false, NewUserError("parse markdown: %v", err))
		return
	}
	ctx := context.Background()
	conn, err := openConn(ctx)
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "open store"))
		return
	}
	defer closeConn(conn)
	switch doc.Kind {
	case "lesson":
		l := *doc.Lesson
		ensureMarkdownDefaults(&l, nil)
		if err := conn.Lessons.Append(ctx, l); err != nil {
			exitWithMnemosError(false, NewSystemError(err, "append lesson"))
			return
		}
		emitJSON(map[string]string{"kind": "lesson", "id": l.ID, "source": l.Source})
	case "playbook":
		p := *doc.Playbook
		ensureMarkdownDefaults(nil, &p)
		if err := conn.Playbooks.Append(ctx, p); err != nil {
			exitWithMnemosError(false, NewSystemError(err, "append playbook"))
			return
		}
		emitJSON(map[string]string{"kind": "playbook", "id": p.ID, "source": p.Source})
	default:
		exitWithMnemosError(false, NewUserError("unknown markdown kind %q", doc.Kind))
	}
}

// handleHistory routes `mnemos history --kind=lesson|playbook --id <id>`.
// Returns prior snapshots newest first.
func handleHistory(args []string, _ Flags) {
	var kind, id string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--kind":
			kind = args[i+1]
			i++
		case "--id":
			id = args[i+1]
			i++
		default:
			exitWithMnemosError(false, NewUserError("unknown flag %q", args[i]))
			return
		}
	}
	if strings.TrimSpace(kind) == "" || strings.TrimSpace(id) == "" {
		exitWithMnemosError(false, NewUserError("--kind and --id are required"))
		return
	}
	ctx := context.Background()
	conn, err := openConn(ctx)
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "open store"))
		return
	}
	defer closeConn(conn)
	switch kind {
	case "lesson":
		vs, err := conn.Lessons.ListVersions(ctx, id)
		if err != nil {
			exitWithMnemosError(false, NewSystemError(err, "list lesson versions"))
			return
		}
		emitJSON(vs)
	case "playbook":
		vs, err := conn.Playbooks.ListVersions(ctx, id)
		if err != nil {
			exitWithMnemosError(false, NewSystemError(err, "list playbook versions"))
			return
		}
		emitJSON(vs)
	default:
		exitWithMnemosError(false, NewUserError("unknown kind %q", kind))
	}
}

// ensureMarkdownDefaults backfills the fields a hand-authored
// markdown file may legitimately omit. Confidence defaults to 0.6
// (above the synthesis floor of 0.55 so a hand-authored entry
// surfaces in queries) and timestamps default to now.
func ensureMarkdownDefaults(l *domain.Lesson, p *domain.Playbook) {
	now := time.Now().UTC()
	if l != nil {
		if l.Source == "" {
			l.Source = "human"
		}
		if l.Confidence == 0 {
			l.Confidence = 0.6
		}
		if l.DerivedAt.IsZero() {
			l.DerivedAt = now
		}
		if len(l.Evidence) == 0 {
			// Validate requires at least one evidence id; humans who
			// hand-author lessons rarely cite raw action ids, so we
			// accept "human" as a placeholder marking provenance.
			l.Evidence = []string{"human"}
		}
	}
	if p != nil {
		if p.Source == "" {
			p.Source = "human"
		}
		if p.Confidence == 0 {
			p.Confidence = 0.6
		}
		if p.DerivedAt.IsZero() {
			p.DerivedAt = now
		}
	}
}
