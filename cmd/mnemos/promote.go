package main

import (
	"context"
	"strconv"

	"go.klarlabs.de/mnemos/internal/consolidate"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
)

// handlePromote runs ADR 0011 Phase B — the tenant→global promotion pass — as a
// one-shot CLI command:
//
//	mnemos consolidate --promote [--min-tenants N] [--gate auto|operator]
//	                   [--tenant-dsn <dsn> ...] [--global-dsn <dsn>]
//	                   [--sensitive <token> ...] [--dry-run]
//
// It reads the synthesized Lessons from each supplied tenant store, runs the
// pure promotion engine (internal/consolidate) with its five gates, and emits a
// structured, auditable plan as JSON. It is READ-ONLY: no promoted lesson is
// written to any global store by this command — the safe posture is that an
// operator inspects the plan (and, under GateOperator, the pending set) before
// any global write. --dry-run is accepted and always effectively on; it is kept
// for symmetry with the rest of `consolidate` and to make the read-only intent
// explicit.
//
// Scope note: reading live multi-tenant lessons through one server's per-request
// tenant scoping (serve/mcp) is a larger wiring job; this command instead
// operates over an explicit set of tenant store DSNs/namespaces, which is the
// natural federation input for an offline consolidation ("sleep") pass and keeps
// the privacy-critical engine fully exercised end-to-end.
func handlePromote(args []string, f Flags) {
	opts, err := parsePromoteOpts(args, f)
	if err != nil {
		exitWithMnemosError(false, err)
		return
	}

	ctx := context.Background()

	// Load per-tenant lessons. With no --tenant-dsn provided, fall back to the
	// default store as a single tenant — which, by the corroboration gate, can
	// never promote anything (it demonstrates the no-leak floor rather than
	// erroring).
	dsns := opts.tenantDSNs
	if len(dsns) == 0 {
		dsns = []string{resolveDSN()}
	}

	var tenants []consolidate.TenantLessons
	for _, dsn := range dsns {
		conn, err := store.Open(ctx, dsn)
		if err != nil {
			exitWithMnemosError(false, NewSystemError(err, "open tenant store %q", dsn))
			return
		}
		lessons, err := conn.Lessons.ListAll(ctx)
		_ = conn.Close()
		if err != nil {
			exitWithMnemosError(false, NewSystemError(err, "list lessons for %q", dsn))
			return
		}
		tenants = append(tenants, consolidate.TenantLessons{Tenant: dsn, Lessons: lessons})
	}

	// Optional global-knowledge source for the contradiction gate.
	var global consolidate.GlobalKnowledge
	if opts.globalDSN != "" {
		global = &storeGlobalKnowledge{dsn: opts.globalDSN}
	}

	// Resolve the display default so the audit output reflects the effective
	// threshold the engine will apply (the engine defaults MinTenants internally).
	if opts.engine.MinTenants <= 0 {
		opts.engine.MinTenants = consolidate.DefaultMinTenants
	}

	p := consolidate.NewPromoter(global, nil)
	res, err := p.Promote(ctx, tenants, opts.engine)
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "promote"))
		return
	}

	emitJSON(map[string]any{
		"tenants_scanned": len(tenants),
		"min_tenants":     opts.engine.MinTenants,
		"gate":            string(opts.engine.Gate),
		"dry_run":         true,
		"promoted":        res.Promoted,
		"pending":         res.Pending,
		"dissonant":       res.Dissonant,
		"skipped":         res.Skipped,
	})
}

type promoteOpts struct {
	engine     consolidate.Options
	tenantDSNs []string
	globalDSN  string
}

func parsePromoteOpts(args []string, _ Flags) (promoteOpts, error) {
	out := promoteOpts{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--promote":
			// consumed as the mode selector; no value.
		case "--min-tenants":
			if i+1 >= len(args) {
				return out, NewUserError("--min-tenants requires a value")
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 1 {
				return out, NewUserError("--min-tenants must be a positive integer")
			}
			out.engine.MinTenants = n
			i++
		case "--gate":
			if i+1 >= len(args) {
				return out, NewUserError("--gate requires a value (auto|operator)")
			}
			switch args[i+1] {
			case "auto":
				out.engine.Gate = consolidate.GateAuto
			case "operator":
				out.engine.Gate = consolidate.GateOperator
			default:
				return out, NewUserError("--gate must be auto or operator")
			}
			i++
		case "--tenant-dsn":
			if i+1 >= len(args) {
				return out, NewUserError("--tenant-dsn requires a value")
			}
			out.tenantDSNs = append(out.tenantDSNs, args[i+1])
			i++
		case "--global-dsn":
			if i+1 >= len(args) {
				return out, NewUserError("--global-dsn requires a value")
			}
			out.globalDSN = args[i+1]
			i++
		case "--sensitive":
			if i+1 >= len(args) {
				return out, NewUserError("--sensitive requires a value")
			}
			out.engine.SensitiveTokens = append(out.engine.SensitiveTokens, args[i+1])
			i++
		case "--dry-run":
			// Always effectively on; accepted for symmetry.
		default:
			return out, NewUserError("unknown flag %q", args[i])
		}
	}
	return out, nil
}

// storeGlobalKnowledge adapts a store DSN to the consolidate.GlobalKnowledge
// port. "Vetted" global claims are the active (in-force) claims of the global
// store; contested/deprecated claims are excluded so a stale belief does not
// mark a fresh cross-tenant candidate dissonant.
type storeGlobalKnowledge struct {
	dsn string
}

func (g *storeGlobalKnowledge) VettedClaims(ctx context.Context) ([]domain.Claim, error) {
	conn, err := store.Open(ctx, g.dsn)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	all, err := conn.Claims.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	vetted := make([]domain.Claim, 0, len(all))
	for _, c := range all {
		if c.Status == domain.ClaimStatusActive {
			vetted = append(vetted, c)
		}
	}
	return vetted, nil
}
