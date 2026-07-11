package main

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"go.klarlabs.de/mnemos/internal/consolidate"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
)

// handlePromote runs ADR 0011 Phase B — the tenant→global promotion pass — as a
// one-shot CLI command:
//
//	mnemos consolidate --promote [--min-tenants N] [--gate auto|operator]
//	                   [--tenant-dsn <dsn> ...] [--global-dsn <dsn>]
//	                   [--sensitive <token> ...] [--dry-run | --apply]
//
//	mnemos consolidate --promote approve <id> --global-dsn <dsn>
//
// It reads the synthesized Schemas (Lessons) from each supplied tenant store,
// runs the pure promotion engine (internal/consolidate) with its five gates, and
// emits a structured, auditable plan as JSON.
//
// Writing is OPT-IN. --dry-run (the default) never writes: an operator inspects
// the plan first. --apply persists the surviving candidates to the global
// (neocortex) store identified by --global-dsn: under GateAuto they land Active
// (in force); under GateOperator they land Pending, awaiting an explicit
// `--promote approve <id>` to activate. Only de-identified fields (statement,
// scope, polarity, corroboration/evidence counts, confidence, surprise) ever
// cross into the global store — see consolidate.PromotedLesson.ToGlobalSchema.
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

	// Operator approval sub-verb: activate a single pending global record.
	if opts.approveID != "" {
		handlePromoteApprove(ctx, opts)
		return
	}

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

	// Prediction-error ranking signal: the DIRECT Lesson→Expectation edge —
	// a lesson's backing actions → the decisions/claims those actions produced
	// → the Expectation on those claims → the peak surprise (see
	// newStoreSurpriseSource).
	surprise, err := newStoreSurpriseSource(ctx, dsns)
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "load prediction-error signal"))
		return
	}

	// Resolve the display default so the audit output reflects the effective
	// threshold the engine will apply (the engine defaults MinTenants internally).
	if opts.engine.MinTenants <= 0 {
		opts.engine.MinTenants = consolidate.DefaultMinTenants
	}

	p := consolidate.NewPromoter(global, surprise)
	res, err := p.Promote(ctx, tenants, opts.engine)
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "promote"))
		return
	}

	out := map[string]any{
		"tenants_scanned": len(tenants),
		"min_tenants":     opts.engine.MinTenants,
		"gate":            string(opts.engine.Gate),
		"dry_run":         !opts.apply,
		"promoted":        res.Promoted,
		"pending":         res.Pending,
		"dissonant":       res.Dissonant,
		"skipped":         res.Skipped,
	}

	// --apply persists the surviving candidates to the neocortex store.
	if opts.apply {
		written, err := applyPromotion(ctx, opts.globalDSN, res)
		if err != nil {
			exitWithMnemosError(false, err)
			return
		}
		out["applied"] = true
		out["written_active"] = written.active
		out["written_pending"] = written.pending
		out["global_dsn"] = opts.globalDSN
	}

	emitJSON(out)
}

// writeCounts records how many global records a promotion apply wrote.
type writeCounts struct {
	active  int
	pending int
}

// applyPromotion persists a promotion Result to the global (neocortex) store.
// GateAuto survivors (res.Promoted) are written Active; GateOperator survivors
// (res.Pending) are written Pending. Dissonant and skipped candidates are never
// written. Only de-identified fields cross (via ToGlobalSchema).
func applyPromotion(ctx context.Context, globalDSN string, res consolidate.Result) (writeCounts, error) {
	var wc writeCounts
	if globalDSN == "" {
		return wc, NewUserError("--apply requires --global-dsn (the neocortex store to write to)")
	}
	conn, err := store.Open(ctx, globalDSN)
	if err != nil {
		return wc, NewSystemError(err, "open global store %q", globalDSN)
	}
	defer func() { _ = conn.Close() }()
	if conn.GlobalSchemas == nil {
		return wc, NewUserError("global store %q does not support promoted-schema persistence", globalDSN)
	}

	now := time.Now().UTC()
	write := func(cands []consolidate.PromotedLesson, status domain.GlobalSchemaStatus) error {
		for _, c := range cands {
			gs := c.ToGlobalSchema(status, now, domain.SystemUser)
			if err := conn.GlobalSchemas.Upsert(ctx, gs); err != nil {
				return NewSystemError(err, "persist global schema %q", gs.ID)
			}
		}
		return nil
	}
	if err := write(res.Promoted, domain.GlobalSchemaStatusActive); err != nil {
		return wc, err
	}
	if err := write(res.Pending, domain.GlobalSchemaStatusPending); err != nil {
		return wc, err
	}
	wc.active = len(res.Promoted)
	wc.pending = len(res.Pending)
	return wc, nil
}

// handlePromoteApprove activates a single pending global schema by id.
func handlePromoteApprove(ctx context.Context, opts promoteOpts) {
	if opts.globalDSN == "" {
		exitWithMnemosError(false, NewUserError("approve requires --global-dsn"))
		return
	}
	conn, err := store.Open(ctx, opts.globalDSN)
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "open global store %q", opts.globalDSN))
		return
	}
	defer func() { _ = conn.Close() }()
	if conn.GlobalSchemas == nil {
		exitWithMnemosError(false, NewUserError("global store %q does not support promoted-schema persistence", opts.globalDSN))
		return
	}
	if err := conn.GlobalSchemas.Approve(ctx, opts.approveID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			exitWithMnemosError(false, NewUserError("no global schema with id %q", opts.approveID))
			return
		}
		exitWithMnemosError(false, NewSystemError(err, "approve global schema %q", opts.approveID))
		return
	}
	emitJSON(map[string]any{
		"approved":   opts.approveID,
		"status":     string(domain.GlobalSchemaStatusActive),
		"global_dsn": opts.globalDSN,
	})
}

