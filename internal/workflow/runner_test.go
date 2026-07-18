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
