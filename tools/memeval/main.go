// Command memeval is the Mnemos side of the answer-quality evaluation (#441).
//
// It reads conversation histories ("haystacks") as JSON lines, writes each one
// into a fresh brain through the public library (Remember, one call per
// session), then recalls against each question and writes what Mnemos would
// hand an agent: the recalled beliefs, their trust, and the sessions their
// evidence came from (for retrieval recall). Answering and judging happen in
// the Python harness (benchmarks/), identically for every system under test.
//
// With -session-time, each session's date is passed as Item.At, so Mnemos
// resolves relative dates ("yesterday", "last week") in the beliefs it
// extracts against the day the session took place.
//
// Every brain is a throwaway SQLite file under -workdir. The tool refuses a
// workdir inside the default brain directory, because the library's default
// DSN resolution would otherwise point at a real brain.
//
//	go run ./tools/memeval -in haystacks.jsonl -out mnemos.jsonl -workdir /tmp/memeval
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.klarlabs.de/mnemos"
	"go.klarlabs.de/mnemos/internal/pipeline"
	"go.klarlabs.de/mnemos/internal/store"
	_ "go.klarlabs.de/mnemos/sqlite"
)

// Haystack is one conversation history and the questions asked of it.
type Haystack struct {
	ID        string     `json:"id"`
	Sessions  []Session  `json:"sessions"`
	Questions []Question `json:"questions"`
}

// Session is one conversation session, written as one episode.
type Session struct {
	ID   string `json:"id"`
	Date string `json:"date"`
	Text string `json:"text"`
}

// Question is asked after every session is written.
type Question struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// Recalled is one belief Mnemos returned for a question.
type Recalled struct {
	ClaimID    string   `json:"claim_id"`
	Text       string   `json:"text"`
	Trust      float64  `json:"trust"`
	SessionIDs []string `json:"session_ids"`
	Dates      []string `json:"dates"`
	// Excerpt is the belief's source excerpt (ADR 0031), set with -excerpts.
	Excerpt string `json:"excerpt,omitempty"`
}

// Passage is one stored passage Mnemos returned for a question, set with
// -passages (Memory.Passages).
type Passage struct {
	EpisodeID string  `json:"episode_id"`
	Text      string  `json:"text"`
	SessionID string  `json:"session_id,omitempty"`
	Date      string  `json:"date,omitempty"`
	Score     float64 `json:"score"`
}

// Answer is the recall for one question.
type Answer struct {
	QuestionID string     `json:"question_id"`
	Recalled   []Recalled `json:"recalled"`
	Passages   []Passage  `json:"passages,omitempty"`
	RecallMS   int64      `json:"recall_ms"`
}

// Output is one line of the result file.
type Output struct {
	HaystackID  string `json:"haystack_id"`
	Claims      int    `json:"claims"`
	IngestMS    int64  `json:"ingest_ms"`
	RelateSkips int64  `json:"relate_skips"`
	// EmbedWaitMS is how long recall waited for background embeds to settle;
	// UnembeddedClaims is how many beliefs still had no vector when it began.
	EmbedWaitMS      int64    `json:"embed_wait_ms"`
	UnembeddedClaims int      `json:"unembedded_claims"`
	Answers          []Answer `json:"answers,omitempty"`
	Error            string   `json:"error,omitempty"`
}

