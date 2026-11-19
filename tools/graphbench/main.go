// Command graphbench measures whether a native graph store would serve
// Mnemos's graph-shaped workloads materially better than the relational
// backends it uses today (ADR 0043).
//
// It generates one corpus (internal/scalebench plus a dependency, merge and
// density overlay, see corpus.go), loads the identical data into SQLite,
// Postgres and Neo4j, and times six workloads on each: multi-hop recall
// expansion, retraction closure, the dissonance listing and the contradiction
// neighbourhood, merge-redirect resolution, the provenance path, and a batch
// write. The relational side runs the current production code path and, where
// that path makes several round trips, a single-statement recursive-CTE
// version. An in-process adjacency map is reported as the upper bound no
// engine can beat. Every answer is checked against SQLite's production answer
// for the same input.
//
// Usage (the containers are managed by scripts/graphbench.sh):
//
//	go run ./tools/graphbench -beliefs 10000                             # SQLite + adjacency map only
//	go run ./tools/graphbench -beliefs 100000 -profile dense \
//	    -pg "$PG_DSN" \
//	    -neo4j http://127.0.0.1:7474 -json out.json
//
// Neither Docker nor a server is needed unless -pg / -neo4j are given, so the
// unit tests and `make test` never start one.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/scalebench"

	_ "go.klarlabs.de/mnemos/internal/store/postgres"
	_ "go.klarlabs.de/mnemos/internal/store/sqlite"
)

type config struct {
	beliefs, batch, iters, warmup, writes, writeBeliefs int
	seed                                                uint64
	profile                                             string
	budget, loadWait, callTimeout                       time.Duration
	maxLoad                                             float64
	pgDSN, neo4jURL, pgContainer, neo4jContainer        string
	neo4jImage, pgImage                                 string
	workDir, jsonOut                                    string
	only, stores                                        string
}

func main() {
	var c config
	flag.IntVar(&c.beliefs, "beliefs", 10000, "beliefs in the corpus")
	flag.Uint64Var(&c.seed, "seed", 1, "corpus seed")
	flag.StringVar(&c.profile, "profile", string(ProfileDense), "corpus profile: uniform | dense")
	flag.IntVar(&c.batch, "batch", 1000, "beliefs per load batch")
	flag.IntVar(&c.iters, "iters", 100, "timed iterations per workload and variant")
	flag.IntVar(&c.warmup, "warmup", 5, "untimed warm-up iterations")
	flag.DurationVar(&c.budget, "budget", 30*time.Second, "stop a cell's timed loop after this long (once it has 10 samples)")
	flag.DurationVar(&c.callTimeout, "call-timeout", 2*time.Minute, "fail a cell when one call takes longer than this")
	flag.IntVar(&c.writes, "writes", 30, "timed write batches per write variant")
	flag.IntVar(&c.writeBeliefs, "write-beliefs", 50, "beliefs per write batch")
	flag.Float64Var(&c.maxLoad, "max-load", 8, "wait before each timed cell until the 1-minute load is at most this")
	flag.DurationVar(&c.loadWait, "load-wait", 90*time.Minute, "give up waiting for a quiet machine after this long")
	flag.StringVar(&c.pgDSN, "pg", "", "Postgres DSN (a namespace per corpus is added); empty skips Postgres")
	flag.StringVar(&c.neo4jURL, "neo4j", "", "Neo4j HTTP URL of an EMPTY database; empty skips Neo4j")
	flag.StringVar(&c.pgContainer, "pg-container", "", "Postgres container name, for memory figures")
	flag.StringVar(&c.neo4jContainer, "neo4j-container", "", "Neo4j container name, for memory and disk figures")
	flag.StringVar(&c.neo4jImage, "neo4j-image", "", "Neo4j image, recorded in the report")
	flag.StringVar(&c.pgImage, "pg-image", "", "Postgres image, recorded in the report")
	flag.StringVar(&c.workDir, "work", "", "directory for the SQLite database (default: a temp dir)")
	flag.StringVar(&c.jsonOut, "json", "", "write the JSON report here")
	flag.StringVar(&c.only, "only", "", "comma-separated workloads to run (default: all)")
	flag.StringVar(&c.stores, "stores", "", "comma-separated stores to time: sqlite, postgres, neo4j, in-process (default: all loaded)")
	flag.Parse()
	if err := run(context.Background(), c); err != nil {
		fmt.Fprintln(os.Stderr, "graphbench:", err)
		os.Exit(1)
	}
}

