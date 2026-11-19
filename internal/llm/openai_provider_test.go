package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIClientErrorsTaggedWithProvider(t *testing.T) {
	// Server returns invalid JSON to trigger the unmarshal error path.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not-valid-json"))
	}))
	defer server.Close()

	cases := []struct {
		provider string
		want     string
	}{
		{"ollama", "ollama"},
		{"openai", "openai"},
		{"openai-compat", "openai-compat"},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			c := NewOpenAIClient(server.URL, "", "test-model", tc.provider)
			_, err := c.Complete(context.Background(), []Message{{Role: RoleUser, Content: "hi"}})
			if err == nil {
				t.Fatal("expected error from invalid JSON response")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain provider %q", err.Error(), tc.want)
			}
		})
	}
}

func TestOpenAIClientToleratesTrailingFrame(t *testing.T) {
	// Some Ollama versions emit a final {"done":true} frame after the
	// main response even when stream=false. The Decoder-based parser
	// should consume the first object and ignore the rest.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hello"}}],"model":"test"}
{"done":true}`))
	}))
	defer server.Close()

	c := NewOpenAIClient(server.URL, "", "test-model", "ollama")
	resp, err := c.Complete(context.Background(), []Message{{Role: RoleUser, Content: "hi"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "hello" {
		t.Fatalf("got content %q, want %q", resp.Content, "hello")
	}
}

func TestOpenAIClientSendsStreamFalse(t *testing.T) {
	gotStream := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		gotStream = string(buf[:n])
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	c := NewOpenAIClient(server.URL, "", "m", "ollama")
	_, err := c.Complete(context.Background(), []Message{{Role: RoleUser, Content: "hi"}})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.Contains(gotStream, `"stream":false`) {
		t.Fatalf("request body should include stream:false, got: %s", gotStream)
	}
}

// Sampling parameters are opt-in: a default client sends neither, so every
// existing caller keeps the provider's defaults; WithTemperature / WithSeed
// send them, which is what a reproducible experiment needs.
func TestOpenAIClientSamplingIsOptIn(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	msgs := []Message{{Role: RoleUser, Content: "hi"}}
	base := NewOpenAIClient(server.URL, "", "m", "ollama")
	if _, err := base.Complete(context.Background(), msgs); err != nil {
		t.Fatal(err)
	}
	tuned := base.WithTemperature(0).WithSeed(7)
	if _, err := tuned.Complete(context.Background(), msgs); err != nil {
		t.Fatal(err)
	}
	if _, err := base.Complete(context.Background(), msgs); err != nil {
		t.Fatal(err)
	}

	for _, i := range []int{0, 2} {
		if _, ok := bodies[i]["temperature"]; ok {
			t.Errorf("request %d: default client sent temperature", i)
		}
		if _, ok := bodies[i]["seed"]; ok {
			t.Errorf("request %d: default client sent seed", i)
		}
	}
	if got, ok := bodies[1]["temperature"]; !ok || got != float64(0) {
		t.Errorf("temperature = %v (present %v), want 0", got, ok)
	}
	if got := bodies[1]["seed"]; got != float64(7) {
		t.Errorf("seed = %v, want 7", got)
	}
}
