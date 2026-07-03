package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

// OpenAIEmbedder calls any OpenAI-compatible embeddings API.
// Works with OpenAI, Ollama (/api/embed), and any compatible endpoint.
type OpenAIEmbedder struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

// NewOpenAIEmbedder creates an embedding client for OpenAI-compatible APIs.
func NewOpenAIEmbedder(baseURL, apiKey, model string) *OpenAIEmbedder {
	return &OpenAIEmbedder{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		http:    defaultEmbeddingHTTPClient(),
	}
}

type openAIEmbedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type openAIEmbedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Model string `json:"model"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// Ollama uses a different embedding response format.
type ollamaEmbedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

// ModelID satisfies [ModelIdentifier], reporting the configured model so
// stored vectors are stamped and recall filters to this model space.
func (c *OpenAIEmbedder) ModelID() string { return c.model }

// Embed generates embeddings for the given texts using the OpenAI embeddings API.
func (c *OpenAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	isOllama := strings.Contains(c.baseURL, ":11434")

	reqBody := openAIEmbedRequest{
		Model: c.model,
		Input: texts,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal embedding request: %w", err)
	}

	endpoint := c.baseURL + "/v1/embeddings"
	if isOllama {
		endpoint = c.baseURL + "/api/embed"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create embedding request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding request failed: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("close body: %v", err)
		}
	}()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read embedding response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embedding API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	if isOllama {
		var result ollamaEmbedResponse
		if err := json.Unmarshal(respBody, &result); err != nil {
			return nil, fmt.Errorf("unmarshal ollama embedding response: %w", err)
		}
		return result.Embeddings, nil
	}

	var result openAIEmbedResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("unmarshal embedding response: %w", err)
	}
	if result.Error != nil {
		return nil, fmt.Errorf("embedding API error: %s: %s", result.Error.Type, result.Error.Message)
	}

	vectors := make([][]float32, len(result.Data))
	for _, d := range result.Data {
		if d.Index < len(vectors) {
			vectors[d.Index] = d.Embedding
		}
	}
	return vectors, nil
}