// Report is the machine-readable result.
type Report struct {
	Schema      string         `json:"schema"`
	Params      map[string]any `json:"params"`
	Environment map[string]any `json:"environment"`
	Corpus      CorpusStats    `json:"corpus"`
	Stores      []StoreInfo    `json:"stores"`
	Cells       []Cell         `json:"cells"`
	Parity      ParitySummary  `json:"parity"`
	Notes       []string       `json:"notes"`
}

// StoreInfo is one store's load cost and footprint.
type StoreInfo struct {
	Name        string  `json:"name"`
	LoadMS      float64 `json:"load_ms"`
	LoadLoadAvg float64 `json:"load_avg_during_load"`
	DiskBytes   int64   `json:"disk_bytes,omitempty"`
	DiskDetail  string  `json:"disk_detail,omitempty"`
	MemBytes    int64   `json:"mem_bytes,omitempty"`
	MemDetail   string  `json:"mem_detail,omitempty"`
}

// Cell is one (workload, store, path) measurement.
type Cell struct {
	Workload string `json:"workload"`
	Variant
	Stats
	MeanResultLines float64 `json:"mean_result_lines"`
	LoadBefore      float64 `json:"load_avg_before"`
	LoadAfter       float64 `json:"load_avg_after"`
	ParityChecked   int     `json:"parity_checked"`
	ParityMismatch  int     `json:"parity_mismatches"`
	MismatchExample string  `json:"mismatch_example,omitempty"`
	Error           string  `json:"error,omitempty"`
}

// ParitySummary counts the checks across all cells.
type ParitySummary struct {
	Reference  string `json:"reference"`
	Checked    int    `json:"checked"`
	Mismatches int    `json:"mismatches"`
}

