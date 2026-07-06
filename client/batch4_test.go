package client_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	client "go.klarlabs.de/mnemos/client"
)

func batch4Stub(t *testing.T, seen map[string]bool) *client.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			seen[r.Method+" "+r.URL.Path] = true
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/blocks" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"blocks": []client.Block{{Owner: "grace", Label: "focus", Value: "x"}}})
		case r.URL.Path == "/v1/blocks" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/v1/actions" && r.Method == http.MethodPost:
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "act_1"})
		case strings.HasSuffix(r.URL.Path, "/outcome") && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/v1/synthesize" && r.Method == http.MethodPost:
			_ = json.NewEncoder(w).Encode(client.SynthesizeResult{LessonsDerived: 2, PlaybooksDerived: 1})
		case r.URL.Path == "/v1/timeline" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"events": []client.TimelineEvent{{ID: "e1", Type: "note", Content: "c"}}})
		case r.URL.Path == "/v1/signals" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"signals": []client.Signal{{Pattern: "spike", Confidence: 0.8}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return client.New(srv.URL)
}

func TestBatch4_Blocks(t *testing.T) {
	c := batch4Stub(t, nil)
	ctx := context.Background()
	blocks, err := c.Blocks(ctx, "grace")
	if err != nil || len(blocks) != 1 || blocks[0].Label != "focus" {
		t.Fatalf("Blocks: %v %+v", err, blocks)
	}
	if err := c.SetBlock(ctx, "grace", "focus", "y"); err != nil {
		t.Fatalf("SetBlock: %v", err)
	}
	if err := c.AppendBlock(ctx, "grace", "focus", "z"); err != nil {
		t.Fatalf("AppendBlock: %v", err)
	}
}

func TestBatch4_ActionsAndSynthesize(t *testing.T) {
	c := batch4Stub(t, nil)
	ctx := context.Background()
	id, err := c.RecordAction(ctx, "investigate", "inc1", "grace", nil)
	if err != nil || id != "act_1" {
		t.Fatalf("RecordAction: %v %q", err, id)
	}
	if err := c.RecordActionOutcome(ctx, id, "success", map[string]float64{"latency": 1.2}, "ok"); err != nil {
		t.Fatalf("RecordActionOutcome: %v", err)
	}
	res, err := c.Synthesize(ctx)
	if err != nil || res == nil || res.LessonsDerived != 2 || res.PlaybooksDerived != 1 {
		t.Fatalf("Synthesize: %v %+v", err, res)
	}
}

func TestBatch4_TimelineAndSignals(t *testing.T) {
	c := batch4Stub(t, nil)
	ctx := context.Background()
	events, err := c.Timeline(ctx, client.TimelineRequest{RunID: "r", Limit: 10})
	if err != nil || len(events) != 1 || events[0].ID != "e1" {
		t.Fatalf("Timeline: %v %+v", err, events)
	}
	signals, err := c.Signals(ctx, "r", 0.5, 10)
	if err != nil || len(signals) != 1 || signals[0].Pattern != "spike" {
		t.Fatalf("Signals: %v %+v", err, signals)
	}
}

func TestBatch4_OutcomePathUsesActionID(t *testing.T) {
	seen := map[string]bool{}
	c := batch4Stub(t, seen)
	if err := c.RecordActionOutcome(context.Background(), "act_99", "failure", nil, ""); err != nil {
		t.Fatalf("RecordActionOutcome: %v", err)
	}
	if !seen["POST /v1/actions/act_99/outcome"] {
		t.Fatalf("outcome not posted to the action id path; seen=%v", seen)
	}
}
