package main

import (
	"net/http"
	"strings"
	"time"

	"go.klarlabs.de/mnemos"
)

// passageDTO is one passage on GET /v1/passages.
type passageDTO struct {
	EpisodeID string            `json:"episode_id"`
	Text      string            `json:"text"`
	At        string            `json:"at,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	Score     float64           `json:"score"`
}

// makePassagesHandler serves GET /v1/passages?query=…&limit=…&run_id=…, the
// stored episode lines that best match a query ([mnemos.Memory.Passages]).
// Passages are raw episode text, so a run-scoped token must name one of its
// runs and gets that run's passages only.
func makePassagesHandler(mem mnemos.Memory) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mem := scopedMem(r.Context(), mem)
		if !requireGET(w, r) || memUnavailable(w, mem) {
			return
		}
		q := r.URL.Query()
		text := strings.TrimSpace(q.Get("query"))
		if text == "" {
			writeError(w, http.StatusBadRequest, "query is required")
			return
		}
		runID := strings.TrimSpace(q.Get("run_id"))
		if !requireRunScope(w, r, runID) {
			return
		}
		passages, err := mem.Passages(r.Context(), mnemos.PassageQuery{
			Text: text, Limit: parseLimitQuery(r, 0), RunID: runID,
		})
		if err != nil {
			writeInternalError(w, "passages", err)
			return
		}
		out := make([]passageDTO, 0, len(passages))
		for _, p := range passages {
			dto := passageDTO{EpisodeID: p.EpisodeID, Text: p.Text, Metadata: p.Metadata, Score: p.Score}
			if !p.At.IsZero() {
				dto.At = p.At.UTC().Format(time.RFC3339)
			}
			out = append(out, dto)
		}
		writeJSON(w, http.StatusOK, map[string]any{"passages": out})
	}
}
