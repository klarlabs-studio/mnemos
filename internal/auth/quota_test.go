package auth

import (
	"errors"
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
)

func TestQuotaTracker_NoLimitsAllowsEverything(t *testing.T) {
	t.Parallel()
	q := NewQuotaTracker()
	for i := 0; i < 1000; i++ {
		if err := q.Charge("a1", domain.AgentQuota{}, 0); err != nil {
			t.Fatalf("charge: %v", err)
		}
	}
}

func TestQuotaTracker_EnforcesMaxWrites(t *testing.T) {
	t.Parallel()
	q := NewQuotaTracker()
	quota := domain.AgentQuota{WindowSeconds: 60, MaxWrites: 3}
	for i := 0; i < 3; i++ {
		if err := q.Charge("a1", quota, 0); err != nil {
			t.Fatalf("charge %d: %v", i, err)
		}
	}
	if err := q.Charge("a1", quota, 0); !errors.Is(err, ErrQuotaExceeded) {
		t.Errorf("err = %v, want ErrQuotaExceeded", err)
	}
}

func TestQuotaTracker_EnforcesMaxTokens(t *testing.T) {
	t.Parallel()
	q := NewQuotaTracker()
	quota := domain.AgentQuota{WindowSeconds: 60, MaxTokens: 1000}
	if err := q.Charge("a1", quota, 600); err != nil {
		t.Fatalf("charge1: %v", err)
	}
	if err := q.Charge("a1", quota, 600); !errors.Is(err, ErrQuotaExceeded) {
		t.Errorf("second charge should overflow, got %v", err)
	}
}

func TestQuotaTracker_IsolatesPerAgent(t *testing.T) {
	t.Parallel()
	q := NewQuotaTracker()
	quota := domain.AgentQuota{WindowSeconds: 60, MaxWrites: 1}
	if err := q.Charge("a1", quota, 0); err != nil {
		t.Fatalf("a1: %v", err)
	}
	if err := q.Charge("a2", quota, 0); err != nil {
		t.Errorf("a2 should have its own bucket: %v", err)
	}
}

func TestQuotaTracker_SnapshotReportsUsage(t *testing.T) {
	t.Parallel()
	q := NewQuotaTracker()
	quota := domain.AgentQuota{WindowSeconds: 60, MaxWrites: 10, MaxTokens: 1000}
	_ = q.Charge("a1", quota, 250)
	_ = q.Charge("a1", quota, 250)
	snap := q.Snapshot("a1")
	if snap.Writes != 2 || snap.Tokens != 500 {
		t.Errorf("snap = %+v", snap)
	}
}

func TestQuotaTracker_Reset(t *testing.T) {
	t.Parallel()
	q := NewQuotaTracker()
	quota := domain.AgentQuota{WindowSeconds: 60, MaxWrites: 1}
	_ = q.Charge("a1", quota, 0)
	q.Reset("a1")
	if err := q.Charge("a1", quota, 0); err != nil {
		t.Errorf("after reset: %v", err)
	}
}
