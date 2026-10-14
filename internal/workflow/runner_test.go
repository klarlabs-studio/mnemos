package workflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

type memStore struct {
	jobs map[string]domain.CompilationJob
}

func (m *memStore) Upsert(_ context.Context, job domain.CompilationJob) error {
	if m.jobs == nil {
		m.jobs = map[string]domain.CompilationJob{}
	}
	m.jobs[job.ID] = job
	return nil
}

func TestRunnerCompletesJob(t *testing.T) {
	store := &memStore{}
	runner := NewRunner(store)
	runner.Timeout = time.Second
	runner.MaxRetries = 0
	runner.nextID = func() (string, error) { return "job_test", nil }

	err := runner.Run(context.Background(), "ingest", map[string]string{"path": "README.md"}, func(_ context.Context, job *Job) error {
		if err := job.SetStatus("loading", ""); err != nil {
			return err
		}
		return job.SetStatus("saving", "")
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if store.jobs["job_test"].Status != "completed" {
		t.Fatalf("final status = %q, want completed", store.jobs["job_test"].Status)
	}
}

func TestRunnerRetriesAndFails(t *testing.T) {
	store := &memStore{}
	runner := NewRunner(store)
	runner.Timeout = time.Second
	runner.MaxRetries = 1
	runner.nextID = func() (string, error) { return "job_fail", nil }

	attempts := 0
	err := runner.Run(context.Background(), "extract", nil, func(_ context.Context, _ *Job) error {
		attempts++
		return errors.New("boom")
	})
	if err == nil {
		t.Fatal("Run() expected error")
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if store.jobs["job_fail"].Status != "failed" {
		t.Fatalf("final status = %q, want failed", store.jobs["job_fail"].Status)
	}
}

// busyStore fails every write the way SQLite does while another process holds
// the write lock.
type busyStore struct{ calls int }

func (b *busyStore) Upsert(context.Context, domain.CompilationJob) error {
	b.calls++
	return errors.New("upsert compilation job: database is locked (5) (SQLITE_BUSY)")
}

// The job record is bookkeeping. A long consolidation held the write lock for
// hours and every command failed on "create job" before doing its work — even
// commands that only read. Failing to record the job must not fail the job.
func TestRunnerRunsTheJobWhenTheRecordCannotBeWritten(t *testing.T) {
	store := &busyStore{}
	runner := NewRunner(store)
	runner.Timeout = time.Second
	runner.MaxRetries = 0

	ran := false
	err := runner.Run(context.Background(), "process", nil, func(_ context.Context, job *Job) error {
		ran = true
		return job.SetStatus("loading", "")
	})
	if err != nil {
		t.Fatalf("Run() error = %v, want the job to run without its record", err)
	}
	if !ran {
		t.Fatal("job body never ran")
	}
	// Each write waits out busy_timeout; once one fails the rest are skipped.
	if store.calls != 1 {
		t.Fatalf("store written %d times after the first write failed, want 1", store.calls)
	}
}

// A read-only job (query, quality, metrics) records nothing, so a read never
// needs the write lock. Status transitions are still validated.
func TestReadOnlyRunnerNeverWritesItsRecord(t *testing.T) {
	store := &busyStore{}
	runner := NewRunner(store)
	runner.Timeout = time.Second
	runner.MaxRetries = 0
	runner.ReadOnly = true

	err := runner.Run(context.Background(), "query", nil, func(_ context.Context, job *Job) error {
		if err := job.SetStatus("loading", ""); err != nil {
			return err
		}
		return job.SetStatus("querying", "")
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if store.calls != 0 {
		t.Fatalf("read-only job wrote its record %d times, want 0", store.calls)
	}

	err = runner.Run(context.Background(), "query", nil, func(_ context.Context, job *Job) error {
		return job.SetStatus("no-such-state", "")
	})
	if err == nil {
		t.Fatal("read-only job accepted an undeclared status")
	}
}
