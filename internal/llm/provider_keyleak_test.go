package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const providerKeyCanary = "sk-LEAKCANARY0000000000000000000000000000"

// A provider (or a proxy in front of it) that answers an error by echoing the
// request back — headers included — must not turn the API key into error text.
// That text reaches server logs and MCP replies, i.e. agent transcripts.
func echoingErrorServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid key: `+r.Header.Get("Authorization")+r.Header.Get("x-api-key")+r.Header.Get("x-goog-api-key")+" "+r.URL.RawQuery+`"}}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestProviderClients_ErrorsDoNotLeakTheAPIKey(t *testing.T) {
	echo := echoingErrorServer(t)
	clients := map[string]func(base string) Client{
		"openai":    func(base string) Client { return NewOpenAIClient(base, providerKeyCanary, "gpt-test", "openai") },
		"anthropic": func(base string) Client { return NewAnthropicClient(base, providerKeyCanary, "claude-test") },
		"gemini":    func(base string) Client { return NewGeminiClient(base, providerKeyCanary, "gemini-test") },
	}
	for name, mk := range clients {
		for _, base := range []string{"http://127.0.0.1:1", echo.URL} {
			_, err := mk(base).Complete(context.Background(), []Message{{Role: RoleUser, Content: "hello"}})
			if err == nil {
				t.Fatalf("%s @ %s: expected an error", name, base)
			}
			if strings.Contains(err.Error(), providerKeyCanary) {
				t.Errorf("%s @ %s: API key leaked into the error: %v", name, base, err)
			}
		}
	}
}
