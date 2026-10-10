// Command memeval is the Mnemos side of the answer-quality evaluation (#441).
//
// It reads conversation histories ("haystacks") as JSON lines, writes each one
// into a fresh brain through the public library (Remember, one call per
// session), then recalls against each question and writes what Mnemos would
// hand an agent: the recalled beliefs, their trust, and the sessions their
// evidence came from (for retrieval recall). Answering and judging happen in
// the Python harness (benchmarks/), identically for every system under test.
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
}

// Answer is the recall for one question.
type Answer struct {
	QuestionID string     `json:"question_id"`
	Recalled   []Recalled `json:"recalled"`
	RecallMS   int64      `json:"recall_ms"`
}

// Output is one line of the result file.
type Output struct {
	HaystackID  string   `json:"haystack_id"`
	Claims      int      `json:"claims"`
	IngestMS    int64    `json:"ingest_ms"`
	RelateSkips int64    `json:"relate_skips"`
	Answers     []Answer `json:"answers,omitempty"`
	Error       string   `json:"error,omitempty"`
}

func main() {
	in := flag.String("in", "", "haystacks, one JSON object per line")
	out := flag.String("out", "", "results, appended; haystacks already present are skipped")
	workdir := flag.String("workdir", "", "directory for the throwaway brains")
	workers := flag.Int("workers", 4, "haystacks processed concurrently")
	limit := flag.Int("limit", 50, "beliefs recalled per question")
	keep := flag.Bool("keep", false, "keep each brain's database after use")
	recallOnly := flag.Bool("recall-only", false, "skip ingest; recall against brains kept by an earlier -keep run")
	flag.Parse()
	if *in == "" || *out == "" || *workdir == "" {
		fmt.Fprintln(os.Stderr, "usage: memeval -in FILE -out FILE -workdir DIR [-workers N] [-limit N]")
		os.Exit(2)
	}
	if *recallOnly {
		*keep = true
	}
	if err := run(*in, *out, *workdir, *workers, *limit, *keep, *recallOnly); err != nil {
		fmt.Fprintln(os.Stderr, "memeval:", err)
		os.Exit(1)
	}
}

func run(in, out, workdir string, workers, limit int, keep, recallOnly bool) error {
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
				o := evaluate(h, workdir, limit, keep, recallOnly, cfg)
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

func evaluate(h Haystack, workdir string, limit int, keep, recallOnly bool, cfg mnemos.ProviderConfig) Output {
	o := Output{HaystackID: h.ID}
	ctx := context.Background()
	path := filepath.Join(workdir, sanitize(h.ID)+".db")
	if recallOnly {
		if _, err := os.Stat(path); err != nil {
			o.Error = "recall-only: no kept brain: " + err.Error()
			return o
		}
	} else {
		_ = os.Remove(path)
	}
	if !keep {
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
	if recallOnly {
		sessions = nil
	}
	for _, s := range sessions {
		content := "Session date: " + s.Date + "\n\n" + s.Text
		err := mem.Remember(ctx, mnemos.Item{
			Type:     "conversation",
			Content:  content,
			Source:   "memeval",
			Metadata: map[string]string{"session_id": s.ID, "session_date": s.Date},
		})
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

	for _, q := range h.Questions {
		t := time.Now()
		results, err := mem.Recall(ctx, mnemos.Query{Text: q.Text, Limit: limit})
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
				SessionIDs: src.ids, Dates: src.dates,
			})
		}
		o.Answers = append(o.Answers, a)
	}
	return o
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
