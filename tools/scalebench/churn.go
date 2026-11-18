package main

// Storage growth and maintenance cost (-churn).
//
// The latency operations in main.go answer "how slow is this at N beliefs".
// This file answers the two questions they cannot:
//
//   - How much does a brain cost to KEEP? On-disk bytes and per-table rows
//     after seeding, normalised per belief and per episode, and then how much
//     each table — the history tables above all — grows per operation over a
//     period of churn. A history table that grows on a READ is a defect (a
//     write on every recall); one whose per-operation growth rises with brain
//     size is super-linear. Both are visible in the phase deltas.
//   - How much does a brain cost to MAINTAIN? A full consolidation pass, a
//     retraction with its impact report, an un-retraction, the full trust
//     recompute after churn, and a recorded-as-of recall next to a live one
//     (the cost of time travel). These land in Report.Operations so the gate
//     can hold ceilings on them like any other operation.
//
// The census reads the SQLite file directly over its own connection. Row
// counts see inserts and deletes; PRAGMA data_version on that connection
// changes when any other connection commits, so it also catches a read path
// that UPDATES rows without adding any.

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strings"
	"time"

	"go.klarlabs.de/mnemos"
	"go.klarlabs.de/mnemos/internal/scalebench"
	"go.klarlabs.de/mnemos/internal/store"

	_ "modernc.org/sqlite" // the census connection
)

// historyTables are the append-only tables whose growth the churn tracks
// explicitly. Every other table is still censused; these are the ones a
// per-operation budget is stated for.
var historyTables = []string{
	"claim_status_history", "claim_validity_history", "claim_credit_history",
	"claim_trust_input_history", "closed_claim_evidence", "closed_relationships",
	"cognitive_journal",
}

type storageReport struct {
	AfterSeed  storageCensus `json:"after_seed"`
	AfterChurn storageCensus `json:"after_churn"`
	Phases     []churnPhase  `json:"phases"`
	// Findings names every read phase that committed a write or grew a table,
	// and every history table whose growth per operation exceeds its budget.
	// Empty means none was observed.
	Findings []string `json:"findings"`
}

type storageCensus struct {
	// DBBytes is the main file plus the WAL, what the brain occupies on disk.
	DBBytes int64 `json:"db_bytes"`
	// PageBytes is page_count × page_size of the main file.
	PageBytes       int64                  `json:"page_bytes"`
	Beliefs         int64                  `json:"beliefs"`
	Episodes        int64                  `json:"episodes"`
	BytesPerBelief  float64                `json:"bytes_per_belief"`
	BytesPerEpisode float64                `json:"bytes_per_episode"`
	Tables          map[string]tableCensus `json:"tables"`
}

type tableCensus struct {
	Rows int64 `json:"rows"`
	// Bytes is the table plus its indexes (dbstat); 0 when dbstat is
	// unavailable.
	Bytes         int64   `json:"bytes,omitempty"`
	RowsPerBelief float64 `json:"rows_per_belief"`
}

type churnPhase struct {
	Name string `json:"name"`
	// Kind is read, recall, write, setup or maintenance. A read phase must
	// not commit anything. A recall phase may update rows in place (live
	// recall's Hebbian, reconsolidation and inhibition write-backs, ADR 0015
	// §4/§5 and ADR 0016) but must not add any.
	Kind      string  `json:"kind"`
	Ops       int     `json:"ops"`
	ElapsedMS float64 `json:"elapsed_ms"`
	// Committed reports whether any other connection committed during the
	// phase (PRAGMA data_version moved).
	Committed bool `json:"committed"`
	// Growth is the row delta of every table that changed.
	Growth map[string]int64 `json:"growth,omitempty"`
	// PerOp is Growth / Ops for the history tables that changed.
	PerOp  map[string]float64 `json:"per_op,omitempty"`
	Detail string             `json:"detail,omitempty"`
	Error  string             `json:"error,omitempty"`
	// mustNotGrow names tables the phase, by the contract of what it does,
	// must not add rows to (a restatement of a held belief adds evidence,
	// never a second belief).
	mustNotGrow []string
}

// censusDB is the census's own connection to the brain's SQLite file. One
// connection, so data_version is read on the same connection every time.
type censusDB struct {
	db     *sql.DB
	path   string
	dbstat bool
}

