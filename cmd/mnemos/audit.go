package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"go.klarlabs.de/mnemos/internal/store"
)

// auditExport is the on-the-wire shape of `mnemos audit`. Top-level fields
// are explicitly listed so a downstream compliance tool can validate the
// document against a schema. The schema_version lets future changes break
// backwards compatibility deliberately rather than silently.
type auditExport struct {
	SchemaVersion string              `json:"schema_version"`
	GeneratedAt   string              `json:"generated_at"`
	DBPath        string              `json:"db_path"`
	Counts        auditCounts         `json:"counts"`
	Events        []eventDTO          `json:"episodes"`
	Claims        []claimDTO          `json:"beliefs"`
	Evidence      []claimEvidenceItem `json:"evidence"`
	Relationships []relationshipDTO   `json:"associations"`
	Embeddings    []embeddingDTO      `json:"embeddings,omitempty"`
}

type auditCounts struct {
	Events        int `json:"episodes"`
	Claims        int `json:"beliefs"`
	Evidence      int `json:"evidence"`
	Relationships int `json:"associations"`
	Embeddings    int `json:"embeddings"`
}

// auditSchemaVersion is bumped to v2 for the ADR-0011 brain-native wire rename:
// the top-level collections are now episodes/beliefs/associations (matching the
// brain-native edge fields belief_id/from_belief_id), a deliberate breaking
// change to the export format.
const auditSchemaVersion = "audit.v2"

// handleAudit dispatches `mnemos audit` and its subcommands.
//
// `mnemos audit` (no subcommand) keeps the legacy point-in-time
// snapshot for backups and compliance reviews. `mnemos audit who
// <id>` filters to a single principal's writes — the F.5 audit
// surface that closes the loop on per-actor governance.
func handleAudit(args []string, flags Flags) {
	if len(args) > 0 && args[0] == "who" {
		handleAuditWho(args[1:], flags)
		return
	}

	includeEmbeddings := false
	for _, a := range args {
		switch a {
		case "--include-embeddings":
			includeEmbeddings = true
		default:
			exitWithMnemosError(false, NewUserError("unknown audit flag %q", a))
			return
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	conn, err := openConn(ctx)
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "open database"))
		return
	}
	defer closeConn(conn)

	export, err := buildAuditExport(ctx, conn, resolveDSN(), includeEmbeddings)
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "build audit export"))
		return
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(export); err != nil {
		exitWithMnemosError(false, NewSystemError(err, "encode audit export"))
		return
	}
}

func buildAuditExport(ctx context.Context, conn *store.Conn, dbPath string, includeEmbeddings bool) (auditExport, error) {
	events, err := loadAllEventsForPush(ctx, conn)
	if err != nil {
		return auditExport{}, fmt.Errorf("load events: %w", err)
	}
	claims, evidence, err := loadAllClaimsForPush(ctx, conn)
	if err != nil {
		return auditExport{}, fmt.Errorf("load claims: %w", err)
	}
	rels, err := loadAllRelationshipsForPush(ctx, conn)
	if err != nil {
		return auditExport{}, fmt.Errorf("load relationships: %w", err)
	}

	export := auditExport{
		SchemaVersion: auditSchemaVersion,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		DBPath:        dbPath,
		Events:        events,
		Claims:        claims,
		Evidence:      evidence,
		Relationships: rels,
		Counts: auditCounts{
			Events:        len(events),
			Claims:        len(claims),
			Evidence:      len(evidence),
			Relationships: len(rels),
		},
	}

	if includeEmbeddings {
		embs, err := loadAllEmbeddingsForPush(ctx, conn)
		if err != nil {
			return auditExport{}, fmt.Errorf("load embeddings: %w", err)
		}
		export.Embeddings = embs
		export.Counts.Embeddings = len(embs)
	}

	return export, nil
}
