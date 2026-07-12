package main

import (
	"context"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/consolidate"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
)

// bornGlobalCreatedBy marks a GlobalSchema written by the top-down born-global
// authoring surface (ADR 0012 §5). Unlike a bottom-up promoted schema — whose
// provenance is cross-tenant corroboration COUNTS — a born-global record is
// reference knowledge a curator authored straight into the neocortex; it never
// passed through a tenant, so there is nothing to de-identify. This sentinel in
// CreatedBy is the human-authorship flag (the domain.GlobalSchema type carries no
// dedicated source/kind field), keeping born-global rows auditable apart from
// promoted ones.
const bornGlobalCreatedBy = "<curator:born-global>"

// bornGlobalConfidence is the representative confidence recorded on an authored
// reference fact. A curator deliberately writing class-level reference knowledge
// (a taxonomy entry, an envenomation profile) is asserting it as authoritative,
// so it is written fully confident; the record can still be superseded by a later
// re-authoring (content-addressed upsert) or contradicted by promoted knowledge.
const bornGlobalConfidence = 1.0

// handleGlobal routes `mnemos global ...` — the top-down neocortex authoring
// surface (ADR 0012 §5). Its only sub-verb today is `author`, the born-global
// complement to the bottom-up promotion paths (`consolidate --promote`).
func handleGlobal(args []string, f Flags) {
	if len(args) == 0 {
		exitWithMnemosError(false, NewUserError("global requires a sub-command (author)"))
		return
	}
	switch args[0] {
	case "author":
		handleGlobalAuthor(args[1:], f)
	default:
		exitWithMnemosError(false, NewUserError("unknown global sub-command %q (want: author)", args[0]))
	}
}

// globalAuthorOpts holds the parsed flags for `mnemos global author`.
type globalAuthorOpts struct {
	statement string
	scope     domain.Scope
	polarity  domain.SchemaPolarity
	status    domain.GlobalSchemaStatus
	token     string
	globalDSN string
	apply     bool
}

// handleGlobalAuthor implements the born-global authoring surface (ADR 0012 §5):
//
//	mnemos global author --statement "<text>"
//	    [--scope-service S --scope-env E --scope-team T]
//	    [--polarity positive|negative] [--status active|pending]
//	    [--token <jwt>] [--global-dsn <dsn>] [--dry-run | --apply]
//
// It writes a curator-authored reference fact DIRECTLY into the shared neocortex
// (global) tier as a domain.GlobalSchema — knowledge about a class (a species, a
// disease, a taxonomy entry) that never passed through any tenant. Because it
// carries no tenant data there is nothing to de-identify; the only gate is the
// curator capability. It is the top-down complement to the bottom-up float-back
// paths, both of which write to the same neocortex store.
//
// Authoring the shared brain is a privileged, curated act, so it is gated behind
// the promote:global scope (domain.ScopePromoteGlobal): the supplied token
// (--token or MNEMOS_TOKEN) is verified — signature, expiry, revocation, and
// scope — BEFORE anything is written (fail closed, reusing verifyCuratorToken).
//
// Writing is OPT-IN. --dry-run (the default) prints the GlobalSchema it WOULD
// write as JSON and touches nothing. --apply persists it via the neocortex
// store's Upsert. The id is content-addressed from statement+scope+polarity
// (consolidate.GlobalSchemaID), so re-authoring the same fact upserts the same
// row rather than churning a new one.
func handleGlobalAuthor(args []string, _ Flags) {
	opts, err := parseGlobalAuthorOpts(args)
	if err != nil {
		exitWithMnemosError(false, err)
		return
	}

	ctx := context.Background()
	globalDSN := resolveCentralDSN(opts.globalDSN)

	// Curator gate (ADR 0012 §3): authoring into the shared brain requires a
	// token bearing promote:global. Verify BEFORE building or writing anything so
	// an unauthorized run does nothing (fail closed).
	if err := verifyCuratorToken(ctx, opts.token, globalDSN); err != nil {
		exitWithMnemosError(false, err)
		return
	}

	gs := opts.toGlobalSchema(time.Now().UTC())
	if err := gs.Validate(); err != nil {
		exitWithMnemosError(false, NewUserError("authored schema is invalid: %v", err))
		return
	}

	out := map[string]any{
		"dry_run":    !opts.apply,
		"global_dsn": redactDSN(globalDSN),
		"schema":     globalSchemaToDTO(gs),
	}

	if opts.apply {
		if err := applyGlobalAuthor(ctx, globalDSN, gs); err != nil {
			exitWithMnemosError(false, err)
			return
		}
		out["applied"] = true
	}

	emitJSON(out)
}