func main() {
	in := flag.String("in", "", "haystacks, one JSON object per line")
	out := flag.String("out", "", "results, appended; haystacks already present are skipped")
	workdir := flag.String("workdir", "", "directory for the throwaway brains")
	workers := flag.Int("workers", 4, "haystacks processed concurrently")
	limit := flag.Int("limit", 50, "beliefs recalled per question")
	keep := flag.Bool("keep", false, "keep each brain's database after use")
	recallOnly := flag.Bool("recall-only", false, "skip ingest; recall against brains kept by an earlier -keep run")
	excerpts := flag.Bool("excerpts", false, "ask recall for each belief's source excerpt (Query.Excerpts, ADR 0031)")
	sessionAt := flag.Bool("session-time", false, "pass each session's date as Item.At, so relative dates in beliefs are resolved against it")
	passages := flag.Int("passages", 0, "also return this many stored passages per question (Memory.Passages); 0 is off")
	flag.Parse()
	if *in == "" || *out == "" || *workdir == "" {
		fmt.Fprintln(os.Stderr, "usage: memeval -in FILE -out FILE -workdir DIR [-workers N] [-limit N] [-keep] [-recall-only] [-excerpts] [-session-time] [-passages N]")
		os.Exit(2)
	}
	if *recallOnly {
		*keep = true
	}
	opts := options{limit: *limit, keep: *keep, recallOnly: *recallOnly, excerpts: *excerpts, sessionTime: *sessionAt, passages: *passages}
	if err := run(*in, *out, *workdir, *workers, opts); err != nil {
		fmt.Fprintln(os.Stderr, "memeval:", err)
		os.Exit(1)
	}
}

// options are the per-haystack evaluation settings.
type options struct {
	limit       int
	keep        bool
	recallOnly  bool
	excerpts    bool
	sessionTime bool
	passages    int
}

func run(in, out, workdir string, workers int, opts options) error {
	if err := guardWorkdir(workdir); err != nil {
		return err
	}
	if err := os.MkdirAll(workdir, 0o750); err != nil {
		return err
	}
	cfg, err := providerConfig()
	if err != nil {
		return err
	}
	done, err := finished(out)
	if err != nil {
		return err
	}
	haystacks, err := readHaystacks(in)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(out, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	var mu sync.Mutex
	jobs := make(chan Haystack)
	var wg sync.WaitGroup
	for range max(1, workers) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for h := range jobs {
				o := evaluate(h, workdir, opts, cfg)
				line, _ := json.Marshal(o)
				mu.Lock()
				_, _ = f.Write(append(line, '\n'))
				mu.Unlock()
				status := "ok"
				if o.Error != "" {
					status = "ERROR " + o.Error
				}
				fmt.Fprintf(os.Stderr, "%s claims=%d ingest=%dms %s\n", h.ID, o.Claims, o.IngestMS, status)
			}
		}()
	}
	for _, h := range haystacks {
		if done[h.ID] {
			continue
		}
		jobs <- h
	}
	close(jobs)
	wg.Wait()
	return nil
}

// guardWorkdir refuses any workdir under the default brain directory.
func guardWorkdir(workdir string) error {
	abs, err := filepath.Abs(workdir)
	if err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	for _, forbidden := range []string{
		filepath.Join(home, ".local", "share", "mnemos"),
		filepath.Join(home, ".mnemos"),
	} {
		if abs == forbidden || strings.HasPrefix(abs, forbidden+string(os.PathSeparator)) {
			return fmt.Errorf("refusing workdir %s: it is inside a real brain directory", abs)
		}
	}
	return nil
}

// providerConfig reads the extraction and embedding providers from the
// environment. Extraction is required: a run without it would silently
// measure the rule-based extractor instead.
func providerConfig() (mnemos.ProviderConfig, error) {
	cfg := mnemos.ProviderConfig{
		LLMProvider:   os.Getenv("MEMEVAL_LLM_PROVIDER"),
		LLMAPIKey:     os.Getenv("MEMEVAL_LLM_API_KEY"),
		LLMModel:      os.Getenv("MEMEVAL_LLM_MODEL"),
		LLMBaseURL:    os.Getenv("MEMEVAL_LLM_BASE_URL"),
		EmbedProvider: os.Getenv("MEMEVAL_EMBED_PROVIDER"),
		EmbedModel:    os.Getenv("MEMEVAL_EMBED_MODEL"),
		EmbedBaseURL:  os.Getenv("MEMEVAL_EMBED_BASE_URL"),
	}
	if cfg.LLMProvider == "" || cfg.LLMModel == "" {
		return cfg, errors.New("MEMEVAL_LLM_PROVIDER and MEMEVAL_LLM_MODEL are required")
	}
	return cfg, nil
}

