package main

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	mnemos "go.klarlabs.de/mnemos"
	"go.klarlabs.de/mnemos/internal/domain"
)

// Working memory + skill loop + temporal over HTTP (v0.69, parity batch 4) —
// the last of the cognitive layer. Reads are GET (no token); writes are POST
// (token). All delegate to the Memory facade.

// --- working memory: /v1/blocks ----------------------------------------------

type blockDTO struct {
	Owner     string `json:"owner"`
	Label     string `json:"label"`
	Value     string `json:"value"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

func makeBlocksHandler(mem mnemos.Memory) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if memUnavailable(w, mem) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			owner := r.URL.Query().Get("owner")
			if owner == "" {
				writeError(w, http.StatusBadRequest, "owner is required")
				return
			}
			blocks, err := mem.Blocks(r.Context(), owner)
			if err != nil {
				writeInternalError(w, "blocks", err)
				return
			}
			out := make([]blockDTO, 0, len(blocks))
			for _, b := range blocks {
				dto := blockDTO{Owner: b.Owner, Label: b.Label, Value: b.Value}
				if !b.UpdatedAt.IsZero() {
					dto.UpdatedAt = b.UpdatedAt.UTC().Format(time.RFC3339)
				}
				out = append(out, dto)
			}
			writeJSON(w, http.StatusOK, map[string]any{"owner": owner, "blocks": out})
		case http.MethodPost:
			if !requireScope(w, r, domain.ScopeClaimsWrite) {
				return
			}
			var req struct {
				Owner  string `json:"owner"`
				Label  string `json:"label"`
				Value  string `json:"value"`
				Append bool   `json:"append"`
			}
			if err := decodeJSON(r, &req); err != nil {
				writeError(w, http.StatusBadRequest, "decode body: "+err.Error())
				return
			}
			if req.Owner == "" || req.Label == "" {
				writeError(w, http.StatusBadRequest, "owner and label are required")
				return
			}
			var err error
			if req.Append {
				err = mem.AppendBlock(r.Context(), req.Owner, req.Label, req.Value)
			} else {
				err = mem.SetBlock(r.Context(), req.Owner, req.Label, req.Value)
			}
			if err != nil {
				writeInternalError(w, "set block", err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"owner": req.Owner, "label": req.Label})
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

// --- skill loop: /v1/actions + /v1/actions/{id}/outcome ----------------------

func makeActionsHandler(mem mnemos.Memory) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if memUnavailable(w, mem) || !requireScope(w, r, domain.ScopeClaimsWrite) {
			return
		}
		var req struct {
			ID       string            `json:"id"`
			Kind     string            `json:"kind"`
			Subject  string            `json:"subject"`
			Actor    string            `json:"actor"`
			Metadata map[string]string `json:"metadata"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "decode body: "+err.Error())
			return
		}
		if req.Kind == "" {
			writeError(w, http.StatusBadRequest, "kind is required")
			return
		}
		id, err := mem.RecordAction(r.Context(), mnemos.ActionItem{
			ID: req.ID, Kind: req.Kind, Subject: req.Subject, Actor: req.Actor, Metadata: req.Metadata,
		})
		if err != nil {
			writeInternalError(w, "record action", err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"id": id})
	}
}

