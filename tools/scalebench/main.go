// Command scalebench records what Mnemos's operations cost against a brain of a
// chosen size and shape. It is the Phase 0 baseline of the consolidation
// program (#382): every later phase is judged by whether these numbers, at the
// same params, got better or merely different.
//
// Usage:
//
//	go run ./tools/scalebench -beliefs 10000                       # small dev brain
//	go run ./tools/scalebench -beliefs 100000 -json base-100k.json
//	go run ./tools/scalebench -beliefs 1000000 -shape hub -timeout 30m
//
// The corpus comes from internal/scalebench and is a pure function of
// -beliefs, -seed and -shape, so a run at another commit regenerates the same
// brain. The report records those params alongside the commit, toolchain and
// hardware, because a latency number without them cannot be compared to
// anything.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.klarlabs.de/mnemos"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/scalebench"
	"go.klarlabs.de/mnemos/internal/store"
	"go.klarlabs.de/mnemos/internal/trust"

	_ "go.klarlabs.de/mnemos/internal/store/sqlite"
)

// Report is the machine-readable result. Durations are milliseconds.
type Report struct {
	SchemaVersion string            `json:"schema_version"`
	Params        reportParams      `json:"params"`
	Environment   environment       `json:"environment"`
	Corpus        scalebench.Stats  `json:"corpus"`
	Operations    []operation       `json:"operations"`
	Memory        memory            `json:"memory"`
	Notes         []string          `json:"notes"`
	Extra         map[string]string `json:"extra,omitempty"`
}

type reportParams struct {
	Beliefs   int    `json:"beliefs"`
	Seed      uint64 `json:"seed"`
	Shape     string `json:"shape"`
	Anchor    string `json:"anchor"`
	Batch     int    `json:"batch"`
	Queries   int    `json:"queries"`
	Ingests   int    `json:"ingests"`
	Backend   string `json:"backend"`
	TimeoutOp string `json:"timeout_per_operation"`
}