// toGlobalSchema builds the domain.GlobalSchema the command would write. The id
// is content-addressed via consolidate.GlobalSchemaID (statement+scope+polarity),
// identical to the promotion write path, so a re-author upserts the same row.
// DistinctTenants is 1 (the single curator source): the field is a magnitude that
// domain.GlobalSchema.Validate requires to be ≥ 1, and born-global data has no
// cross-tenant corroboration — the human-authorship provenance is carried by
// CreatedBy instead. EvidenceCount is 0 (no tenant evidence backs it).
func (o globalAuthorOpts) toGlobalSchema(at time.Time) domain.GlobalSchema {
	id := consolidate.GlobalSchemaID(consolidate.PromotedLesson{
		Statement: o.statement,
		Scope:     o.scope,
		Polarity:  o.polarity,
	})
	polarity := o.polarity
	if polarity == "" {
		polarity = domain.SchemaPolarityPositive
	}
	return domain.GlobalSchema{
		ID:              id,
		Statement:       o.statement,
		Scope:           o.scope,
		Polarity:        polarity,
		DistinctTenants: 1,
		EvidenceCount:   0,
		Confidence:      bornGlobalConfidence,
		Status:          o.status,
		PromotedAt:      at,
		CreatedBy:       bornGlobalCreatedBy,
	}
}

// applyGlobalAuthor persists an authored GlobalSchema to the neocortex store.
func applyGlobalAuthor(ctx context.Context, globalDSN string, gs domain.GlobalSchema) error {
	conn, err := store.Open(ctx, globalDSN)
	if err != nil {
		return NewSystemError(err, "open global store %q", redactDSN(globalDSN))
	}
	defer func() { _ = conn.Close() }()
	if conn.GlobalSchemas == nil {
		return NewUserError("global store %q does not support neocortex persistence", redactDSN(globalDSN))
	}
	if err := conn.GlobalSchemas.Upsert(ctx, gs); err != nil {
		return NewSystemError(err, "author global schema %q", gs.ID)
	}
	return nil
}

func parseGlobalAuthorOpts(args []string) (globalAuthorOpts, error) {
	out := globalAuthorOpts{status: domain.GlobalSchemaStatusActive}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--statement":
			if i+1 >= len(args) {
				return out, NewUserError("--statement requires a value")
			}
			out.statement = args[i+1]
			i++
		case "--scope-service":
			if i+1 >= len(args) {
				return out, NewUserError("--scope-service requires a value")
			}
			out.scope.Service = args[i+1]
			i++
		case "--scope-env":
			if i+1 >= len(args) {
				return out, NewUserError("--scope-env requires a value")
			}
			out.scope.Env = args[i+1]
			i++
		case "--scope-team":
			if i+1 >= len(args) {
				return out, NewUserError("--scope-team requires a value")
			}
			out.scope.Team = args[i+1]
			i++
		case "--polarity":
			if i+1 >= len(args) {
				return out, NewUserError("--polarity requires a value (positive|negative)")
			}
			switch args[i+1] {
			case "positive":
				out.polarity = domain.SchemaPolarityPositive
			case "negative":
				out.polarity = domain.SchemaPolarityNegative
			default:
				return out, NewUserError("--polarity must be positive or negative")
			}
			i++
		case "--status":
			if i+1 >= len(args) {
				return out, NewUserError("--status requires a value (active|pending)")
			}
			switch args[i+1] {
			case string(domain.GlobalSchemaStatusActive):
				out.status = domain.GlobalSchemaStatusActive
			case string(domain.GlobalSchemaStatusPending):
				out.status = domain.GlobalSchemaStatusPending
			default:
				return out, NewUserError("--status must be active or pending")
			}
			i++
		case "--token":
			if i+1 >= len(args) {
				return out, NewUserError("--token requires a value")
			}
			out.token = args[i+1]
			i++
		case "--global-dsn":
			if i+1 >= len(args) {
				return out, NewUserError("--global-dsn requires a value")
			}
			out.globalDSN = args[i+1]
			i++
		case "--apply":
			out.apply = true
		case "--dry-run":
			// Default; accepted for symmetry and explicitness.
			out.apply = false
		default:
			return out, NewUserError("unknown flag %q", args[i])
		}
	}
	if strings.TrimSpace(out.statement) == "" {
		return out, NewUserError("global author requires --statement <text>")
	}
	return out, nil
}