func run(ctx context.Context, c config) error {
	logf := func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) }
	p := scalebench.Params{Beliefs: c.beliefs, Seed: c.seed, Shape: scalebench.ShapeUniform, Anchor: scalebench.DefaultAnchor, RunID: "graphbench"}
	start := time.Now()
	corpus, err := GenerateCorpus(p, Profile(c.profile), c.batch)
	if err != nil {
		return err
	}
	logf("corpus %s/%d generated in %s: %+v", c.profile, c.beliefs, time.Since(start).Round(time.Millisecond), corpus.Stats)
	mem := newMemGraph(corpus)
	mem.sortContradictions()
	in := sampleInputs(corpus, (c.writes+c.warmup)*2, c.writeBeliefs)

	rep := Report{
		Schema: "graphbench/v1",
		Params: map[string]any{
			"beliefs": c.beliefs, "seed": c.seed, "profile": c.profile, "iters": c.iters, "warmup": c.warmup,
			"budget": c.budget.String(), "writes": c.writes, "write_beliefs": c.writeBeliefs, "max_load": c.maxLoad,
		},
		Environment: environment(c),
		Corpus:      corpus.Stats,
		Notes: []string{
			"Latency is wall-clock per call from the Go client: SQLite is in-process; Postgres and Neo4j are Docker containers reached over loopback TCP.",
			"Neo4j is reached over its HTTP transactional Cypher API (JSON), not Bolt; round_trip_floor reports the transport floor of each store.",
			"adjacency-map is Go maps with no I/O: an upper bound on any engine, not a candidate backend.",
			"Parity: every answer is compared, per input, with the in-process oracle's answer; SQLite production is one of the paths checked, so agreement is transitive.",
		},
	}

	if c.workDir == "" {
		d, err := os.MkdirTemp("", "graphbench-*")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(d) }()
		c.workDir = d
	}
	sqlitePath := filepath.Join(c.workDir, fmt.Sprintf("graphbench-%s-%d.db", c.profile, c.beliefs))
	_ = os.Remove(sqlitePath)
	// Never let anything fall back to the developer's real brain.
	_ = os.Setenv("MNEMOS_DB_URL", "sqlite://"+sqlitePath)

	variants := map[string][]Variant{}
	add := func(m map[string][]Variant) {
		for k, v := range m {
			variants[k] = append(variants[k], v...)
		}
	}
	floors := []Variant{}

	// SQLite: always, and the parity reference.
	sq, info, err := loadRel(ctx, c, "sqlite", "sqlite://"+sqlitePath, corpus, logf)
	if err != nil {
		return err
	}
	defer sq.close()
	if info.DiskBytes, err = sq.diskBytes(ctx, sqlitePath, ""); err != nil {
		return err
	}
	rep.Stores = append(rep.Stores, info)
	add(sq.variants())
	floors = append(floors, Variant{Store: "sqlite", Path: "SELECT 1", run: sqlPing(sq.db)})

	var pg *relStore
	pgNS := fmt.Sprintf("gb_%s_%d", strings.ReplaceAll(c.profile, "-", "_"), c.beliefs)
	if c.pgDSN != "" {
		dsn := c.pgDSN + sep(c.pgDSN) + "namespace=" + pgNS
		pg, info, err = loadRel(ctx, c, "postgres", dsn, corpus, logf)
		if err != nil {
			return err
		}
		defer pg.close()
		if _, err := pg.db.ExecContext(ctx, "VACUUM ANALYZE"); err != nil {
			return fmt.Errorf("postgres analyze: %w", err)
		}
		if info.DiskBytes, err = pg.diskBytes(ctx, "", pgNS); err != nil {
			return err
		}
		rep.Stores = append(rep.Stores, info)
		add(pg.variants())
		floors = append(floors, Variant{Store: "postgres", Path: "SELECT 1", run: sqlPing(pg.db)})
	}

	var neo *neo4jStore
	if c.neo4jURL != "" {
		neo = newNeo4j(c.neo4jURL)
		if err := neo.waitReady(ctx, 3*time.Minute); err != nil {
			return err
		}
		la, err := waitForQuietMachine(ctx, c.maxLoad, c.loadWait, logf)
		if err != nil {
			return err
		}
		t0 := time.Now()
		if err := neo.load(ctx, corpus); err != nil {
			return fmt.Errorf("neo4j load: %w", err)
		}
		info := StoreInfo{Name: "neo4j", LoadMS: msOf(time.Since(t0)), LoadLoadAvg: la}
		logf("neo4j loaded in %s", time.Since(t0).Round(time.Millisecond))
		rep.Stores = append(rep.Stores, info)
		add(neo.variants())
		floors = append(floors, Variant{Store: "neo4j", Path: "RETURN 1 (HTTP)", run: neo.ping})
	}
	oracles := map[string]func(context.Context, any) ([]string, error){
		wExpand2: typed(mem.expand), wExpand5: typed(mem.expand), wRetract: typed(mem.retract),
		wDissonance: typed(mem.dissonancePage), wNeighbour: typed(mem.neighbourhood),
		wRedirects: typed(mem.redirects), wProvenance: typed(mem.provenance),
	}
	add(map[string][]Variant{
		wExpand2:    {{Store: "in-process", Path: "adjacency-map", run: typed(mem.expand)}},
		wExpand5:    {{Store: "in-process", Path: "adjacency-map", run: typed(mem.expand)}},
		wRetract:    {{Store: "in-process", Path: "adjacency-map", run: typed(mem.retract)}},
		wDissonance: {{Store: "in-process", Path: "adjacency-map", run: typed(mem.dissonancePage)}},
		wNeighbour:  {{Store: "in-process", Path: "adjacency-map", run: typed(mem.neighbourhood)}},
		wRedirects:  {{Store: "in-process", Path: "adjacency-map", run: typed(mem.redirects)}},
		wProvenance: {{Store: "in-process", Path: "adjacency-map", run: typed(mem.provenance)}},
	})

	workloads := []struct {
		name   string
		inputs []any
	}{
		{"round_trip_floor", asAny(make([]struct{}, inputsPerLoad))},
		{wExpand2, asAny(in.Expand2)},
		{wExpand5, asAny(in.Expand5)},
		{wRetract, asAny(in.Retract)},
		{wDissonance, asAny(in.Pages)},
		{wNeighbour, asAny(in.Neighbour)},
		{wRedirects, asAny(in.Redirects)},
		{wProvenance, asAny(in.Provenance)},
	}
	variants["round_trip_floor"] = floors
	if c.stores != "" {
		keep := map[string]bool{}
		for _, s := range strings.Split(c.stores, ",") {
			keep[strings.TrimSpace(s)] = true
		}
		for w, vs := range variants {
			var kept []Variant
			for _, v := range vs {
				if keep[v.Store] {
					kept = append(kept, v)
				}
			}
			variants[w] = kept
		}
	}
	only := map[string]bool{}
	for _, w := range strings.Split(c.only, ",") {
		if w = strings.TrimSpace(w); w != "" {
			only[w] = true
		}
	}
	for _, w := range workloads {
		if len(only) > 0 && !only[w.name] {
			continue
		}
		ref, err := oracle(ctx, w.name, w.inputs, oracles[w.name])
		if err != nil {
			return err
		}
		for _, v := range variants[w.name] {
			cell, err := measureCell(ctx, c, w.name, v, w.inputs, ref, logf)
			if err != nil {
				return err
			}
			rep.Cells = append(rep.Cells, cell)
		}
	}
	// Neo4j's footprint, measured after the reads rather than right after the
	// load: until a checkpoint the loaded data sits in the transaction log and
	// the store files are nearly empty. Community has no db.checkpoint(), so
	// scripts/graphbench.sh shortens the checkpoint interval instead, and the
	// read cells give it minutes to run.
	if neo != nil && c.neo4jContainer != "" {
		for i := range rep.Stores {
			if rep.Stores[i].Name == "neo4j" {
				rep.Stores[i].DiskBytes, rep.Stores[i].DiskDetail = containerDisk(ctx, c.neo4jContainer, "/data/databases/neo4j", "/data/transactions/neo4j")
			}
		}
	}

	// Writes last: they change the stores. Each variant writes its own
	// batches, so no variant re-upserts rows another one wrote.
	if len(only) == 0 || only[wWrite] {
		per := c.writes + c.warmup
		for _, v := range variants[wWrite] {
			slot := 0
			if v.Path == "ports-only" {
				slot = 1
			}
			inputs := asAny(in.Writes[slot*per : (slot+1)*per])
			cell, err := measureWrite(ctx, c, v, inputs, logf)
			if err != nil {
				return err
			}
			rep.Cells = append(rep.Cells, cell)
		}
	}

	// Footprints after the run.
	for i := range rep.Stores {
		switch rep.Stores[i].Name {
		case "sqlite":
			rep.Stores[i].MemBytes, rep.Stores[i].MemDetail = maxRSS(), "graphbench process max RSS (SQLite is in-process; includes the corpus and the adjacency map)"
		case "postgres":
			if c.pgContainer != "" {
				rep.Stores[i].MemBytes, rep.Stores[i].MemDetail = containerMem(ctx, c.pgContainer)
			}
		case "neo4j":
			if c.neo4jContainer != "" {
				rep.Stores[i].MemBytes, rep.Stores[i].MemDetail = containerMem(ctx, c.neo4jContainer)
			}
		}
	}
	rep.Parity.Reference = "in-process adjacency-map oracle, on every input each path ran (sqlite/production is checked against it like every other path)"
	for _, cell := range rep.Cells {
		rep.Parity.Checked += cell.ParityChecked
		rep.Parity.Mismatches += cell.ParityMismatch
	}
	printHuman(rep)
	if c.jsonOut != "" {
		b, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(c.jsonOut, append(b, '\n'), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func sep(dsn string) string {
	if strings.Contains(dsn, "?") {
		return "&"
	}
	return "?"
}

func loadRel(ctx context.Context, c config, name, dsn string, corpus *Corpus, logf func(string, ...any)) (*relStore, StoreInfo, error) {
	s, err := openRel(ctx, name, dsn)
	if err != nil {
		return nil, StoreInfo{}, err
	}
	la, err := waitForQuietMachine(ctx, c.maxLoad, c.loadWait, logf)
	if err != nil {
		s.close()
		return nil, StoreInfo{}, err
	}
	t0 := time.Now()
	if err := s.load(ctx, corpus); err != nil {
		s.close()
		return nil, StoreInfo{}, fmt.Errorf("%s load: %w", name, err)
	}
	logf("%s loaded in %s", name, time.Since(t0).Round(time.Millisecond))
	return s, StoreInfo{Name: name, LoadMS: msOf(time.Since(t0)), LoadLoadAvg: la}, nil
}

func sqlPing(db *sql.DB) func(context.Context, any) ([]string, error) {
	return func(ctx context.Context, _ any) ([]string, error) {
		var one int
		return nil, db.QueryRowContext(ctx, "SELECT 1").Scan(&one)
	}
}

// oracle answers every input with the in-process adjacency map. Every store
// path is compared with it on every input it ran, so two paths that both match
// the oracle returned identical sets; SQLite's production path is one of them,
// which is what validates the oracle itself.
func oracle(ctx context.Context, workload string, inputs []any, fn func(context.Context, any) ([]string, error)) (map[int]string, error) {
	ref := map[int]string{}
	if fn == nil {
		return ref, nil
	}
	for i, in := range inputs {
		res, err := fn(ctx, in)
		if err != nil {
			return nil, fmt.Errorf("oracle %s: %w", workload, err)
		}
		ref[i] = canonical(res)
	}
	return ref, nil
}

// measureCell times one variant over the inputs and checks each distinct
// input's answer against the oracle's.
func measureCell(ctx context.Context, c config, workload string, v Variant, inputs []any, ref map[int]string, logf func(string, ...any)) (Cell, error) {
	cell := Cell{Workload: workload, Variant: v}
	before, err := waitForQuietMachine(ctx, c.maxLoad, c.loadWait, logf)
	if err != nil {
		return cell, err
	}
	cell.LoadBefore = before
	// A variant slower than a fifth of the budget per call is sampled less:
	// one warm-up and at least 3 timed calls, rather than 5 and 10.
	slow := c.budget / 5
	minSamples := 10
	for i := 0; i < c.warmup; i++ {
		s := time.Now()
		if _, err := callWithTimeout(ctx, c.callTimeout, v, inputs[i%len(inputs)]); err != nil {
			cell.Error = err.Error()
			logf("  %s %s/%s: %v", workload, v.Store, v.Path, err)
			return cell, nil
		}
		if time.Since(s) > slow {
			minSamples = 3
			break
		}
	}
	var ds []time.Duration
	checked := map[int]bool{}
	lines := 0
	t0 := time.Now()
	for i := 0; i < c.iters; i++ {
		if i >= minSamples && time.Since(t0) > c.budget {
			break
		}
		idx := i % len(inputs)
		s := time.Now()
		res, err := callWithTimeout(ctx, c.callTimeout, v, inputs[idx])
		d := time.Since(s)
		if err != nil {
			cell.Error = err.Error()
			logf("  %s %s/%s: %v", workload, v.Store, v.Path, err)
			break
		}
		ds = append(ds, d)
		lines += len(res)
		if workload == "round_trip_floor" || checked[idx] {
			continue
		}
		checked[idx] = true
		h := canonical(res)
		want, ok := ref[idx]
		if !ok {
			continue
		}
		cell.ParityChecked++
		if h != want {
			cell.ParityMismatch++
			if cell.MismatchExample == "" {
				cell.MismatchExample = fmt.Sprintf("input %d: %+v", idx, inputs[idx])
			}
		}
	}
	cell.Stats = summarize(ds)
	if len(ds) > 0 {
		cell.MeanResultLines = float64(lines) / float64(len(ds))
	}
	cell.LoadAfter = loadAvg()
	logf("  %-28s %-10s %-15s n=%-4d p50=%8.2fms p95=%8.2fms p99=%8.2fms lines=%.0f parity=%d/%d load=%.1f",
		workload, v.Store, v.Path, cell.Samples, cell.P50MS, cell.P95MS, cell.P99MS, cell.MeanResultLines,
		cell.ParityChecked-cell.ParityMismatch, cell.ParityChecked, cell.LoadAfter)
	return cell, nil
}

// callWithTimeout bounds one call; a timeout is reported as the cell's error,
// not a harness failure.
func callWithTimeout(ctx context.Context, limit time.Duration, v Variant, in any) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	res, err := v.run(ctx, in)
	if err != nil && ctx.Err() != nil {
		return nil, fmt.Errorf("timeout: one call exceeded %s", limit)
	}
	return res, err
}

// measureWrite times distinct write batches; warm-up batches are written
// first and not timed.
func measureWrite(ctx context.Context, c config, v Variant, inputs []any, logf func(string, ...any)) (Cell, error) {
	cell := Cell{Workload: wWrite, Variant: v}
	before, err := waitForQuietMachine(ctx, c.maxLoad, c.loadWait, logf)
	if err != nil {
		return cell, err
	}
	cell.LoadBefore = before
	var ds []time.Duration
	for i, in := range inputs {
		s := time.Now()
		if _, err := v.run(ctx, in); err != nil {
			cell.Error = err.Error()
			logf("  write %s/%s: %v", v.Store, v.Path, err)
			break
		}
		if i >= c.warmup {
			ds = append(ds, time.Since(s))
		}
	}
	cell.Stats = summarize(ds)
	cell.LoadAfter = loadAvg()
	logf("  %-28s %-10s %-15s n=%-4d p50=%8.2fms p95=%8.2fms p99=%8.2fms load=%.1f",
		wWrite, v.Store, v.Path, cell.Samples, cell.P50MS, cell.P95MS, cell.P99MS, cell.LoadAfter)
	return cell, nil
}

func printHuman(r Report) {
	fmt.Printf("graphbench %s/%d — %d beliefs, %d episodes, %d evidence, rels %v, %d dependencies, %d redirects\n",
		r.Params["profile"], r.Params["beliefs"], r.Corpus.Beliefs, r.Corpus.Episodes, r.Corpus.Evidence,
		r.Corpus.Relationships, r.Corpus.Dependencies, r.Corpus.Redirects)
	for _, s := range r.Stores {
		fmt.Printf("  store %-9s load %10.0f ms  disk %8.1f MB  mem %8.1f MB\n", s.Name, s.LoadMS, float64(s.DiskBytes)/1e6, float64(s.MemBytes)/1e6)
	}
	fmt.Printf("%-28s %-10s %-15s %5s %9s %9s %9s %8s %7s\n", "workload", "store", "path", "n", "p50ms", "p95ms", "p99ms", "lines", "parity")
	for _, c := range r.Cells {
		par := "-"
		if c.ParityChecked > 0 {
			par = fmt.Sprintf("%d/%d", c.ParityChecked-c.ParityMismatch, c.ParityChecked)
		}
		if c.Error != "" {
			par = "ERROR"
		}
		fmt.Printf("%-28s %-10s %-15s %5d %9.2f %9.2f %9.2f %8.0f %7s\n", c.Workload, c.Store, c.Path, c.Samples, c.P50MS, c.P95MS, c.P99MS, c.MeanResultLines, par)
	}
	fmt.Printf("parity: %d checks against %s, %d mismatches\n", r.Parity.Checked, r.Parity.Reference, r.Parity.Mismatches)
}

func environment(c config) map[string]any {
	env := map[string]any{
		"go_version": runtime.Version(), "goos": runtime.GOOS, "goarch": runtime.GOARCH, "num_cpu": runtime.NumCPU(),
		"started_at": time.Now().UTC().Format(time.RFC3339), "neo4j_image": c.neo4jImage, "postgres_image": c.pgImage,
	}
	if out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output(); err == nil {
		env["commit"] = strings.TrimSpace(string(out))
	}
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("sysctl", "-n", "machdep.cpu.brand_string").Output(); err == nil {
			env["cpu"] = strings.TrimSpace(string(out))
		}
	}
	return env
}