func openCensus(path string) (*censusDB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	c := &censusDB{db: db, path: path}
	var n int64
	if err := db.QueryRow(`SELECT count(*) FROM dbstat`).Scan(&n); err != nil {
		fmt.Fprintln(os.Stderr, "scalebench: dbstat unavailable, per-table bytes omitted:", err)
	} else {
		c.dbstat = true
	}
	return c, nil
}

func (c *censusDB) close() { _ = c.db.Close() }

func (c *censusDB) dataVersion() int64 {
	var v int64
	_ = c.db.QueryRow(`PRAGMA data_version`).Scan(&v)
	return v
}

func (c *censusDB) tables() ([]string, error) {
	rows, err := c.db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (c *censusDB) rowCounts() (map[string]int64, error) {
	names, err := c.tables()
	if err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(names))
	for _, n := range names {
		var k int64
		// Table names come from sqlite_master, not from input.
		if err := c.db.QueryRow(`SELECT count(*) FROM "` + n + `"`).Scan(&k); err != nil { //nolint:gosec // G202: name from sqlite_master
			return nil, fmt.Errorf("count %s: %w", n, err)
		}
		out[n] = k
	}
	return out, nil
}

// tableBytes sums dbstat page sizes per table, folding each index into the
// table it indexes. FTS shadow tables keep their own names.
func (c *censusDB) tableBytes() map[string]int64 {
	out := map[string]int64{}
	if !c.dbstat {
		return out
	}
	rows, err := c.db.Query(`SELECT COALESCE(m.tbl_name, d.name), SUM(d.pgsize)
		FROM dbstat d LEFT JOIN sqlite_master m ON m.name = d.name GROUP BY 1`)
	if err != nil {
		fmt.Fprintln(os.Stderr, "scalebench: dbstat:", err)
		return out
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var n string
		var b int64
		if err := rows.Scan(&n, &b); err != nil {
			fmt.Fprintln(os.Stderr, "scalebench: dbstat:", err)
			continue
		}
		out[n] = b
	}
	return out
}

func (c *censusDB) census() (storageCensus, error) {
	counts, err := c.rowCounts()
	if err != nil {
		return storageCensus{}, err
	}
	bytes := c.tableBytes()
	s := storageCensus{Tables: map[string]tableCensus{}, Beliefs: counts["claims"], Episodes: counts["events"]}
	var pages, size int64
	_ = c.db.QueryRow(`PRAGMA page_count`).Scan(&pages)
	_ = c.db.QueryRow(`PRAGMA page_size`).Scan(&size)
	s.PageBytes = pages * size
	s.DBBytes = fileBytes(c.path)
	if s.Beliefs > 0 {
		s.BytesPerBelief = round2(float64(s.DBBytes) / float64(s.Beliefs))
	}
	if s.Episodes > 0 {
		s.BytesPerEpisode = round2(float64(s.DBBytes) / float64(s.Episodes))
	}
	for n, rows := range counts {
		t := tableCensus{Rows: rows, Bytes: bytes[n]}
		if s.Beliefs > 0 {
			t.RowsPerBelief = round4(float64(rows) / float64(s.Beliefs))
		}
		s.Tables[n] = t
	}
	return s, nil
}

func fileBytes(path string) int64 {
	var total int64
	for _, p := range []string{path, path + "-wal"} {
		if fi, err := os.Stat(p); err == nil {
			total += fi.Size()
		}
	}
	return total
}

func round4(v float64) float64 { return float64(int64(v*10000+0.5)) / 10000 }

// phaseRunner measures one churn phase: its wall time, whether anything
// committed, and the row delta of every table.
type phaseRunner struct {
	c      *censusDB
	phases []churnPhase
}

func (pr *phaseRunner) run(name, kind string, fn func(ctx context.Context) (ops int, detail string, err error)) {
	pr.runGuarded(name, kind, nil, fn)
}

