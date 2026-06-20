// Package mcp is the optional MCP delivery adapter for a mnemos
// [mnemos.Store]. It exposes the store's core operations as MCP tools so
// agents can read and write evidence-backed memory over the Model Context
// Protocol.
//
// As the spec states, this delivery adapter is NOT part of the stable API
// — it is a packaging convenience over the stable [mnemos.Store] port.
// Every write still routes through the store's governed axi kernel, so
// the no-bypass guarantee holds.
//
//	store, _ := mnemos.New(mnemos.WithSQLite("./mnemos.db"))
//	srv := mcp.NewServer(store)
//	mcp.ServeStdio(ctx, srv)
package mcp

import (
	"context"
	"time"

	upstream "go.klarlabs.de/mcp"
	"go.klarlabs.de/mnemos"
)

// Server is the mnemos MCP server. It wraps the upstream MCP server and
// the backing [mnemos.Store].
type Server = upstream.Server

// NewServer builds an MCP [Server] whose tools are backed by store. The
// registered tools are:
//
//	remember        — ingest text, extract and persist claims
//	remember_claim  — persist a pre-built claim (requires evidence)
//	remember_event  — append a temporal event
//	recall          — answer a query (supports bitemporal as_of/recorded_as_of)
//	get             — exact claim lookup by id
//	scan            — valid-time range query
//
// The caller owns store and is responsible for closing it.
func NewServer(store mnemos.Store) *Server {
	srv := upstream.NewServer(upstream.ServerInfo{
		Name: "mnemos",
		Capabilities: upstream.Capabilities{
			Tools: true,
		},
	},
		upstream.WithTitle("Mnemos MCP Server"),
		upstream.WithDescription("Read and write evidence-backed, bitemporal agent memory governed by axi-go."),
		upstream.WithInstructions("Use recall to read memory and remember/remember_claim/remember_event to write it. Claims require evidence: link remember_claim to event ids you persisted with remember_event."),
	)
	registerTools(srv, store)
	return srv
}

// ServeStdio serves srv over stdio until ctx is cancelled or the stream
// closes. It is the spec's stdio entry point for an MCP server.
func ServeStdio(ctx context.Context, srv *Server) error {
	return upstream.ServeStdio(ctx, srv)
}

// --- tool input/output shapes ---

type rememberInput struct {
	Type    string `json:"type" jsonschema:"description=Claim type: fact, hypothesis, or decision"`
	Content string `json:"content" jsonschema:"required,description=The text to remember"`
	Source  string `json:"source" jsonschema:"description=Optional origin label"`
	RunID   string `json:"run_id" jsonschema:"description=Optional run grouping id"`
}

type statusOutput struct {
	Status string `json:"status"`
}

type rememberClaimInput struct {
	Text       string   `json:"text" jsonschema:"required,description=The claim statement"`
	Type       string   `json:"type" jsonschema:"description=Claim type"`
	Confidence float64  `json:"confidence" jsonschema:"description=Confidence in [0,1]"`
	EventIDs   []string `json:"event_ids" jsonschema:"required,description=Source event ids backing the claim (claims require evidence)"`
	RunID      string   `json:"run_id" jsonschema:"description=Optional run grouping id"`
}

type claimIDOutput struct {
	ClaimID string `json:"claim_id"`
}

type rememberEventInput struct {
	ID      string `json:"id" jsonschema:"description=Optional stable id; generated when empty"`
	At      string `json:"at" jsonschema:"required,description=RFC3339 wall-clock time of the event"`
	Type    string `json:"type" jsonschema:"description=Event type, e.g. deployment, incident"`
	Content string `json:"content" jsonschema:"required,description=Event description"`
	RunID   string `json:"run_id" jsonschema:"description=Optional run grouping id"`
}

type eventIDOutput struct {
	EventID string `json:"event_id"`
}

type recallInput struct {
	Text         string `json:"text" jsonschema:"required,description=The question or keyword"`
	RunID        string `json:"run_id" jsonschema:"description=Optional run scope"`
	Hops         int    `json:"hops" jsonschema:"description=Graph expansion hops"`
	Limit        int    `json:"limit" jsonschema:"description=Max results"`
	AsOf         string `json:"as_of" jsonschema:"description=RFC3339 valid-time instant"`
	RecordedAsOf string `json:"recorded_as_of" jsonschema:"description=RFC3339 transaction-time instant"`
}

type recallResult struct {
	ClaimID    string  `json:"claim_id"`
	Text       string  `json:"text"`
	Type       string  `json:"type"`
	TrustScore float64 `json:"trust_score"`
}

type recallOutput struct {
	Results []recallResult `json:"results"`
}

type getInput struct {
	ClaimID string `json:"claim_id" jsonschema:"required,description=The claim id to look up"`
}

type claimView struct {
	ID         string  `json:"id"`
	Statement  string  `json:"statement"`
	Type       string  `json:"type"`
	Confidence float64 `json:"confidence"`
	TrustScore float64 `json:"trust_score"`
	ValidFrom  string  `json:"valid_from"`
	ValidUntil string  `json:"valid_until,omitempty"`
	RecordedAt string  `json:"recorded_at"`
}