type environment struct {
	Commit    string `json:"commit"`
	Dirty     bool   `json:"dirty"`
	GoVersion string `json:"go_version"`
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`
	NumCPU    int    `json:"num_cpu"`
	CPU       string `json:"cpu,omitempty"`
	MemBytes  uint64 `json:"mem_bytes,omitempty"`
	StartedAt string `json:"started_at"`
}

type operation struct {
	Name    string  `json:"name"`
	Samples int     `json:"samples"`
	TotalMS float64 `json:"total_ms"`
	P50MS   float64 `json:"p50_ms"`
	P95MS   float64 `json:"p95_ms"`
	MaxMS   float64 `json:"max_ms"`
	// PerSecond is throughput where it is meaningful (items per second).
	PerSecond float64 `json:"per_second,omitempty"`
	Outcome   string  `json:"outcome"` // ok | timeout | error | unsupported
	Error     string  `json:"error,omitempty"`
	Detail    string  `json:"detail,omitempty"`
}

type memory struct {
	PeakHeapInuseBytes uint64 `json:"peak_heap_inuse_bytes"`
	MaxRSSBytes        int64  `json:"max_rss_bytes"`
	DBBytes            int64  `json:"db_bytes"`
}

func main() {
	var (
		p                       scalebench.Params
		batch, queries, ingests int
		jsonOut, workDir, shape string
		timeout                 time.Duration
		keep                    bool
	)
	flag.IntVar(&p.Beliefs, "beliefs", 10000, "number of beliefs to generate")
	flag.Uint64Var(&p.Seed, "seed", 1, "corpus seed")
	flag.StringVar(&shape, "shape", string(scalebench.ShapeUniform), "corpus shape: "+shapeList())
	flag.IntVar(&batch, "batch", 1000, "beliefs per bulk-load batch")
	flag.IntVar(&queries, "queries", 50, "recall queries to time")
	flag.IntVar(&ingests, "ingests", 20, "Remember calls to time (write path incl. incremental relate)")
	flag.DurationVar(&timeout, "timeout", 20*time.Minute, "ceiling for each measured operation")
	flag.StringVar(&jsonOut, "json", "", "write the report here (\"-\" for stdout)")
	flag.StringVar(&workDir, "work", "", "directory for the database (default: a temp dir)")
	flag.BoolVar(&keep, "keep", false, "keep the database after the run")
	flag.IntVar(&repeat, "repeat", 1, "measure the warm operations this many times and report the median")
	flag.StringVar(&checkFile, "check", "", "fail when an operation exceeds the ceilings in this JSON file (see bench/scale-gate.json)")
	flag.StringVar(&profileOp, "profile-op", "", "CPU-profile this one operation (e.g. ingest_remember, brain_health)")
	flag.StringVar(&profileOut, "cpuprofile", "scalebench.cpu.pprof", "where -profile-op writes its profile")
	flag.Parse()
	p.Shape = scalebench.Shape(shape)
	p.Anchor = scalebench.DefaultAnchor
	p.RunID = "scalebench"

	if err := run(p, batch, queries, ingests, timeout, jsonOut, workDir, keep); err != nil {
		fmt.Fprintln(os.Stderr, "scalebench:", err)
		os.Exit(1)
	}
}

func shapeList() string {
	var s []string
	for _, sh := range scalebench.Shapes {
		s = append(s, string(sh))
	}
	return strings.Join(s, ", ")
}

func run(p scalebench.Params, batch, queries, ingests int, timeout time.Duration, jsonOut, workDir string, keep bool) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if workDir == "" {
		d, err := os.MkdirTemp("", "scalebench-*")
		if err != nil {
			return err
		}
		workDir = d
		if !keep {
			defer func() { _ = os.RemoveAll(d) }()
		}
	}
	dbPath := filepath.Join(workDir, fmt.Sprintf("scalebench-%s-%d-%d.db", p.Shape, p.Beliefs, p.Seed))
	_ = os.Remove(dbPath)
	dsn := "sqlite://" + dbPath
	// Never let anything fall back to the developer's real brain.
	_ = os.Setenv("MNEMOS_DB_URL", dsn)

	rep := Report{
		SchemaVersion: "scalebench/v1",
		Params: reportParams{
			Beliefs: p.Beliefs, Seed: p.Seed, Shape: string(p.Shape), Anchor: p.Anchor.Format(time.RFC3339),
			Batch: batch, Queries: queries, Ingests: ingests, Backend: "sqlite", TimeoutOp: timeout.String(),
		},
		Environment: env(),
		Notes: []string{
			"Corpus is generated, not extracted: load time measures the storage write path, not extraction.",
			"Beliefs carry no embeddings; recall uses the token-overlap path (passive mode).",
			"ingest = mnemos.Remember in passive mode: rule extraction + incremental relate against the loaded corpus + persist + scoped trust.",
			"Each operation runs under its own timeout; 'timeout' is a result, not a harness failure.",
		},
	}

	stopMem, peak := sampleHeap()
	ctx := context.Background()

	// 1. Schema bootstrap on an empty store.
	rep.Operations = append(rep.Operations, timeOnce("open_empty_store", timeout, func(ctx context.Context) (string, error) {
		conn, err := store.Open(ctx, dsn)
		if err != nil {
			return "", err
		}
		return "", conn.Close()
	}))

	// 2. Bulk load.
	loadCtx, cancel := context.WithTimeout(ctx, 4*timeout)
	lastPrint := time.Now()
	lr, err := scalebench.Load(loadCtx, dsn, p, batch, func(s scalebench.Stats) {
		if time.Since(lastPrint) > 10*time.Second {
			fmt.Fprintf(os.Stderr, "  loaded %d/%d beliefs\n", s.Claims, p.Beliefs)
			lastPrint = time.Now()
		}
	})
	cancel()
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}
	rep.Corpus = lr.Stats
	rep.Operations = append(rep.Operations, operation{
		Name: "bulk_load", Samples: 1, TotalMS: ms(lr.Elapsed), P50MS: ms(lr.Elapsed), P95MS: ms(lr.Elapsed), MaxMS: ms(lr.Elapsed),
		PerSecond: round2(float64(lr.Stats.Claims) / lr.Elapsed.Seconds()), Outcome: "ok", Detail: "beliefs/s",
	})

	// 3. The full trust recompute (`mnemos recompute-trust --all`), with the
	// canonical scorer that command uses.
	rep.Operations = append(rep.Operations, timeOnce("recompute_trust_full", timeout, func(ctx context.Context) (string, error) {
		conn, err := store.Open(ctx, dsn)
		if err != nil {
			return "", err
		}
		defer func() { _ = conn.Close() }()
		scorer, ok := conn.Claims.(ports.TrustScorer)
		if !ok {
			return "", errUnsupported
		}
		// The canonical model every writer uses (ADR 0026), scored at the
		// corpus anchor so the pass is reproducible.
		n, err := scorer.RecomputeTrust(ctx, trust.Scorer(p.Anchor))
		return fmt.Sprintf("%d beliefs rescored", n), err
	}))

	// 4. Reopening a populated store (migration check on existing data).
	rep.Operations = append(rep.Operations, timeOnce("open_populated_store", timeout, func(ctx context.Context) (string, error) {
		conn, err := store.Open(ctx, dsn)
		if err != nil {
			return "", err
		}
		return "", conn.Close()
	}))

	var mem mnemos.Memory
	rep.Operations = append(rep.Operations, timeOnce("library_open", timeout, func(ctx context.Context) (string, error) {
		var err error
		mem, err = mnemos.New(mnemos.WithStorage(dsn), mnemos.WithPassiveMode())
		return "", err
	}))
	if mem == nil {
		return finish(rep, peak, stopMem, dbPath, jsonOut)
	}
	defer func() { _ = mem.Close() }()

	g, _ := scalebench.NewGenerator(p)
	qs := g.QueryTexts(queries)
	ingestTexts := g.IngestTexts(ingests * max(repeat, 1))

	// The warm operations, measured repeat times on the same loaded brain.
	// One 1M-belief sample swings 2-4x with the OS page cache; the median of
	// several warm rounds is what a gate can compare against a ceiling.
	// Each round ingests fresh texts, so Remember never hits a dedupe path.
	var rounds [][]operation
	for round := 0; round < max(repeat, 1); round++ {
		texts := ingestTexts[round*ingests : (round+1)*ingests]
		rounds = append(rounds, warmOperations(mem, qs, texts, timeout))
	}
	rep.Operations = append(rep.Operations, medianRounds(rounds)...)
	if repeat > 1 {
		rep.Notes = append(rep.Notes, fmt.Sprintf("warm operations: median of %d rounds on the same loaded brain", repeat))
	}

	return finish(rep, peak, stopMem, dbPath, jsonOut)
}

var errUnsupported = errors.New("unsupported by backend")

func finish(rep Report, peak *atomic.Uint64, stop func(), dbPath, jsonOut string) error {
	stop()
	rep.Memory.PeakHeapInuseBytes = peak.Load()
	rep.Memory.MaxRSSBytes = maxRSS()
	if fi, err := os.Stat(dbPath); err == nil {
		rep.Memory.DBBytes = fi.Size()
		if wal, err := os.Stat(dbPath + "-wal"); err == nil {
			rep.Memory.DBBytes += wal.Size()
		}
	}
	printHuman(rep)
	if err := writeReport(rep, jsonOut); err != nil {
		return err
	}
	// The report is written first, so a failed gate still leaves the numbers.
	if checkFile != "" {
		return checkLimits(rep, checkFile)
	}
	return nil
}

func writeReport(rep Report, jsonOut string) error {
	if jsonOut == "" {
		return nil
	}
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	if jsonOut == "-" {
		_, err = os.Stdout.Write(append(data, '\n'))
		return err
	}
	return os.WriteFile(jsonOut, append(data, '\n'), 0o644)
}

// gateFile is the -check file: per corpus size, per shape, per operation,
// the slowest p50 and p95 that still pass. A ceiling is a regression alarm,
// not a target: it sits well above the measured value so a noisy run does not
// trip it and an order-of-magnitude regression always does.
type gateFile struct {
	// Beliefs is the corpus size the ceilings were set for; a run at any
	// other size is refused rather than compared.
	Beliefs int                             `json:"beliefs"`
	Limits  map[string]map[string]opCeiling `json:"limits"`
	Notes   []string                        `json:"notes,omitempty"`
}

type opCeiling struct {
	P50MS float64 `json:"p50_ms,omitempty"`
	P95MS float64 `json:"p95_ms,omitempty"`
}

// checkLimits compares rep against the ceilings for its shape and returns an
// error naming every operation that exceeded one, did not complete, or was
// gated but not measured.
func checkLimits(rep Report, path string) error {
	data, err := os.ReadFile(path) //nolint:gosec // operator-supplied gate file
	if err != nil {
		return err
	}
	var gate gateFile
	if err := json.Unmarshal(data, &gate); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if gate.Beliefs != rep.Params.Beliefs {
		return fmt.Errorf("gate %s is for %d beliefs, this run had %d", path, gate.Beliefs, rep.Params.Beliefs)
	}
	limits, ok := gate.Limits[rep.Params.Shape]
	if !ok {
		return fmt.Errorf("gate %s has no ceilings for shape %q", path, rep.Params.Shape)
	}
	var failures []string
	for _, op := range rep.Operations {
		c, ok := limits[op.Name]
		if !ok {
			continue
		}
		switch {
		case op.Outcome != "ok":
			failures = append(failures, fmt.Sprintf("%s: %s %s", op.Name, op.Outcome, op.Error))
		case c.P50MS > 0 && op.P50MS > c.P50MS:
			failures = append(failures, fmt.Sprintf("%s: p50 %.0f ms > ceiling %.0f ms", op.Name, op.P50MS, c.P50MS))
		case c.P95MS > 0 && op.P95MS > c.P95MS:
			failures = append(failures, fmt.Sprintf("%s: p95 %.0f ms > ceiling %.0f ms", op.Name, op.P95MS, c.P95MS))
		}
	}
	for name := range limits {
		if !slices.ContainsFunc(rep.Operations, func(o operation) bool { return o.Name == name }) {
			failures = append(failures, fmt.Sprintf("%s: gated but not measured", name))
		}
	}
	if len(failures) > 0 {
		slices.Sort(failures)
		return fmt.Errorf("scale gate failed (%s, %d beliefs):\n  %s", rep.Params.Shape, rep.Params.Beliefs, strings.Join(failures, "\n  "))
	}
	fmt.Printf("scale gate passed (%s, %d beliefs)\n", rep.Params.Shape, rep.Params.Beliefs)
	return nil
}

// profileOp names the one operation to CPU-profile, so a profile shows that
// operation and not the bulk load that dominates a run's wall time.
var profileOp, profileOut string

// repeat and checkFile are the gate flags (-repeat, -check).
var (
	repeat    int
	checkFile string
)

// profiling starts the CPU profile when name is the selected operation and
// returns the function that stops it.
func profiling(name string) func() {
	if name != profileOp {
		return func() {}
	}
	f, err := os.Create(profileOut)
	if err != nil {
		fmt.Fprintln(os.Stderr, "scalebench: cpuprofile:", err)
		return func() {}
	}
	if err := pprof.StartCPUProfile(f); err != nil {
		fmt.Fprintln(os.Stderr, "scalebench: cpuprofile:", err)
		_ = f.Close()
		return func() {}
	}
	return func() {
		pprof.StopCPUProfile()
		_ = f.Close()
		fmt.Fprintf(os.Stderr, "  cpu profile of %s written to %s\n", name, profileOut)
	}
}

func timeOnce(name string, timeout time.Duration, fn func(context.Context) (string, error)) operation {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	defer profiling(name)()
	start := time.Now()
	detail, err := fn(ctx)
	d := time.Since(start)
	op := operation{Name: name, Samples: 1, TotalMS: ms(d), P50MS: ms(d), P95MS: ms(d), MaxMS: ms(d), Outcome: "ok", Detail: detail}
	classify(&op, err)
	return op
}

func timeEach[T any](name string, timeout time.Duration, inputs []T, fn func(context.Context, T) error) operation {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	defer profiling(name)()
	var ds []time.Duration
	var total time.Duration
	op := operation{Name: name, Outcome: "ok"}
	for _, in := range inputs {
		start := time.Now()
		err := fn(ctx, in)
		d := time.Since(start)
		if err != nil {
			classify(&op, err)
			break
		}
		ds = append(ds, d)
		total += d
	}
	op.Samples = len(ds)
	op.TotalMS = ms(total)
	if len(ds) > 0 {
		slices.Sort(ds)
		op.P50MS = ms(ds[len(ds)/2])
		op.P95MS = ms(ds[min(len(ds)-1, len(ds)*95/100)])
		op.MaxMS = ms(ds[len(ds)-1])
		op.PerSecond = round2(float64(len(ds)) / total.Seconds())
		op.Detail = "ops/s"
	}
	return op
}

func classify(op *operation, err error) {
	switch {
	case err == nil:
	case errors.Is(err, context.DeadlineExceeded):
		op.Outcome = "timeout"
	case errors.Is(err, errUnsupported):
		op.Outcome = "unsupported"
	default:
		op.Outcome, op.Error = "error", err.Error()
	}
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// round2 keeps throughput at the precision it is meaningful to. Sixteen
// significant digits of ops/s are noise, and runs of them are long enough to
// trip secret/PII digit detectors in the committed reports.
func round2(v float64) float64 { return math.Round(v*100) / 100 }

// sampleHeap tracks peak HeapInuse every 50ms; a single ReadMemStats at the end
// would only see what survived the last GC.
func sampleHeap() (stop func(), peak *atomic.Uint64) {
	peak = &atomic.Uint64{}
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(50 * time.Millisecond)
		defer t.Stop()
		var ms runtime.MemStats
		for {
			runtime.ReadMemStats(&ms)
			if ms.HeapInuse > peak.Load() {
				peak.Store(ms.HeapInuse)
			}
			select {
			case <-done:
				return
			case <-t.C:
			}
		}
	}()
	return func() { close(done); wg.Wait() }, peak
}

func env() environment {
	e := environment{
		GoVersion: runtime.Version(), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
		NumCPU: runtime.NumCPU(), StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if out, err := exec.Command("git", "rev-parse", "HEAD").Output(); err == nil {
		e.Commit = strings.TrimSpace(string(out))
	}
	if out, err := exec.Command("git", "status", "--porcelain", "--untracked-files=no").Output(); err == nil {
		e.Dirty = len(bytes.TrimSpace(out)) > 0
	}
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("sysctl", "-n", "machdep.cpu.brand_string").Output(); err == nil {
			e.CPU = strings.TrimSpace(string(out))
		}
		if out, err := exec.Command("sysctl", "-n", "hw.memsize").Output(); err == nil {
			_, _ = fmt.Sscan(strings.TrimSpace(string(out)), &e.MemBytes)
		}
	} else if data, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, l := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(l, "model name") {
				e.CPU = strings.TrimSpace(strings.SplitN(l, ":", 2)[1])
				break
			}
		}
	}
	return e
}

func printHuman(r Report) {
	fmt.Printf("scalebench  %d beliefs  shape=%s seed=%d  commit=%.12s dirty=%v  %s/%s %s\n",
		r.Params.Beliefs, r.Params.Shape, r.Params.Seed, r.Environment.Commit, r.Environment.Dirty,
		r.Environment.GOOS, r.Environment.GOARCH, r.Environment.CPU)
	fmt.Printf("corpus: %d episodes, %d beliefs, %d evidence, %d associations (%d contradicts), %d topics\n",
		r.Corpus.Events, r.Corpus.Claims, r.Corpus.Evidence, r.Corpus.Relationships, r.Corpus.Contradicts, r.Corpus.Topics)
	fmt.Printf("%-22s %8s %11s %11s %11s %12s  %s\n", "operation", "samples", "p50 ms", "p95 ms", "max ms", "per_second", "outcome")
	for _, o := range r.Operations {
		fmt.Printf("%-22s %8d %11.2f %11.2f %11.2f %12.1f  %s %s\n", o.Name, o.Samples, o.P50MS, o.P95MS, o.MaxMS, o.PerSecond, o.Outcome, o.Error)
	}
	fmt.Printf("peak heap in-use %.1f MiB, max RSS %.1f MiB, db %.1f MiB\n",
		float64(r.Memory.PeakHeapInuseBytes)/(1<<20), float64(r.Memory.MaxRSSBytes)/(1<<20), float64(r.Memory.DBBytes)/(1<<20))
}

// warmOperations runs one round of the operations measured on a loaded brain.
func warmOperations(mem mnemos.Memory, qs, texts []string, timeout time.Duration) []operation {
	var ops []operation
	ops = append(ops, timeEach("recall_hops0", timeout, qs, func(ctx context.Context, q string) error {
		_, err := mem.Recall(ctx, mnemos.Query{Text: q, Limit: 10})
		return err
	}))
	ops = append(ops, timeEach("recall_hops1", timeout, qs, func(ctx context.Context, q string) error {
		_, err := mem.Recall(ctx, mnemos.Query{Text: q, Limit: 10, Hops: 1})
		return err
	}))
	ops = append(ops, timeOnce("brain_health", timeout, func(ctx context.Context) (string, error) {
		h, err := mem.BrainHealth(ctx)
		return fmt.Sprintf("status=%s mode=%s", h.Status, h.Mode), err
	}))
	// The exact scan BrainHealth falls back from above 50k live beliefs.
	ops = append(ops, timeOnce("brain_health_full", timeout, func(ctx context.Context) (string, error) {
		h, err := mem.BrainHealthFull(ctx)
		return fmt.Sprintf("status=%s", h.Status), err
	}))
	ops = append(ops, timeOnce("knowledge_gaps", timeout, func(ctx context.Context) (string, error) {
		gaps, err := mem.KnowledgeGaps(ctx, 20)
		return fmt.Sprintf("%d gaps", len(gaps)), err
	}))
	ops = append(ops, timeEach("ingest_remember", timeout, texts, func(ctx context.Context, text string) error {
		return mem.Remember(ctx, mnemos.Item{Type: "fact", Content: text})
	}))

	return ops
}

// medianRounds folds repeated rounds into one operation per name: the median
// p50 and p95 across rounds, the worst max, and the first failure if any round
// did not complete.
func medianRounds(rounds [][]operation) []operation {
	if len(rounds) == 1 {
		return rounds[0]
	}
	var out []operation
	for i, first := range rounds[0] {
		op := first
		var p50s, p95s []float64
		for _, r := range rounds {
			o := r[i]
			p50s, p95s = append(p50s, o.P50MS), append(p95s, o.P95MS)
			op.MaxMS = math.Max(op.MaxMS, o.MaxMS)
			if o.Outcome != "ok" && op.Outcome == "ok" {
				op.Outcome, op.Error = o.Outcome, o.Error
			}
		}
		op.P50MS, op.P95MS = median(p50s), median(p95s)
		op.Detail = fmt.Sprintf("%s (median of %d rounds)", first.Detail, len(rounds))
		out = append(out, op)
	}
	return out
}

func median(xs []float64) float64 {
	s := slices.Clone(xs)
	slices.Sort(s)
	return s[len(s)/2]
}