func makeActionOutcomeHandler(mem mnemos.Memory, actionID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if memUnavailable(w, mem) || !requireScope(w, r, domain.ScopeClaimsWrite) {
			return
		}
		var req struct {
			Result  string             `json:"result"`
			Metrics map[string]float64 `json:"metrics"`
			Notes   string             `json:"notes"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "decode body: "+err.Error())
			return
		}
		if err := mem.RecordActionOutcome(r.Context(), mnemos.OutcomeItem{
			ActionID: actionID, Result: req.Result, Metrics: req.Metrics, Notes: req.Notes,
		}); err != nil {
			writeInternalError(w, "record action outcome", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"action_id": actionID, "result": req.Result})
	}
}

func makeActionSubresourceHandler(mem mnemos.Memory) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tail := strings.TrimPrefix(r.URL.Path, "/v1/actions/")
		parts := strings.SplitN(tail, "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] != "outcome" {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		makeActionOutcomeHandler(mem, parts[0])(w, r)
	}
}

// --- skill loop: /v1/synthesize ----------------------------------------------

func makeSynthesizeHandler(mem mnemos.Memory) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if memUnavailable(w, mem) || !requireScope(w, r, domain.ScopeClaimsWrite) {
			return
		}
		res, err := mem.Synthesize(r.Context())
		if err != nil {
			writeInternalError(w, "synthesize", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"lessons_derived":   res.LessonsDerived,
			"playbooks_derived": res.PlaybooksDerived,
		})
	}
}

// --- temporal: /v1/timeline + /v1/signals ------------------------------------

type timelineEventDTO struct {
	ID       string            `json:"id"`
	At       string            `json:"at,omitempty"`
	Type     string            `json:"type"`
	Content  string            `json:"content"`
	RunID    string            `json:"run_id,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

func makeTimelineHandler(mem mnemos.Memory) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireGET(w, r) || memUnavailable(w, mem) {
			return
		}
		q := r.URL.Query()
		tq := mnemos.TimelineQuery{RunID: q.Get("run_id"), Limit: parseLimitQuery(r, 50)}
		if v := q.Get("from"); v != "" {
			if t, err := parseTimeFlexible(v); err == nil {
				tq.From = t
			}
		}
		if v := q.Get("to"); v != "" {
			if t, err := parseTimeFlexible(v); err == nil {
				tq.To = t
			}
		}
		if v := q.Get("types"); v != "" {
			tq.Types = strings.Split(v, ",")
		}
		events, err := mem.Timeline(r.Context(), tq)
		if err != nil {
			writeInternalError(w, "timeline", err)
			return
		}
		out := make([]timelineEventDTO, 0, len(events))
		for _, e := range events {
			dto := timelineEventDTO{ID: e.ID, Type: e.Type, Content: e.Content, RunID: e.RunID, Metadata: e.Metadata}
			if !e.At.IsZero() {
				dto.At = e.At.UTC().Format(time.RFC3339)
			}
			out = append(out, dto)
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": out})
	}
}

type signalDTO struct {
	Pattern     string  `json:"pattern"`
	RunID       string  `json:"run_id,omitempty"`
	Strength    float64 `json:"strength"`
	Confidence  float64 `json:"confidence"`
	Class       string  `json:"class,omitempty"`
	DetectedAt  string  `json:"detected_at,omitempty"`
	WindowStart string  `json:"window_start,omitempty"`
	WindowEnd   string  `json:"window_end,omitempty"`
}

func makeSignalsHandler(mem mnemos.Memory) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireGET(w, r) || memUnavailable(w, mem) {
			return
		}
		q := r.URL.Query()
		sq := mnemos.SignalQuery{RunID: q.Get("run_id"), Limit: parseLimitQuery(r, 50)}
		if f, err := strconv.ParseFloat(q.Get("min_confidence"), 64); err == nil {
			sq.MinConfidence = f
		}
		signals, err := mem.Signals(r.Context(), sq)
		if err != nil {
			writeInternalError(w, "signals", err)
			return
		}
		out := make([]signalDTO, 0, len(signals))
		for _, s := range signals {
			dto := signalDTO{Pattern: s.Pattern, RunID: s.RunID, Strength: s.Strength, Confidence: s.Confidence, Class: s.Class}
			if !s.DetectedAt.IsZero() {
				dto.DetectedAt = s.DetectedAt.UTC().Format(time.RFC3339)
			}
			if !s.WindowStart.IsZero() {
				dto.WindowStart = s.WindowStart.UTC().Format(time.RFC3339)
			}
			if !s.WindowEnd.IsZero() {
				dto.WindowEnd = s.WindowEnd.UTC().Format(time.RFC3339)
			}
			out = append(out, dto)
		}
		writeJSON(w, http.StatusOK, map[string]any{"signals": out})
	}
}