type scanInput struct {
	ValidFrom  string `json:"valid_from" jsonschema:"description=RFC3339 lower bound"`
	ValidUntil string `json:"valid_until" jsonschema:"description=RFC3339 upper bound"`
	Limit      int    `json:"limit" jsonschema:"description=Max results"`
}

type scanOutput struct {
	Claims []claimView `json:"claims"`
}

// registerTools wires the core store operations onto srv.
func registerTools(srv *Server, store mnemos.Store) {
	srv.Tool("remember").
		Description("Ingest text, extract claims, and persist them with evidence links.").
		OutputSchema(statusOutput{}).
		ValidateInput().
		Handler(func(ctx context.Context, in rememberInput) (statusOutput, error) {
			if err := store.Remember(ctx, mnemos.Item{
				Type:    in.Type,
				Content: in.Content,
				Source:  in.Source,
				RunID:   in.RunID,
			}); err != nil {
				return statusOutput{}, err
			}
			return statusOutput{Status: "remembered"}, nil
		})

	srv.Tool("remember_claim").
		Description("Persist a pre-built claim. Claims require evidence: supply event_ids.").
		OutputSchema(claimIDOutput{}).
		ValidateInput().
		Handler(func(ctx context.Context, in rememberClaimInput) (claimIDOutput, error) {
			id, err := store.RememberClaim(ctx, mnemos.ClaimItem{
				Text:       in.Text,
				Type:       in.Type,
				Confidence: in.Confidence,
				EventIDs:   in.EventIDs,
				RunID:      in.RunID,
			})
			if err != nil {
				return claimIDOutput{}, err
			}
			return claimIDOutput{ClaimID: id}, nil
		})

	srv.Tool("remember_event").
		Description("Append a temporal event to the timeline.").
		OutputSchema(eventIDOutput{}).
		ValidateInput().
		Handler(func(ctx context.Context, in rememberEventInput) (eventIDOutput, error) {
			at, err := parseTime(in.At)
			if err != nil {
				return eventIDOutput{}, err
			}
			ev := mnemos.Event{
				ID:      in.ID,
				At:      at,
				Type:    in.Type,
				Content: in.Content,
				RunID:   in.RunID,
			}
			if err := store.RememberEvent(ctx, ev); err != nil {
				return eventIDOutput{}, err
			}
			return eventIDOutput{EventID: ev.ID}, nil
		})

	srv.Tool("recall").
		Description("Answer a query against stored memory. Supports bitemporal as_of (valid time) and recorded_as_of (transaction time).").
		OutputSchema(recallOutput{}).
		ValidateInput().
		Handler(func(ctx context.Context, in recallInput) (recallOutput, error) {
			q := mnemos.Query{
				Text:  in.Text,
				RunID: in.RunID,
				Hops:  in.Hops,
				Limit: in.Limit,
			}
			if in.AsOf != "" {
				t, err := parseTime(in.AsOf)
				if err != nil {
					return recallOutput{}, err
				}
				q.AsOf = t
			}
			if in.RecordedAsOf != "" {
				t, err := parseTime(in.RecordedAsOf)
				if err != nil {
					return recallOutput{}, err
				}
				q.RecordedAsOf = t
			}
			results, err := store.Recall(ctx, q)
			if err != nil {
				return recallOutput{}, err
			}
			out := recallOutput{Results: make([]recallResult, len(results))}
			for i, r := range results {
				out.Results[i] = recallResult{
					ClaimID:    r.ClaimID,
					Text:       r.Text,
					Type:       r.Type,
					TrustScore: r.TrustScore,
				}
			}
			return out, nil
		})

	srv.Tool("get").
		Description("Exact claim lookup by id.").
		OutputSchema(claimView{}).
		ValidateInput().
		Handler(func(ctx context.Context, in getInput) (claimView, error) {
			c, err := store.Get(ctx, in.ClaimID)
			if err != nil {
				return claimView{}, err
			}
			return toClaimView(c), nil
		})

	srv.Tool("scan").
		Description("Return claims whose valid time overlaps the requested window.").
		OutputSchema(scanOutput{}).
		ValidateInput().
		Handler(func(ctx context.Context, in scanInput) (scanOutput, error) {
			var q mnemos.ScanQuery
			q.Limit = in.Limit
			if in.ValidFrom != "" {
				t, err := parseTime(in.ValidFrom)
				if err != nil {
					return scanOutput{}, err
				}
				q.ValidFrom = t
			}
			if in.ValidUntil != "" {
				t, err := parseTime(in.ValidUntil)
				if err != nil {
					return scanOutput{}, err
				}
				q.ValidUntil = t
			}
			claims, err := store.Scan(ctx, q)
			if err != nil {
				return scanOutput{}, err
			}
			out := scanOutput{Claims: make([]claimView, len(claims))}
			for i, c := range claims {
				out.Claims[i] = toClaimView(c)
			}
			return out, nil
		})
}

func toClaimView(c mnemos.Claim) claimView {
	v := claimView{
		ID:         c.ID,
		Statement:  c.Statement,
		Type:       c.Type,
		Confidence: c.Confidence,
		TrustScore: c.TrustScore,
		ValidFrom:  formatTime(c.ValidFrom),
		RecordedAt: formatTime(c.RecordedAt),
	}
	if c.ValidUntil != nil {
		v.ValidUntil = formatTime(*c.ValidUntil)
	}
	return v
}

func parseTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339, s)
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
