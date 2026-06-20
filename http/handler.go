// Package http is the optional HTTP delivery adapter for a mnemos
// [mnemos.Store]. It exposes the store's core operations (remember,
// remember-claim, remember-event, recall, get, scan, timeline) over a
// small JSON/REST surface so non-Go callers and service-to-service
// consumers can reach the same governed memory the embeddable library
// offers.
//
// As the spec states, this delivery adapter is NOT part of the stable API
// — it is a packaging convenience over the stable [mnemos.Store] port.
// Every write still routes through the store's governed axi kernel, so
// the no-bypass guarantee holds.
//
//	store, _ := mnemos.New(mnemos.WithSQLite("./mnemos.db"))
//	handler := http.NewHandler(store)
//	http.ListenAndServe(ctx, ":8080", handler)
package http

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"time"

	"go.klarlabs.de/mnemos"
)

// Read/write timeouts applied by [ListenAndServe]. Conservative defaults
// suitable for a local service-to-service memory endpoint.
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 30 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 120 * time.Second
)

// NewHandler returns an [http.Handler] that serves the core operations of
// store over JSON. Routes:
//
//	POST /v1/remember        body: {type, content, source, run_id, metadata}
//	POST /v1/claims          body: {text, type, confidence, event_ids, valid_from, valid_until, run_id}
//	POST /v1/events          body: {id, at, type, content, run_id, metadata}
//	POST /v1/recall          body: {text, run_id, hops, limit, as_of, recorded_as_of, include_history}
//	GET  /v1/claims/{id}      exact lookup
//	POST /v1/scan            body: {valid_from, valid_until, limit}
//	POST /v1/timeline        body: {from, to, types, run_id, limit}
//
// The handler holds the store for the lifetime of the process; the caller
// owns the store and is responsible for closing it.
func NewHandler(store mnemos.Store) http.Handler {
	h := &handler{store: store}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/remember", h.remember)
	mux.HandleFunc("POST /v1/claims", h.rememberClaim)
	mux.HandleFunc("POST /v1/events", h.rememberEvent)
	mux.HandleFunc("POST /v1/recall", h.recall)
	mux.HandleFunc("GET /v1/claims/{id}", h.get)
	mux.HandleFunc("POST /v1/scan", h.scan)
	mux.HandleFunc("POST /v1/timeline", h.timeline)
	return mux
}

// ListenAndServe starts an HTTP server bound to addr serving handler,
// shutting it down gracefully when ctx is cancelled. Returns nil on a
// clean shutdown; the underlying server error otherwise.
func ListenAndServe(ctx context.Context, addr string, handler http.Handler) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	// Bind explicitly so addr ":0"/"127.0.0.1:0" works and tests can use
	// an ephemeral port deterministically.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

type handler struct {
	store mnemos.Store
}

func (h *handler) remember(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Type     string            `json:"type"`
		Content  string            `json:"content"`
		Source   string            `json:"source"`
		RunID    string            `json:"run_id"`
		Metadata map[string]string `json:"metadata"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := h.store.Remember(r.Context(), mnemos.Item{
		Type:     in.Type,
		Content:  in.Content,
		Source:   in.Source,
		RunID:    in.RunID,
		Metadata: in.Metadata,
	}); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "remembered"})
}

func (h *handler) rememberClaim(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text       string    `json:"text"`
		Type       string    `json:"type"`
		Confidence float64   `json:"confidence"`
		EventIDs   []string  `json:"event_ids"`
		ValidFrom  time.Time `json:"valid_from"`
		ValidUntil time.Time `json:"valid_until"`
		RunID      string    `json:"run_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	id, err := h.store.RememberClaim(r.Context(), mnemos.ClaimItem{
		Text:       in.Text,
		Type:       in.Type,
		Confidence: in.Confidence,
		EventIDs:   in.EventIDs,
		ValidFrom:  in.ValidFrom,
		ValidUntil: in.ValidUntil,
		RunID:      in.RunID,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"claim_id": id})
}

func (h *handler) rememberEvent(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID       string            `json:"id"`
		At       time.Time         `json:"at"`
		Type     string            `json:"type"`
		Content  string            `json:"content"`
		RunID    string            `json:"run_id"`
		Metadata map[string]string `json:"metadata"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := h.store.RememberEvent(r.Context(), mnemos.Event{
		ID:       in.ID,
		At:       in.At,
		Type:     in.Type,
		Content:  in.Content,
		RunID:    in.RunID,
		Metadata: in.Metadata,
	}); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "remembered"})
}

func (h *handler) recall(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text           string    `json:"text"`
		RunID          string    `json:"run_id"`
		Hops           int       `json:"hops"`
		Limit          int       `json:"limit"`
		AsOf           time.Time `json:"as_of"`
		RecordedAsOf   time.Time `json:"recorded_as_of"`
		IncludeHistory bool      `json:"include_history"`
	}
	if !decode(w, r, &in) {
		return
	}
	results, err := h.store.Recall(r.Context(), mnemos.Query{
		Text:           in.Text,
		RunID:          in.RunID,
		Hops:           in.Hops,
		Limit:          in.Limit,
		AsOf:           in.AsOf,
		RecordedAsOf:   in.RecordedAsOf,
		IncludeHistory: in.IncludeHistory,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	claim, err := h.store.Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, claim)
}

func (h *handler) scan(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ValidFrom  time.Time `json:"valid_from"`
		ValidUntil time.Time `json:"valid_until"`
		Limit      int       `json:"limit"`
	}
	if !decode(w, r, &in) {
		return
	}
	claims, err := h.store.Scan(r.Context(), mnemos.ScanQuery{
		ValidFrom:  in.ValidFrom,
		ValidUntil: in.ValidUntil,
		Limit:      in.Limit,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"claims": claims})
}

func (h *handler) timeline(w http.ResponseWriter, r *http.Request) {
	var in struct {
		From  time.Time `json:"from"`
		To    time.Time `json:"to"`
		Types []string  `json:"types"`
		RunID string    `json:"run_id"`
		Limit int       `json:"limit"`
	}
	if !decode(w, r, &in) {
		return
	}
	events, err := h.store.Timeline(r.Context(), mnemos.TimelineQuery{
		From:  in.From,
		To:    in.To,
		Types: in.Types,
		RunID: in.RunID,
		Limit: in.Limit,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

// decode reads a JSON body into v, writing a 400 and returning false on
// failure.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Body == nil {
		writeError(w, http.StatusBadRequest, errors.New("empty request body"))
		return false
	}
	defer func() { _ = r.Body.Close() }()
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