// runGuarded is run with tables the phase must not add rows to.
func (pr *phaseRunner) runGuarded(name, kind string, mustNotGrow []string, fn func(ctx context.Context) (ops int, detail string, err error)) {
	before, err := pr.c.rowCounts()
	if err != nil {
		pr.phases = append(pr.phases, churnPhase{Name: name, Kind: kind, Error: err.Error()})
		return
	}
	v0 := pr.c.dataVersion()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	start := time.Now()
	ops, detail, ferr := fn(ctx)
	elapsed := time.Since(start)
	cancel()
	ph := churnPhase{Name: name, Kind: kind, Ops: ops, ElapsedMS: ms(elapsed), Detail: detail, Committed: pr.c.dataVersion() != v0, mustNotGrow: mustNotGrow}
	if ferr != nil {
		ph.Error = ferr.Error()
	}
	after, err := pr.c.rowCounts()
	if err == nil {
		ph.Growth, ph.PerOp = growth(before, after, ops)
	}
	pr.phases = append(pr.phases, ph)
	fmt.Fprintf(os.Stderr, "  churn %-28s %5d ops %10.1f ms committed=%v %v\n", name, ops, ph.ElapsedMS, ph.Committed, ph.Growth)
}

// growth returns the non-zero row deltas, and for history tables the delta
// per operation.
func growth(before, after map[string]int64, ops int) (map[string]int64, map[string]float64) {
	g := map[string]int64{}
	per := map[string]float64{}
	for n, a := range after {
		if d := a - before[n]; d != 0 {
			g[n] = d
			if ops > 0 && slices.Contains(historyTables, n) {
				per[n] = round4(float64(d) / float64(ops))
			}
		}
	}
	if len(g) == 0 {
		g = nil
	}
	if len(per) == 0 {
		per = nil
	}
	return g, per
}

// perOpBudget is the most rows a history table may gain per WRITE operation
// before the churn flags it. A history row records one change to one
// belief, so a write that touches a handful of beliefs adds a handful; tens
// per operation means one operation is versioning far more than it changed.
// Maintenance passes touch the whole brain by design and are not budgeted.
const perOpBudget = 25

// findings turns the phases into the defect list.
func findings(phases []churnPhase) []string {
	out := []string{} // [] not null in the report: "none observed"
	for _, ph := range phases {
		if ph.Error != "" {
			out = append(out, fmt.Sprintf("%s: phase failed: %s", ph.Name, ph.Error))
		}
		if ph.Kind == "read" || ph.Kind == "recall" {
			if len(ph.Growth) > 0 {
				out = append(out, fmt.Sprintf("DEFECT %s (%s, %d ops) grew tables: %v", ph.Name, ph.Kind, ph.Ops, ph.Growth))
			} else if ph.Committed && ph.Kind == "read" {
				out = append(out, fmt.Sprintf("DEFECT %s (read, %d ops) committed writes (data_version moved) without adding rows", ph.Name, ph.Ops))
			}
			continue
		}
		for _, t := range ph.mustNotGrow {
			if d := ph.Growth[t]; d > 0 {
				out = append(out, fmt.Sprintf("DEFECT %s (%d ops) added %d %s rows; it must add none", ph.Name, ph.Ops, d, t))
			}
		}
		if ph.Kind != "write" {
			continue
		}
		for t, v := range ph.PerOp {
			if v > perOpBudget {
				out = append(out, fmt.Sprintf("%s grows %s by %.1f rows per operation (budget %d)", ph.Name, t, v, perOpBudget))
			}
		}
	}
	slices.Sort(out)
	return out
}

// churnInputs are the deterministic subjects of the churn: which beliefs and
// episodes each phase acts on.
type churnInputs struct {
	active []string // active, current, generated beliefs in id order
	events []string // generated episode ids
}

func (c *censusDB) churnInputs(p scalebench.Params) (churnInputs, error) {
	rows, err := c.db.Query(`SELECT id FROM claims WHERE status = 'active' AND id LIKE 'sb-cl-%' ORDER BY id`)
	if err != nil {
		return churnInputs{}, err
	}
	defer func() { _ = rows.Close() }()
	var in churnInputs
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return churnInputs{}, err
		}
		in.active = append(in.active, id)
	}
	for e := 0; e < (p.Beliefs+4)/5; e++ {
		in.events = append(in.events, fmt.Sprintf("sb-ev-%09d", e))
	}
	return in, rows.Err()
}