func evaluate(h Haystack, workdir string, opts options, cfg mnemos.ProviderConfig) Output {
	o := Output{HaystackID: h.ID}
	ctx := context.Background()
	path := filepath.Join(workdir, sanitize(h.ID)+".db")
	if opts.recallOnly {
		if _, err := os.Stat(path); err != nil {
			o.Error = "recall-only: no kept brain: " + err.Error()
			return o
		}
	} else {
		_ = os.Remove(path)
	}
	if !opts.keep {
		defer func() {
			for _, suffix := range []string{"", "-wal", "-shm"} {
				_ = os.Remove(path + suffix)
			}
		}()
	}
	dsn := "sqlite://" + path
	mem, err := mnemos.New(mnemos.WithStorage(dsn), mnemos.WithEnhancedMode(cfg))
	if err != nil {
		o.Error = "open: " + err.Error()
		return o
	}
	defer func() { _ = mem.Close() }()

	skipsBefore := pipeline.RelateSkips()
	start := time.Now()
	sessions := h.Sessions
	if opts.recallOnly {
		sessions = nil
	}
	for _, s := range sessions {
		content := "Session date: " + s.Date + "\n\n" + s.Text
		item := mnemos.Item{
			Type:     "conversation",
			Content:  content,
			Source:   "memeval",
			Metadata: map[string]string{"session_id": s.ID, "session_date": s.Date},
		}
		if opts.sessionTime {
			item.At, _ = sessionTime(s.Date)
		}
		err := mem.Remember(ctx, item)
		if err != nil {
			o.Error = fmt.Sprintf("remember %s: %v", s.ID, err)
			return o
		}
	}
	o.IngestMS = time.Since(start).Milliseconds()
	o.RelateSkips = pipeline.RelateSkips() - skipsBefore

	conn, err := store.Open(ctx, dsn)
	if err != nil {
		o.Error = "open store: " + err.Error()
		return o
	}
	defer func() { _ = conn.Close() }()
	if n, err := conn.Claims.CountAll(ctx); err == nil {
		o.Claims = int(n)
	}

	// Remember embeds in the background, a few at a time, so a large haystack
	// is still embedding when its last session returns. A user recalls long
	// after writing; recalling at once would measure a half-embedded brain.
	waited := time.Now()
	if err := awaitEmbeddings(ctx, conn); err != nil {
		o.Error = "await embeddings: " + err.Error()
		return o
	}
	o.EmbedWaitMS = time.Since(waited).Milliseconds()
	if missing, err := conn.Claims.ListIDsMissingEmbedding(ctx); err == nil {
		o.UnembeddedClaims = len(missing)
	}

	for _, q := range h.Questions {
		t := time.Now()
		results, err := mem.Recall(ctx, mnemos.Query{Text: q.Text, Limit: opts.limit, Excerpts: opts.excerpts})
		if err != nil {
			o.Error = fmt.Sprintf("recall %s: %v", q.ID, err)
			return o
		}
		a := Answer{QuestionID: q.ID, RecallMS: time.Since(t).Milliseconds()}
		sources, err := sessionsOf(ctx, conn, results)
		if err != nil {
			o.Error = fmt.Sprintf("sources %s: %v", q.ID, err)
			return o
		}
		for _, r := range results {
			src := sources[r.ClaimID]
			a.Recalled = append(a.Recalled, Recalled{
				ClaimID: r.ClaimID, Text: r.Text, Trust: r.TrustScore,
				SessionIDs: src.ids, Dates: src.dates, Excerpt: r.Excerpt,
			})
		}
		if opts.passages > 0 {
			ps, err := mem.Passages(ctx, mnemos.PassageQuery{Text: q.Text, Limit: opts.passages})
			if err != nil {
				o.Error = fmt.Sprintf("passages %s: %v", q.ID, err)
				return o
			}
			for _, p := range ps {
				a.Passages = append(a.Passages, Passage{
					EpisodeID: p.EpisodeID, Text: p.Text, Score: p.Score,
					SessionID: p.Metadata["input_session_id"], Date: p.Metadata["input_session_date"],
				})
			}
		}
		a.RecallMS = time.Since(t).Milliseconds()
		o.Answers = append(o.Answers, a)
	}
	return o
}