type promoteOpts struct {
	engine     consolidate.Options
	tenantDSNs []string
	globalDSN  string
	apply      bool
	approveID  string
}

func parsePromoteOpts(args []string, _ Flags) (promoteOpts, error) {
	out := promoteOpts{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--promote":
			// consumed as the mode selector; no value.
		case "approve":
			// Sub-verb: `--promote approve <id>`. The next positional arg is
			// the id of the global schema to activate.
			if i+1 >= len(args) {
				return out, NewUserError("approve requires a global schema id")
			}
			out.approveID = args[i+1]
			i++
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
		case "--apply":
			out.apply = true
		case "--dry-run":
			// Default; accepted for symmetry and explicitness.
			out.apply = false
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

// directSurpriseSource is a store-backed consolidate.SurpriseSource that ranks
// promotion candidates by the DIRECT Lesson→Expectation prediction-error edge.
//
// Linkage (the real model path, replacing the earlier scope-aggregation proxy):
//
//	Schema.Evidence  → action ids
//	Outcome.ActionID → the outcomes those actions produced
//	Decision.OutcomeID + Decision.Beliefs → the decisions those outcomes drove
//	                    and the belief claim ids that justified them
//	Expectation(ClaimID) → the forward prediction on each belief claim, whose
//	                       reconciled Surprise() is the prediction-error scalar
//
// The peak (max) surprise reachable from a lesson's backing actions is its
// prediction-error weight. byActionID caches that peak per action id (the join
// key on Schema.Evidence), so SurpriseFor is an O(evidence) lookup. A lesson
// none of whose actions reach a resolved expectation returns hasData=false and
// falls back to corroboration-count ranking in the engine.
type directSurpriseSource struct {
	byActionID map[string]float64
}

func (s *directSurpriseSource) SurpriseFor(_ context.Context, lesson domain.Lesson) (float64, bool) {
	peak, has := 0.0, false
	for _, actionID := range lesson.Evidence {
		v, ok := s.byActionID[actionID]
		if !ok {
			continue
		}
		if !has || v > peak {
			peak, has = v, true
		}
	}
	return peak, has
}

// newStoreSurpriseSource builds a directSurpriseSource by resolving, across every
// tenant store, the direct action→outcome→decision→belief→expectation path and
// recording the peak reconciled surprise reachable from each action id. Stores
// lacking the necessary repositories simply contribute no signal (never an
// error), and action ids are globally unique, so keying the merged map by action
// id keeps each tenant's signal correctly attributed.
func newStoreSurpriseSource(ctx context.Context, dsns []string) (consolidate.SurpriseSource, error) {
	byAction := map[string]float64{}
	for _, dsn := range dsns {
		conn, err := store.Open(ctx, dsn)
		if err != nil {
			return nil, err
		}
		if err := accumulateActionSurprise(ctx, conn, byAction); err != nil {
			_ = conn.Close()
			return nil, err
		}
		_ = conn.Close()
	}
	return &directSurpriseSource{byActionID: byAction}, nil
}

// accumulateActionSurprise walks one tenant store's decision/outcome/expectation
// graph and records, into byAction, the peak reconciled surprise reachable from
// each action id via the direct Lesson→Expectation edge. It is defensive: a
// backend missing decisions, outcomes, or expectations contributes nothing.
func accumulateActionSurprise(ctx context.Context, conn *store.Conn, byAction map[string]float64) error {
	if conn.Decisions == nil || conn.Outcomes == nil || conn.Expectations == nil {
		return nil
	}

	// outcomeID → belief claim ids of the decision that consumed that outcome.
	decisions, err := conn.Decisions.ListAll(ctx)
	if err != nil {
		return err
	}
	outcomeToBeliefs := map[string][]string{}
	for _, d := range decisions {
		if d.OutcomeID == "" || len(d.Beliefs) == 0 {
			continue
		}
		outcomeToBeliefs[d.OutcomeID] = append(outcomeToBeliefs[d.OutcomeID], d.Beliefs...)
	}
	if len(outcomeToBeliefs) == 0 {
		return nil
	}

	// claimID → reconciled surprise (memoised; only claims that back a decision
	// are ever queried).
	claimSurprise := map[string]float64{}
	surpriseFor := func(claimID string) (float64, bool, error) {
		if v, ok := claimSurprise[claimID]; ok {
			return v, true, nil
		}
		exp, ok, err := conn.Expectations.Get(ctx, claimID)
		if err != nil {
			return 0, false, err
		}
		if !ok {
			return 0, false, nil
		}
		s, meaningful := exp.Surprise()
		if !meaningful {
			return 0, false, nil
		}
		claimSurprise[claimID] = s
		return s, true, nil
	}

	outcomes, err := conn.Outcomes.ListAll(ctx)
	if err != nil {
		return err
	}
	for _, o := range outcomes {
		beliefs, ok := outcomeToBeliefs[o.ID]
		if !ok {
			continue
		}
		peak, has := 0.0, false
		for _, claimID := range beliefs {
			s, ok, err := surpriseFor(claimID)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			if !has || s > peak {
				peak, has = s, true
			}
		}
		if !has {
			continue
		}
		if cur, ok := byAction[o.ActionID]; !ok || peak > cur {
			byAction[o.ActionID] = peak
		}
	}
	return nil
}
