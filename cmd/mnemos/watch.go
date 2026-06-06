package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.klarlabs.de/mnemos/internal/ingest"
	"go.klarlabs.de/mnemos/internal/parser"
	"go.klarlabs.de/mnemos/internal/pipeline"
	"go.klarlabs.de/mnemos/internal/relate"
	"go.klarlabs.de/mnemos/internal/store"
)

// defaultWatchInterval is how often the watcher polls each registered file.
// Short enough to feel responsive; long enough not to thrash on agent edits.
const defaultWatchInterval = 5 * time.Second

// Watcher tracks a set of files and re-ingests them when content changes.
// State is in-memory only — MCP restarts wipe the watch set, and the agent
// re-registers paths as needed. Polling-based to avoid the cross-platform
// quirks of fsnotify on stdio servers; the cost is a few extra stat calls
// per interval.
type Watcher struct {
	mu       sync.Mutex
	hashes   map[string]string // absolute path → sha256 hex of last-seen content
	conn     *store.Conn
	interval time.Duration
	actor    string // user id to stamp as created_by on re-ingested events/claims

	startOnce sync.Once
	stopCh    chan struct{}
}

// NewWatcher returns a watcher that re-ingests changed files into the
// store conn. The background goroutine is not started until the first
// Add call. actor is the user id stamped on everything the watcher
// persists; pass domain.SystemUser (or empty) to attribute writes to
// the system.
func NewWatcher(conn *store.Conn, actor string) *Watcher {
	return &Watcher{
		hashes:   make(map[string]string),
		conn:     conn,
		interval: defaultWatchInterval,
		actor:    actor,
		stopCh:   make(chan struct{}),
	}
}

// Add registers path for watching. The current content hash is recorded but
// no ingest happens — auto-ingest or an explicit process_text call is
// expected to have brought the file into the DB already. Returns the number
// of files currently being watched after this call.
func (w *Watcher) Add(path string) (int, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return 0, fmt.Errorf("resolve path: %w", err)
	}
	hash, err := hashFile(abs)
	if err != nil {
		return 0, err
	}

	w.mu.Lock()
	w.hashes[abs] = hash
	count := len(w.hashes)
	w.mu.Unlock()

	w.startOnce.Do(func() { go w.loop(context.Background()) })
	return count, nil
}

// Watching reports whether path is currently in the watch set.
func (w *Watcher) Watching(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.hashes[abs]
	return ok
}

// Count returns the number of paths currently being watched.
func (w *Watcher) Count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.hashes)
}

// Stop signals the polling loop to exit. Safe to call from anywhere; further
// Add calls after Stop will not restart the loop.
func (w *Watcher) Stop() {
	select {
	case <-w.stopCh:
		// already closed
	default:
		close(w.stopCh)
	}
}

func (w *Watcher) loop(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-w.stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.tick(ctx)
		}
	}
}

// tick re-hashes every watched file and re-ingests any whose content has
// changed since the last observation. Disappearing files are dropped from
// the set with a stderr note. Returns counts for tests.
func (w *Watcher) tick(ctx context.Context) (changed, removed int) {
	w.mu.Lock()
	snapshot := make(map[string]string, len(w.hashes))
	for k, v := range w.hashes {
		snapshot[k] = v
	}
	w.mu.Unlock()

	service := ingest.NewService()
	normalizer := parser.NewNormalizer()
	extractor, err := pipeline.NewExtractor(false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "watch: failed to build extractor: %v\n", err)
		return 0, 0
	}
	relEngine := relate.NewEngine()

	for path, oldHash := range snapshot {
		newHash, err := hashFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				w.mu.Lock()
				delete(w.hashes, path)
				w.mu.Unlock()
				removed++
				fmt.Fprintf(os.Stderr, "watch: dropped missing file %s\n", path)
				continue
			}
			fmt.Fprintf(os.Stderr, "watch: hash %s: %v\n", path, err)
			continue
		}
		if newHash == oldHash {
			continue
		}

		runID := fmt.Sprintf("watch-%s", time.Now().UTC().Format("20060102T150405"))
		if err := ingestSingleDoc(ctx, w.conn, service, normalizer, extractor, relEngine, runID, path, w.actor); err != nil {
			fmt.Fprintf(os.Stderr, "watch: re-ingest %s: %v\n", path, err)
			continue
		}

		w.mu.Lock()
		w.hashes[path] = newHash
		w.mu.Unlock()
		changed++
		fmt.Fprintf(os.Stderr, "watch: re-ingested %s\n", path)
	}

	return changed, removed
}

// hashFile returns the hex-encoded sha256 of the file at path.
func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: watcher reads user-registered files by design
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