// containerMem reads a container's memory use from `docker stats`.
func containerMem(ctx context.Context, name string) (int64, string) {
	out, err := exec.CommandContext(ctx, "docker", "stats", "--no-stream", "--format", "{{.MemUsage}}", name).Output()
	if err != nil {
		return 0, "docker stats failed: " + err.Error()
	}
	usage := strings.TrimSpace(string(out))
	used, _, _ := strings.Cut(usage, "/")
	return parseDockerBytes(strings.TrimSpace(used)), "docker stats: " + usage
}

// parseDockerBytes parses docker's human sizes ("512.3MiB", "1.2GiB", "980kB").
func parseDockerBytes(s string) int64 {
	units := []struct {
		suffix string
		mult   float64
	}{{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}, {"GB", 1e9}, {"MB", 1e6}, {"kB", 1e3}, {"B", 1}}
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			v, err := strconv.ParseFloat(strings.TrimSuffix(s, u.suffix), 64)
			if err != nil {
				return 0
			}
			return int64(v * u.mult)
		}
	}
	return 0
}

// containerDisk sums `du -sk` over paths inside a container.
func containerDisk(ctx context.Context, name string, paths ...string) (int64, string) {
	var total int64
	var parts []string
	for _, p := range paths {
		out, err := exec.CommandContext(ctx, "docker", "exec", name, "du", "-sk", p).Output()
		if err != nil {
			parts = append(parts, p+": "+err.Error())
			continue
		}
		f := strings.Fields(string(out))
		if len(f) == 0 {
			continue
		}
		kb, _ := strconv.ParseInt(f[0], 10, 64)
		total += kb * 1024
		parts = append(parts, fmt.Sprintf("%s=%dKiB", p, kb))
	}
	return total, strings.Join(parts, " ")
}

func fileSizes(paths ...string) (int64, error) {
	var total int64
	for _, p := range paths {
		st, err := os.Stat(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, err
		}
		total += st.Size()
	}
	return total, nil
}