// pick returns n distinct ids spread evenly over ids, starting at an offset
// derived from salt so two phases do not act on the same beliefs.
func pick(ids []string, n, salt int) []string {
	if n <= 0 || len(ids) == 0 {
		return nil
	}
	n = min(n, len(ids))
	stride := len(ids) / n
	out := make([]string, 0, n)
	for k := range n {
		out = append(out, ids[(k*stride+salt)%len(ids)])
	}
	return out
}

// sleepOptions mirrors the hosted and local sleep pass (cmd/mnemos
// sleepArgs), minus the LLM session-noise clearing, which needs a provider.
// Kept in step by hand because a tool cannot import package main; the
// consolidation is the library's either way.
func sleepOptions() mnemos.ConsolidateOptions {
	return mnemos.ConsolidateOptions{
		ForgetRefuted: true, ReinforceValidated: true, AssignSalience: true,
		AssignCredit: true, Plastic: true, Synthesize: true, ReinforcePlaybooks: true,
		DecayAssociations: true, DecayInhibition: true, Journal: true,
	}
}

// churnSizes scales the churn with nothing: every phase performs the same
// number of operations at every brain size, so per-operation growth and
// per-operation latency compare directly across sizes.
type churnSizes struct {
	Recalls, Gets, Restates, Feedback, Decisions, MergePairs, Retractions, Prunes int
}

var defaultChurn = churnSizes{Recalls: 200, Gets: 200, Restates: 20, Feedback: 50, Decisions: 20, MergePairs: 100, Retractions: 5, Prunes: 50}

