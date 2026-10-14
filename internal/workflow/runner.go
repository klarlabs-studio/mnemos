package workflow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"time"

	"go.klarlabs.de/bolt"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/statekit"
)

type jobStore interface {
	Upsert(context.Context, domain.CompilationJob) error
}

// Runner executes compilation jobs with timeout, retry, and status tracking.
type Runner struct {
	Store      jobStore
	Timeout    time.Duration
	MaxRetries int
	Verbose    bool
	// ReadOnly marks a job that only reads (query, quality, metrics). Its
	// record is never written, so a read never needs the store's write lock:
	// on SQLite a long consolidation holds that lock for hours, and a query
	// that had to record itself first failed with SQLITE_BUSY before reading
	// anything. Status transitions are still validated.
	ReadOnly bool
	now      func() time.Time
	nextID   func() (string, error)
	logger   *bolt.Logger
}

// Job represents a single in-flight compilation job managed by a Runner.
type Job struct {
	id     string
	runner *Runner
	data   domain.CompilationJob
	fsm    *statekit.Interpreter[struct{}]
	// unrecorded stops persistence for the rest of the job: set for a
	// read-only job, and after the first record write fails. Each failed write
	// on a locked SQLite store costs a full busy_timeout, so trying again on
	// every status change would turn one lost record into a stalled job.
	unrecorded bool
}

// NewRunner returns a Runner with sensible defaults for timeout and retries.
func NewRunner(store jobStore) Runner {
	return Runner{
		Store:      store,
		Timeout:    10 * time.Second,
		MaxRetries: 2,
		now:        time.Now,
		nextID:     newJobID,
		logger:     bolt.New(bolt.NewJSONHandler(os.Stderr)),
	}
}

// Run creates a job of the given kind and executes fn with retry and timeout
// handling. Each attempt gets
// a context bounded by r.Timeout and derived from parent, so a caller's
// deadline or cancellation reaches the job body. Pass context.Background() when
// there is no caller deadline to honor.
func (r Runner) Run(parent context.Context, kind string, scope map[string]string, fn func(context.Context, *Job) error) error {
	jobID, err := r.nextID()
	if err != nil {
		return fmt.Errorf("generate job id: %w", err)
	}
	now := r.now().UTC()
	jobData := domain.CompilationJob{
		ID:        jobID,
		Kind:      kind,
		Status:    "pending",
		Scope:     scope,
		StartedAt: now,
		UpdatedAt: now,
		Error:     "",
	}
	fsm, err := newStatusMachine()
	if err != nil {
		return fmt.Errorf("create workflow state machine: %w", err)
	}
	fsm.Start()

	job := &Job{id: jobID, runner: &r, data: jobData, fsm: fsm, unrecorded: r.ReadOnly}
	job.record()
	job.log("started", map[string]any{"attempt": 0})

	var lastErr error
	for attempt := 1; attempt <= r.MaxRetries+1; attempt++ {
		if err := job.SetStatus("running", ""); err != nil {
			return err
		}
		job.log("attempt", map[string]any{"attempt": attempt})

		// Derive from parent, not context.Background(): r.Timeout caps a single
		// attempt, but the caller's deadline still applies (WithTimeout keeps
		// whichever is earlier) and caller cancellation propagates into fn.
		ctx, cancel := context.WithTimeout(parent, r.Timeout)
		start := r.now()
		err := fn(ctx, job)
		cancel()

		duration := r.now().Sub(start).Milliseconds()
		if err == nil {
			if err := job.SetStatus("completed", ""); err != nil {
				return err
			}
			job.log("completed", map[string]any{"attempt": attempt, "duration_ms": duration})
			return nil
		}

		lastErr = err
		job.log("attempt_failed", map[string]any{"attempt": attempt, "duration_ms": duration, "error": err.Error()})
		if attempt <= r.MaxRetries {
			if err := job.SetStatus("retrying", err.Error()); err != nil {
				return err
			}
			continue
		}
	}

	if err := job.SetStatus("failed", lastErr.Error()); err != nil {
		return err
	}
	job.log("failed", map[string]any{"error": lastErr.Error()})
	return lastErr
}

// ID returns the unique identifier of the job.
func (j *Job) ID() string {
	return j.id
}

// SetStatus transitions the job to the given status and persists the change.
func (j *Job) SetStatus(status, errMsg string) error {
	if j.fsm != nil {
		before := j.fsm.State().Value
		j.fsm.Send(statekit.Event{Type: statekit.EventType(status)})
		after := j.fsm.State().Value
		if before == after && string(after) != status {
			return fmt.Errorf("invalid workflow status transition: %s -> %s", before, status)
		}
	}

	j.data.Status = status
	j.data.Error = errMsg
	j.data.UpdatedAt = j.runner.now().UTC()
	j.record()
	return nil
}

// record persists the job's current state, best-effort. The record is
// bookkeeping — nothing reads it back to decide anything — so failing to write
// it must never fail the work it describes. The first failure is logged at
// debug and persistence stops for the rest of the job.
func (j *Job) record() {
	if j.unrecorded {
		return
	}
	if err := j.runner.Store.Upsert(context.Background(), j.data); err != nil {
		j.unrecorded = true
		if j.runner.logger != nil {
			j.runner.logger.Debug().
				Str("job_id", j.id).
				Str("kind", j.data.Kind).
				Str("status", j.data.Status).
				Str("error", err.Error()).
				Msg("workflow: job record not written; continuing without it")
		}
	}
}

func (j *Job) log(stage string, fields map[string]any) {
	if !j.runner.Verbose {
		return
	}
	event := j.runner.logger.Info().
		Str("job_id", j.id).
		Str("kind", j.data.Kind).
		Str("stage", stage).
		Str("status", j.data.Status)
	if errMsg := j.data.Error; errMsg != "" {
		event = event.Str("error", errMsg)
	}
	for k, v := range fields {
		event = event.Any(k, v)
	}
	event.Msg("workflow")
}

func newJobID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "job_" + hex.EncodeToString(buf), nil
}
