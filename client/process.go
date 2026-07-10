package client

import (
	"context"
	"net/http"
)

// ProcessRequest is the body for POST /v1/process — raw text to run through the
// server-side ingest→extract→relate pipeline. UseLLM/UseEmbeddings are tri-state:
// leave them nil to let the server decide (LLM extraction iff it has a provider
// configured), or set them explicitly to force. A hosted-brain hook leaves them
// nil since it can't know the server's LLM config.
type ProcessRequest struct {
	Text          string `json:"text"`
	UseLLM        *bool  `json:"use_llm,omitempty"`
	UseEmbeddings *bool  `json:"use_embeddings,omitempty"`
}

// ProcessResponse reports what the pipeline persisted.
type ProcessResponse struct {
	RunID          string `json:"run_id"`
	Events         int    `json:"events"`
	Claims         int    `json:"claims"`
	Relationships  int    `json:"relationships"`
	Embeddings     int    `json:"embeddings"`
	UsedLLM        bool   `json:"used_llm"`
	UsedEmbeddings bool   `json:"used_embeddings"`
}

// Process hits POST /v1/process, running the full ingest→extract→relate pipeline
// on raw text server-side. Requires a token with claims:write. This is the
// hosted-brain path for the capture hook: it POSTs a session transcript and the
// server extracts and persists claims.
func (c *Client) Process(ctx context.Context, req ProcessRequest) (*ProcessResponse, error) {
	var out ProcessResponse
	if err := c.do(ctx, http.MethodPost, "/v1/process", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