// runChurn simulates a period of use on the loaded brain and measures what
// it costs to keep and to maintain. Maintenance timings are returned as
// operations; storage growth as the report section.
func runChurn(mem mnemos.Memory, dsn, dbPath string, p scalebench.Params, qs, restateTexts []string, timeout time.Duration, afterSeed storageCensus) ([]operation, *storageReport, error) {
	c, err := openCensus(dbPath)
	if err != nil {
		return nil, nil, err
	}
	defer c.close()
	in, err := c.churnInputs(p)
	if err != nil {
		return nil, nil, err
	}
	sz := defaultChurn
	pr := &phaseRunner{c: c}
	var ops []operation
	churnStart := time.Now().UTC()

	recallQueries := make([]string, sz.Recalls)
	for i := range recallQueries {
		recallQueries[i] = qs[i%len(qs)]
	}

	// Reads. Each must leave the store untouched.
	pr.run("recall_hops1", "recall", func(ctx context.Context) (int, string, error) {
		for i, q := range recallQueries {
			if _, err := mem.Recall(ctx, mnemos.Query{Text: q, Limit: 10, Hops: 1}); err != nil {
				return i, "", err
			}
		}
		return len(recallQueries), "", nil
	})
	pr.run("get_belief", "read", func(ctx context.Context) (int, string, error) {
		for i, id := range pick(in.active, sz.Gets, 7) {
			if _, err := mem.Get(ctx, id); err != nil {
				return i, "", err
			}
		}
		return sz.Gets, "", nil
	})
	pr.run("brain_health", "read", func(ctx context.Context) (int, string, error) {
		_, err := mem.BrainHealth(ctx)
		return 1, "", err
	})
	pr.run("recall_recorded_as_of", "read", func(ctx context.Context) (int, string, error) {
		for i, q := range recallQueries {
			if _, err := mem.Recall(ctx, mnemos.Query{Text: q, Limit: 10, Hops: 1, RecordedAsOf: churnStart}); err != nil {
				return i, "", err
			}
		}
		return len(recallQueries), "", nil
	})

	// Writes.
	restates := restateTexts[:min(sz.Restates, len(restateTexts))]
	// The same documents again, sessionless: the same origin, so by design
	// (#443) only the episode is stored and no belief gains evidence.
	pr.runGuarded("reingest_same_document", "write", []string{"claims", "claim_evidence"}, func(ctx context.Context) (int, string, error) {
		for i, t := range restates {
			if err := mem.Remember(ctx, mnemos.Item{Type: "fact", Content: t}); err != nil {
				return i, "", err
			}
		}
		return len(restates), "documents the warm phase already ingested, no session", nil
	})
	// Held beliefs stated again in new sessions: a new origin each, so each
	// belief gains one evidence link instead of a second row. Remember cannot
	// express a session (Item.Metadata lands on the episode as
	// input_session_id, not the session_id the fold reads), so the
	// restatement is an episode plus a claim citing it.
	pr.runGuarded("restate_new_session", "write", []string{"claims"}, func(ctx context.Context) (int, string, error) {
		ids := pick(in.active, sz.Restates, 29)
		for i, id := range ids {
			held, err := mem.Get(ctx, id)
			if err != nil {
				return i, "", err
			}
			ev := mnemos.Event{
				ID: fmt.Sprintf("sb-restate-%04d", i), At: time.Now().UTC(),
				Content:  "restated in a later session: " + held.Statement,
				Metadata: map[string]string{"session_id": fmt.Sprintf("scalebench-restate-%d", i)},
			}
			if err := mem.RememberEvent(ctx, ev); err != nil {
				return i, "", err
			}
			if _, err := mem.RememberClaim(ctx, mnemos.ClaimItem{Text: held.Statement, Type: held.Type, Confidence: held.Confidence, EventIDs: []string{ev.ID}}); err != nil {
				return i, "", err
			}
		}
		return len(ids), "a held belief restated in a new session (episode + claim citing it)", nil
	})
	pr.run("feedback", "write", func(ctx context.Context) (int, string, error) {
		for i, id := range pick(in.active, sz.Feedback, 11) {
			if _, err := mem.RecordFeedback(ctx, id, i%2 == 0, "scalebench"); err != nil {
				return i, "", err
			}
		}
		return sz.Feedback, "", nil
	})
	pr.run("decision_expectation", "write", func(ctx context.Context) (int, string, error) {
		beliefs := pick(in.active, sz.Decisions*2, 13)
		for i := range sz.Decisions {
			pair := beliefs[2*i : 2*i+2]
			if _, err := mem.RecordDecision(ctx, mnemos.Decision{Statement: fmt.Sprintf("scalebench decision %d", i), Beliefs: pair}); err != nil {
				return i, "", err
			}
			for _, b := range pair {
				if err := mem.Expect(ctx, b, 10, 1, time.Now().Add(24*time.Hour)); err != nil {
					return i, "", err
				}
				observed := 10.0 // validated
				if i%2 == 1 {
					observed = 100 // refuted
				}
				if err := mem.RecordObservation(ctx, b, observed); err != nil {
					return i, "", err
				}
			}
		}
		return sz.Decisions, "one decision on two beliefs, each with an observed expectation (half validated, half refuted)", nil
	})
	pr.run("embed_merge_pairs", "setup", func(ctx context.Context) (int, string, error) {
		return sz.MergePairs, "", seedMergePairs(ctx, dsn, pick(in.active, sz.MergePairs*2, 17))
	})

	// Maintenance: the full sleep pass, timed as an operation.
	var consolidated mnemos.ConsolidateResult
	pr.run("consolidate", "maintenance", func(ctx context.Context) (int, string, error) {
		var err error
		op := timeOnce("consolidate_full", timeout, func(ctx context.Context) (string, error) {
			consolidated, err = mem.Consolidate(ctx, sleepOptions())
			return fmt.Sprintf("scanned=%d merged=%d credited=%d trust_refreshed=%d", consolidated.ClaimsScanned, consolidated.Merged, consolidated.Credited, consolidated.TrustRefreshed), err
		})
		ops = append(ops, op)
		return 1, op.Detail, err
	})

	events := pick(in.events, sz.Retractions, 19)
	var impact []string
	pr.run("retract_event", "write", func(ctx context.Context) (int, string, error) {
		op := timeEach("retract_event", timeout, events, func(ctx context.Context, id string) error {
			r, err := mem.RetractEvent(ctx, id, "scalebench retraction")
			if err == nil {
				impact = append(impact, fmt.Sprintf("%d retracted/%d rescored/%d edges", len(r.Impact.RetractedClaims), len(r.Impact.Rescored), len(r.Impact.Relationships)))
			}
			return err
		})
		op.Detail = "impact: " + strings.Join(impact, ", ")
		ops = append(ops, op)
		return op.Samples, op.Detail, opErr(op)
	})
	pr.run("unretract_event", "write", func(ctx context.Context) (int, string, error) {
		op := timeEach("unretract_event", timeout, events, func(ctx context.Context, id string) error {
			_, err := mem.UnretractEvent(ctx, id, "scalebench un-retraction")
			return err
		})
		ops = append(ops, op)
		return op.Samples, "", opErr(op)
	})
	pr.run("prune_deprecate", "write", func(ctx context.Context) (int, string, error) {
		for i, id := range pick(in.active, sz.Prunes, 23) {
			if _, err := mem.DeprecateClaim(ctx, id, "scalebench prune"); err != nil {
				return i, "", err
			}
		}
		return sz.Prunes, "", nil
	})
	pr.run("recompute_trust_full", "maintenance", func(ctx context.Context) (int, string, error) {
		op := recomputeTrustOp("recompute_trust_after_churn", dsn, p, timeout)
		ops = append(ops, op)
		return 1, op.Detail, opErr(op)
	})

	// Time travel against a brain that now has history: the same queries,
	// live and as recorded before the churn began.
	pr.run("recall_live_vs_recorded", "recall", func(ctx context.Context) (int, string, error) {
		live := timeEach("recall_live_after_churn", timeout, recallQueries, func(ctx context.Context, q string) error {
			_, err := mem.Recall(ctx, mnemos.Query{Text: q, Limit: 10, Hops: 1})
			return err
		})
		asOf := timeEach("recall_recorded_as_of", timeout, recallQueries, func(ctx context.Context, q string) error {
			_, err := mem.Recall(ctx, mnemos.Query{Text: q, Limit: 10, Hops: 1, RecordedAsOf: churnStart})
			return err
		})
		ops = append(ops, live, asOf)
		ratio := 0.0
		if live.P50MS > 0 {
			ratio = asOf.P50MS / live.P50MS
		}
		return live.Samples + asOf.Samples, fmt.Sprintf("recorded-as-of p50 is %.2fx live", ratio), firstErr(opErr(live), opErr(asOf))
	})

	afterChurn, err := c.census()
	if err != nil {
		return ops, nil, err
	}
	return ops, &storageReport{AfterSeed: afterSeed, AfterChurn: afterChurn, Phases: pr.phases, Findings: findings(pr.phases)}, nil
}

