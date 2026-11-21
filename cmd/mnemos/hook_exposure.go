package main

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// Recall exposures (ADR 0048).
//
// The recall hook injects beliefs into a session's context. When that session
// later says one of them back, its captured episode is the brain's own belief
// returning, not an independent report, and must not count as corroboration.
// Nothing stored linked a recall to a session — reconsolidation bumps
// last_verified, which says that a belief was recalled, never by whom — so the
// hook records it: one row per (session, belief) it showed.
//
// The write runs in a detached worker after the context has been emitted. A
// recall hook is read-only and answers while another process holds the write
// lock; a synchronous write here would make every prompt wait on that lock.

// recallExposureHook is the hook sub-command the detached worker runs.
const recallExposureHook = "record-exposures"

// exposureWorkerTimeout bounds the worker's write. It runs detached, so the
// bound protects the machine from piled-up workers, not the prompt.
const exposureWorkerTimeout = 30 * time.Second

// recallExposurePayload is what the recall hook hands its worker: the shown
// beliefs per tier ("global", "workspace") and the instant they were shown.
type recallExposurePayload struct {
	SessionID string              `json:"session_id"`
	Cwd       string              `json:"cwd"`
	ShownAt   time.Time           `json:"shown_at"`
	Shown     map[string][]string `json:"shown"`
}

// shownExposures returns the beliefs renderRecall actually showed, by tier.
// Only the rendered lines count: a belief ranked below the cut was never in the
// session's context, so it cannot have seeded anything.
func shownExposures(claims []recallClaim) map[string][]string {
	out := map[string][]string{}
	for i, c := range claims {
		if i >= maxRecallLines {
			break
		}
		if c.ID == "" {
			continue
		}
		tier := c.Source
		if tier == "" {
			tier = "global"
		}
		out[tier] = append(out[tier], c.ID)
	}
	return out
}

// spawnRecallExposures hands the shown beliefs to a detached worker. It is a
// no-op without a session id (nothing to key the exposure on) and on a hosted
// brain (the REST search API has no session field; ADR 0048 lists it as not
// addressed).
func spawnRecallExposures(ev hookEvent, claims []recallClaim, at time.Time) {
	if strings.TrimSpace(ev.SessionID) == "" || hostedConfigured() {
		return
	}
	shown := shownExposures(claims)
	if len(shown) == 0 {
		return
	}
	payload, err := json.Marshal(recallExposurePayload{SessionID: ev.SessionID, Cwd: ev.Cwd, ShownAt: at.UTC(), Shown: shown})
	if err != nil {
		return
	}
	_ = spawnDetachedCapture(recallExposureHook, payload)
}

// hookRecordExposures is the worker: it records the exposures it was handed in
// the brain each tier came from, through the governed writer. Fail-open.
func hookRecordExposures(raw []byte) {
	var p recallExposurePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), exposureWorkerTimeout)
	defer cancel()
	recordRecallExposures(ctx, p)
}

// recordRecallExposures writes p's exposures: the global tier to the pinned
// brain, the workspace tier to the session's repo brain.
func recordRecallExposures(ctx context.Context, p recallExposurePayload) {
	write := func(ids []string) {
		if len(ids) == 0 {
			return
		}
		exposures := make([]domain.RecallExposure, 0, len(ids))
		for _, id := range ids {
			exposures = append(exposures, domain.RecallExposure{SessionID: p.SessionID, ClaimID: id, ExposedAt: p.ShownAt})
		}
		w, err := openWriter(ctx)
		if err != nil {
			log.Printf("record recall exposures: %v", err)
			return
		}
		defer closeWriter(w)
		if _, err := w.RecallExposures(ctx, exposures); err != nil {
			log.Printf("record recall exposures: %v", err)
		}
	}
	write(p.Shown["global"])
	if ids := p.Shown["workspace"]; len(ids) > 0 {
		if dsn, _ := repoBrain(p.Cwd); dsn != "" {
			withBrainDSN(dsn, func() { write(ids) })
		}
	}
}
