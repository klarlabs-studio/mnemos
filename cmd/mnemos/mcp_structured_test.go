package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/bolt"
	"go.klarlabs.de/mcp/middleware"
	"go.klarlabs.de/mcp/protocol"
	"go.klarlabs.de/mcp/schema"
	"go.klarlabs.de/mnemos/internal/domain"
)

// #503: query_knowledge's structured content failed its own output schema in
// Claude Code ("ConfidenceComponents must be object"), so the client dropped
// every belief. The schema generator types a Go map as "object" and a slice as
// "array", while encoding/json writes a nil one as null. These tests check the
// served response the way a strict client does: null matches no typed schema.

// servedToolCall runs a tools/call through the served middleware stack, with a
// handler that answers the way the mcp library does for a typed result: the
// JSON of the value, promoted to structuredContent.
func servedToolCall(t *testing.T, result any) map[string]any {
	t.Helper()
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var structured map[string]any
	if err := json.Unmarshal(data, &structured); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	handler := middleware.HandlerFunc(func(context.Context, *protocol.Request) (*protocol.Response, error) {
		return &protocol.Response{JSONRPC: "2.0", Result: map[string]any{
			"content":           []map[string]any{{"type": "text", "text": string(data)}},
			"structuredContent": structured,
		}}, nil
	})
	stack := mcpMiddlewareStack(bolt.New(bolt.NewJSONHandler(io.Discard)))
	for i := len(stack) - 1; i >= 0; i-- {
		handler = stack[i](handler)
	}
	params, _ := json.Marshal(toolCallParams{Name: "query_knowledge"})
	resp, err := handler(context.Background(), &protocol.Request{JSONRPC: "2.0", Method: protocol.MethodToolsCall, Params: params})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	// Read the response back off the wire, as a client would.
	wire, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(wire, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return out
}

func TestMCPStructuredContent_MatchesOutputSchema(t *testing.T) {
	cases := map[string]any{
		// The #503 shape: beliefs without a confidence decomposition.
		"query_knowledge": newRecallOutput(domain.Answer{
			Claims: []domain.Claim{{ID: "cl_1", Text: "the queue drains hourly", Type: domain.ClaimTypeFact,
				Status: domain.ClaimStatusActive, Confidence: 0.8, CreatedAt: time.Unix(0, 0).UTC()}},
		}),
		// An empty answer: every slice and map nil.
		"query_knowledge, empty": recallOutput{},
		"list_beliefs, empty":    mcpListClaimsOutput{},
	}
	for name, result := range cases {
		t.Run(name, func(t *testing.T) {
			s, err := schema.Generate(result)
			if err != nil {
				t.Fatalf("generate schema: %v", err)
			}
			raw, _ := json.Marshal(s)
			var advertised map[string]any
			if err := json.Unmarshal(raw, &advertised); err != nil {
				t.Fatalf("read schema: %v", err)
			}
			got := servedToolCall(t, result)
			sc, ok := got["structuredContent"]
			if !ok {
				t.Fatal("response has no structuredContent")
			}
			if errs := strictValidate("data", sc, advertised); len(errs) > 0 {
				t.Errorf("structured content does not match the output schema:\n  %s", strings.Join(errs, "\n  "))
			}
		})
	}
}

// Dropping a null member must not drop anything else: a zero, an empty string
// and an empty list are values, not absences.
func TestMCPStructuredContent_KeepsZeroValues(t *testing.T) {
	got := servedToolCall(t, map[string]any{
		"n": 0, "s": "", "empty": []any{}, "obj": map[string]any{"gone": nil, "kept": false},
		"list": []any{map[string]any{"gone": nil, "x": 1}},
		"gone": nil,
	})
	sc, _ := json.Marshal(got["structuredContent"])
	if want := `{"empty":[],"list":[{"x":1}],"n":0,"obj":{"kept":false},"s":""}`; string(sc) != want {
		t.Errorf("structuredContent = %s, want %s", sc, want)
	}
}

// strictValidate checks v against the subset of JSON Schema the mcp generator
// emits (type, properties, required, items, additionalProperties false), the
// way a strict client reads it: null matches no declared type.
func strictValidate(path string, v any, s map[string]any) []string {
	typ, _ := s["type"].(string)
	if typ == "" {
		return nil
	}
	if !jsonTypeIs(v, typ) {
		return []string{fmt.Sprintf("%s must be %s, got %s", path, typ, jsonTypeOf(v))}
	}
	var errs []string
	switch typ {
	case "object":
		obj := v.(map[string]any)
		props, _ := s["properties"].(map[string]any)
		if req, ok := s["required"].([]any); ok {
			for _, r := range req {
				if _, present := obj[r.(string)]; !present {
					errs = append(errs, fmt.Sprintf("%s/%s is required", path, r))
				}
			}
		}
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			ps, declared := props[k].(map[string]any)
			if !declared {
				if closed, ok := s["additionalProperties"].(bool); ok && !closed {
					errs = append(errs, fmt.Sprintf("%s/%s is not declared", path, k))
				}
				continue
			}
			errs = append(errs, strictValidate(path+"/"+k, obj[k], ps)...)
		}
	case "array":
		items, _ := s["items"].(map[string]any)
		for i, e := range v.([]any) {
			if items != nil {
				errs = append(errs, strictValidate(fmt.Sprintf("%s/%d", path, i), e, items)...)
			}
		}
	}
	return errs
}

func jsonTypeIs(v any, typ string) bool {
	switch typ {
	case "integer":
		f, ok := v.(float64)
		return ok && f == float64(int64(f))
	case "number":
		_, ok := v.(float64)
		return ok
	default:
		return jsonTypeOf(v) == typ
	}
}

func jsonTypeOf(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64:
		return "number"
	}
	return fmt.Sprintf("%T", v)
}
