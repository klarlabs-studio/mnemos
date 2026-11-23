package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Capture workers are spawned after every assistant reply in every open
// session, detached, with nothing bounding them. Each one opens the brain,
// extracts through the local model (which Ollama serves one request at a
// time, so the rest wait holding their memory), relates the new claims
// against the whole store and rescores trust. With five sessions open they
// piled up: three workers at ~50% CPU each was an ordinary sample, on a
// machine that then ran short of memory. Two workers for the SAME session
// could also overlap, and since the offset is saved only after a chunk is
// stored, both captured the same lines.
//
// So a worker must hold two locks: its session's, and one of a fixed number
// of machine-wide slots. The incremental worker gives up when either is busy,
// which loses nothing: the offset has not moved, so the next reply's worker
// (or the SessionEnd sweep) takes the same lines. The SessionEnd sweep is the
// session's last chance, so it waits for both, inside its own budget.

// maxCaptureWorkers bounds how many capture workers run at once on one
// machine, across all sessions. Two keeps one extracting while another
// relates and writes; more only queue on the model and the write lock.
const maxCaptureWorkers = 2

// captureLockPoll is how often a waiting SessionEnd sweep retries the locks.
const captureLockPoll = 250 * time.Millisecond

// errLockHeld reports that another worker holds a lock.
var errLockHeld = errors.New("lock held")

// captureLocks is what a worker holds while it captures.
type captureLocks struct{ files []*os.File }

// release drops every lock. Closing the file releases it.
func (l *captureLocks) release() {
	for _, f := range l.files {
		_ = f.Close()
	}
	l.files = nil
}

// tryCaptureLocks takes the session's lock and a free worker slot, or neither.
// It returns errLockHeld when either is busy. A session without an id has no
// offset to protect, so it needs only the slot.
func tryCaptureLocks(sessionID string) (*captureLocks, error) {
	dir := captureStateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	held := &captureLocks{}
	if strings.TrimSpace(sessionID) != "" {
		sum := sha256.Sum256([]byte(sessionID))
		f, err := lockPath(filepath.Join(dir, hex.EncodeToString(sum[:16])+".lock"))
		if err != nil {
			return nil, err
		}
		held.files = append(held.files, f)
	}
	for i := range maxCaptureWorkers {
		f, err := lockPath(filepath.Join(dir, fmt.Sprintf("worker-%d.lock", i)))
		if errors.Is(err, errLockHeld) {
			continue
		}
		if err != nil {
			held.release()
			return nil, err
		}
		held.files = append(held.files, f)
		return held, nil
	}
	held.release()
	return nil, errLockHeld
}

// waitCaptureLocks retries tryCaptureLocks until it succeeds or ctx ends.
func waitCaptureLocks(ctx context.Context, sessionID string) (*captureLocks, error) {
	for {
		held, err := tryCaptureLocks(sessionID)
		if !errors.Is(err, errLockHeld) {
			return held, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(captureLockPoll):
		}
	}
}

// lockPath opens path and locks it, closing it again when the lock is taken.
func lockPath(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // path is under the capture state dir
	if err != nil {
		return nil, err
	}
	if err := tryLockFile(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}
