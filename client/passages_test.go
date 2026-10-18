package client_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	client "go.klarlabs.de/mnemos/client"
)

func TestPassages_SendsTheQueryAndDecodesPassages(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/passages" {
			http.NotFound(w, r)
			return
		}
		got = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"passages": []client.Passage{
			{EpisodeID: "ev-1", Text: "Jon: Paris.\nGina: Rome.", At: "2023-05-08T13:56:00Z", Score: 2.5},
		}})
	}))
	t.Cleanup(srv.Close)

	passages, err := client.New(srv.URL).Passages(context.Background(), client.PassagesRequest{
		Query: "Paris or Rome?", Limit: 3, RunID: "run-b",
	})
	if err != nil {
		t.Fatalf("Passages: %v", err)
	}
	if got.Get("query") != "Paris or Rome?" || got.Get("limit") != "3" || got.Get("run_id") != "run-b" {
		t.Errorf("query string = %v", got)
	}
	if len(passages) != 1 || passages[0].EpisodeID != "ev-1" || passages[0].Score != 2.5 {
		t.Errorf("passages = %+v", passages)
	}
}