func opErr(op operation) error {
	if op.Outcome == "ok" {
		return nil
	}
	return fmt.Errorf("%s: %s %s", op.Name, op.Outcome, op.Error)
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// seedMergePairs gives each consecutive pair of ids the same embedding, so
// the consolidation's semantic dedupe has real duplicates to merge. Pairs are
// mutually orthogonal enough (random 64-d vectors) that nothing merges across
// pairs. The corpus carries no embeddings otherwise, so without this the pass
// would scan nothing and the merge cost would go unmeasured.
func seedMergePairs(ctx context.Context, dsn string, ids []string) error {
	conn, err := store.Open(ctx, dsn)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	rng := rand.New(rand.NewPCG(42, 7))
	for i := 0; i+1 < len(ids); i += 2 {
		v := make([]float32, 64)
		for j := range v {
			v[j] = float32(rng.NormFloat64())
		}
		for _, id := range ids[i : i+2] {
			if err := conn.Embeddings.Upsert(ctx, id, "claim", v, "scalebench-stub", ""); err != nil {
				return fmt.Errorf("embed %s: %w", id, err)
			}
		}
	}
	return nil
}

func printStorage(s *storageReport) {
	if s == nil {
		return
	}
	for _, st := range []struct {
		name string
		c    storageCensus
	}{{"after seed", s.AfterSeed}, {"after churn", s.AfterChurn}} {
		fmt.Printf("storage %s: %.1f MiB, %.0f B/belief, %.0f B/episode (%d beliefs, %d episodes)\n",
			st.name, float64(st.c.DBBytes)/(1<<20), st.c.BytesPerBelief, st.c.BytesPerEpisode, st.c.Beliefs, st.c.Episodes)
	}
	for _, f := range s.Findings {
		fmt.Println("finding:", f)
	}
}
