package embedding

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const embedKeyCanary = "sk-EMBEDLEAKCANARY000000000000000000000000"

// An embedding provider (or a proxy) that answers an error by echoing the
// request — headers and query, where Gemini carries its key — must not turn the
// API key into error text.
func TestEmbedders_ErrorsDoNotLeakTheAPIKey(t *testing.T) {
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"bad key `+r.Header.Get("Authorization")+r.Header.Get("x-goog-api-key")+" "+r.URL.RawQuery+`"}`)
	}))
	t.Cleanup(echo.Close)

	embedders := map[string]interface {
		Embed(context.Context, []string) ([][]float32, error)
	}{
		"openai": NewOpenAIEmbedder(echo.URL, embedKeyCanary, "text-embedding-test"),
		"gemini": NewGeminiEmbedder(echo.URL, embedKeyCanary, "embedding-test"),
	}
	for name, e := range embedders {
		_, err := e.Embed(context.Background(), []string{"hello"})
		if err == nil {
			t.Fatalf("%s: expected an error from the 401", name)
		}
		if strings.Contains(err.Error(), embedKeyCanary) {
			t.Errorf("%s: API key leaked into the error: %v", name, err)
		}
	}
}
