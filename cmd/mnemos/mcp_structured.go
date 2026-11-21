package main

import (
	"context"
	"encoding/json"
	"fmt"

	"go.klarlabs.de/mcp/middleware"
	"go.klarlabs.de/mcp/protocol"
)

// mcpStructuredContentMiddleware makes every tools/call response's
// structuredContent satisfy the output schema the tool advertises (#503).
//
// The mcp library derives that schema from the Go output type: a map becomes
// {"type":"object"} and a slice {"type":"array"}, with no way to say "or null".
// encoding/json writes a nil map or slice as null, and a strict client (Claude
// Code validates with Ajv) rejects the whole response when it meets one: one
// belief without a confidence decomposition, or an answer with no
// dissonances, and the caller received nothing.
//
// Removing a null member is lossless here. No output field is required, and
// a nil map or slice means "none", which an absent member also says. Null
// array elements are kept: they are values, and no output type produces one.
//
// The proper fix is a nullable schema for nil-able fields in the library
// generator; until mnemos depends on one, this keeps every one of the tools
// conformant in one place instead of each output type remembering.
func mcpStructuredContentMiddleware() middleware.Middleware {
	return func(next middleware.HandlerFunc) middleware.HandlerFunc {
		return func(ctx context.Context, req *protocol.Request) (*protocol.Response, error) {
			resp, err := next(ctx, req)
			if err != nil || resp == nil || req == nil || req.Method != protocol.MethodToolsCall {
				return resp, err
			}
			result, ok := resp.Result.(map[string]any)
			if !ok {
				return resp, nil
			}
			sc, present := result["structuredContent"]
			if !present || sc == nil {
				return resp, nil
			}
			cleaned, err := withoutNullMembers(sc)
			if err != nil {
				return nil, fmt.Errorf("normalize structured content: %w", err)
			}
			result["structuredContent"] = cleaned
			return resp, nil
		}
	}
}

// withoutNullMembers returns v as decoded JSON with every null object member
// removed, at any depth. v may be a typed value (a handler's
// StructuredResult) or already-decoded JSON.
func withoutNullMembers(v any) (any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var decoded any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	return dropNullMembers(decoded), nil
}

func dropNullMembers(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, e := range t {
			if e == nil {
				delete(t, k)
				continue
			}
			t[k] = dropNullMembers(e)
		}
	case []any:
		for i, e := range t {
			t[i] = dropNullMembers(e)
		}
	}
	return v
}
