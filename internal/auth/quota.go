package auth

import (
	"errors"
	"sync"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// ErrQuotaExceeded is returned when an agent's rolling-window quota
// would be breached by a write.
var ErrQuotaExceeded = errors.New("agent quota exceeded")

// QuotaTracker enforces per-agent rolling-window quotas in memory.
// Counters reset on process restart — operators wanting durable
// enforcement persist them through a future repository (the in-memory
// approach is "good enough" for v0 quota policies and matches how
// other rate-limit primitives in the codebase behave).
//
// The tracker is safe for concurrent use after construction.
type QuotaTracker struct {
	mu      sync.Mutex
	windows map[string]*rollingWindow
}

type rollingWindow struct {
	start  time.Time
	writes int64
	tokens int64
}

// NewQuotaTracker constructs an empty tracker.
func NewQuotaTracker() *QuotaTracker {
	return &QuotaTracker{windows: map[string]*rollingWindow{}}
}

// Charge debits one write (and tokens) against the agent's quota,
// rolling the window forward when it has expired. Returns
// [ErrQuotaExceeded] when the resulting counters would exceed the
// configured limits; otherwise nil.
//
// Zero-value quota fields mean "no limit" for that dimension, which
// is the steady-state for agents created before quotas were a thing.
func (q *QuotaTracker) Charge(agentID string, quota domain.AgentQuota, tokens int64) error {
	if quota.IsZero() {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	now := time.Now().UTC()
	w, ok := q.windows[agentID]
	if !ok || (quota.WindowSeconds > 0 && now.Sub(w.start) >= time.Duration(quota.WindowSeconds)*time.Second) {
		w = &rollingWindow{start: now}
		q.windows[agentID] = w
	}
	if quota.MaxWrites > 0 && w.writes+1 > quota.MaxWrites {
		return ErrQuotaExceeded
	}
	if quota.MaxTokens > 0 && tokens > 0 && w.tokens+tokens > quota.MaxTokens {
		return ErrQuotaExceeded
	}
	w.writes++
	w.tokens += tokens
	return nil
}

// Snapshot returns the current usage for the agent. Useful for the
// `mnemos agent quota status` CLI command.
func (q *QuotaTracker) Snapshot(agentID string) domain.AgentUsage {
	q.mu.Lock()
	defer q.mu.Unlock()
	w, ok := q.windows[agentID]
	if !ok {
		return domain.AgentUsage{AgentID: agentID}
	}
	return domain.AgentUsage{
		AgentID:     agentID,
		WindowStart: w.start,
		Writes:      w.writes,
		Tokens:      w.tokens,
	}
}

// Reset zeroes the rolling counter for one agent. Called by the
// admin path so an operator can revoke a quota lockout without
// waiting for the window to roll over.
func (q *QuotaTracker) Reset(agentID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.windows, agentID)
}