// embedSettle is how long the vector count must hold still before recall
// starts; embedWaitMax bounds the wait if the embedder stalls.
const (
	embedSettle  = 15 * time.Second
	embedWaitMax = 30 * time.Minute
)

// awaitEmbeddings waits until the brain's vector count stops growing.
func awaitEmbeddings(ctx context.Context, conn *store.Conn) error {
	deadline := time.Now().Add(embedWaitMax)
	last, stableSince := int64(-1), time.Now()
	for time.Now().Before(deadline) {
		n, err := conn.Embeddings.CountAll(ctx)
		if err != nil {
			return err
		}
		if n != last {
			last, stableSince = n, time.Now()
		} else if time.Since(stableSince) >= embedSettle {
			return nil
		}
		time.Sleep(time.Second)
	}
	return nil
}

// sessionLayouts are the benchmarks' session date formats: LoCoMo
// ("1:56 pm on 8 May, 2023") and LongMemEval ("2023/05/20 (Sat) 02:21").
var sessionLayouts = []string{"3:04 pm on 2 January, 2006", "2006/01/02 (Mon) 15:04"}

// sessionTime parses a session date. A date in no known format is not
// guessed: the session is then written without Item.At.
func sessionTime(date string) (time.Time, bool) {
	d := strings.TrimSpace(date)
	for _, layout := range sessionLayouts {
		if t, err := time.Parse(layout, d); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

type sessionSet struct{ ids, dates []string }

// sessionsOf maps each recalled claim to the sessions its evidence came from,
// through the claim's evidence links and each episode's metadata.
func sessionsOf(ctx context.Context, conn *store.Conn, results []mnemos.Result) (map[string]sessionSet, error) {
	ids := make([]string, 0, len(results))
	for _, r := range results {
		ids = append(ids, r.ClaimID)
	}
	links, err := conn.Claims.ListEvidenceByClaimIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	eventIDs := make([]string, 0, len(links))
	for _, l := range links {
		eventIDs = append(eventIDs, l.EventID)
	}
	events, err := conn.Events.ListByIDs(ctx, eventIDs)
	if err != nil {
		return nil, err
	}
	meta := make(map[string]map[string]string, len(events))
	for _, e := range events {
		meta[e.ID] = e.Metadata
	}
	out := make(map[string]sessionSet, len(results))
	for _, l := range links {
		// Ingest stores Item.Metadata on the event with an "input_" prefix.
		m := meta[l.EventID]
		sid := m["input_session_id"]
		if sid == "" {
			continue
		}
		s := out[l.ClaimID]
		if !contains(s.ids, sid) {
			s.ids = append(s.ids, sid)
			s.dates = append(s.dates, m["input_session_date"])
		}
		out[l.ClaimID] = s
	}
	return out, nil
}

func readHaystacks(path string) ([]Haystack, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []Haystack
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		if len(strings.TrimSpace(sc.Text())) == 0 {
			continue
		}
		var h Haystack
		if err := json.Unmarshal(sc.Bytes(), &h); err != nil {
			return nil, fmt.Errorf("parse haystack: %w", err)
		}
		out = append(out, h)
	}
	return out, sc.Err()
}

// finished lists the haystacks the output already holds without an error, so
// an interrupted run resumes instead of paying for them twice.
func finished(path string) (map[string]bool, error) {
	done := map[string]bool{}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return done, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var o Output
		if json.Unmarshal(sc.Bytes(), &o) == nil && o.Error == "" {
			done[o.HaystackID] = true
		}
	}
	return done, sc.Err()
}

func sanitize(id string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || r == ' ' {
			return '_'
		}
		return r
	}, id)
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
