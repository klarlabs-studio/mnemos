package main

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"

	axidomain "go.klarlabs.de/axi/domain"
	"go.klarlabs.de/bolt"
	"go.klarlabs.de/mcp/middleware"
	"go.klarlabs.de/mcp/protocol"
	"go.klarlabs.de/mnemos/internal/auth"
	"go.klarlabs.de/mnemos/internal/domain"
)

// The map must cover every tool the server actually registers. A tool added
// without a scope classification falls through to the wildcard default, which
// is safe but locks out legitimate callers — better to fail here, at build
// time, than to debug a 403 in production.
func TestMCPToolScopes_CoversEveryRegisteredTool(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("locate repo root: %v", err)
	}
	registered := mustReadMCPTools(t, filepath.Join(root, "cmd/mnemos/mcp.go"))
	if len(registered) == 0 {
		t.Fatal("no registered tool names found; the srv.Tool extraction needs updating")
	}
	for _, name := range registered {
		if _, ok := mcpToolScopes[name]; !ok {
			t.Errorf("tool %q is registered but unclassified in mcpToolScopes — "+
				"add it as a read (\"\") or with the scope its writes require", name)
		}
	}
	for name := range mcpToolScopes {
		if !hasToolName(registered, name) {
			t.Errorf("mcpToolScopes lists %q, which is no longer registered", name)
		}
	}
}

// The axi-kernel registry already declares each tool it covers as a read or a
// write. Where the two overlap they must agree — a tool declared
// EffectWriteLocal but mapped to the empty scope is an ungated write.
func TestMCPToolScopes_AgreeWithKernelEffects(t *testing.T) {
	for _, action := range mcpTools() {
		scope, ok := mcpToolScopes[action.Name]
		if !ok {
			t.Errorf("kernel action %q missing from mcpToolScopes", action.Name)
			continue
		}
		switch action.Effect {
		case axidomain.EffectWriteLocal:
			if scope == "" {
				t.Errorf("%q is EffectWriteLocal but requires no scope — it is an ungated write", action.Name)
			}
		case axidomain.EffectReadLocal:
			if scope != "" {
				t.Errorf("%q is EffectReadLocal but requires scope %q; reads should need none", action.Name, scope)
			}
		}
	}
}

// An unclassified tool must default to deny-by-wildcard, never to "read".
func TestRequiredScopeForTool_UnknownDefaultsClosed(t *testing.T) {
	if got := requiredScopeForTool("some_tool_added_tomorrow"); got != domain.ScopeWildcard {
		t.Errorf("unknown tool required scope %q, want %q (fail closed)", got, domain.ScopeWildcard)
	}
}

// The core regression: a read-scoped token reaches read tools and is refused
// write tools. Before the fix it reached everything.
func TestEnforceToolScope_ReadTokenCannotWrite(t *testing.T) {
	ctx := withClaims(context.Background(), &auth.Claims{Scopes: []string{"claims:read"}})

	if err := enforceToolScope(ctx, "query_knowledge"); err != nil {
		t.Errorf("read token refused a read tool: %v", err)
	}
	for _, tool := range []string{"process_text", "remember", "forget", "set_block", "record_decision"} {
		err := enforceToolScope(ctx, tool)
		if err == nil {
			t.Errorf("read-scoped token was allowed to call write tool %q", tool)
			continue
		}
		if !strings.Contains(err.Error(), "missing required scope") {
			t.Errorf("%s: unexpected error %v", tool, err)
		}
	}
}

func TestEnforceToolScope_WriteTokenAndWildcard(t *testing.T) {
	writer := withClaims(context.Background(), &auth.Claims{Scopes: []string{domain.ScopeClaimsWrite}})
	if err := enforceToolScope(writer, "process_text"); err != nil {
		t.Errorf("claims:write token refused process_text: %v", err)
	}
	// A data-write scope must NOT reach the host-level configuration tool.
	if err := enforceToolScope(writer, "configure_environment"); err == nil {
		t.Error("claims:write token reached configure_environment (a server-filesystem write)")
	}
	// promote:global gates the tenant→global boundary specifically.
	if err := enforceToolScope(writer, "memory_promote"); err == nil {
		t.Error("claims:write token reached memory_promote without promote:global")
	}

	wildcard := withClaims(context.Background(), &auth.Claims{Scopes: []string{domain.ScopeWildcard}})
	for _, tool := range []string{"process_text", "memory_promote", "configure_environment", "query_knowledge"} {
		if err := enforceToolScope(wildcard, tool); err != nil {
			t.Errorf("wildcard token refused %q: %v", tool, err)
		}
	}
}

// stdio carries no claims. Gating it would break every existing local install,
// and the user already owns that process.
func TestEnforceToolScope_UnauthenticatedPassesThrough(t *testing.T) {
	for _, tool := range []string{"process_text", "memory_promote", "configure_environment"} {
		if err := enforceToolScope(context.Background(), tool); err != nil {
			t.Errorf("unauthenticated (stdio) call to %q was refused: %v", tool, err)
		}
	}
}

// An authenticated token with no scopes at all must be refused writes, matching
// requireScope's behaviour on the REST surface.
func TestEnforceToolScope_EmptyScopesDenied(t *testing.T) {
	ctx := withClaims(context.Background(), &auth.Claims{})
	if err := enforceToolScope(ctx, "process_text"); err == nil {
		t.Error("token with no scopes was allowed to write")
	}
	if err := enforceToolScope(ctx, "query_knowledge"); err != nil {
		t.Errorf("token with no scopes was refused a read: %v", err)
	}
}

// The middleware must enforce on tools/call and stay out of the way otherwise.
func TestMCPScopeMiddleware(t *testing.T) {
	called := false
	next := func(ctx context.Context, req *protocol.Request) (*protocol.Response, error) {
		called = true
		return &protocol.Response{}, nil
	}
	guard := mcpScopeMiddleware()(next)
	ctx := withClaims(context.Background(), &auth.Claims{Scopes: []string{"claims:read"}})

	call := func(method, tool string) error {
		called = false
		params, _ := json.Marshal(toolCallParams{Name: tool})
		_, err := guard(ctx, &protocol.Request{Method: method, Params: params})
		return err
	}

	if err := call(protocol.MethodToolsCall, "process_text"); err == nil {
		t.Error("middleware let a read token through to process_text")
	} else if called {
		t.Error("handler ran despite the scope denial")
	}

	if err := call(protocol.MethodToolsCall, "query_knowledge"); err != nil {
		t.Errorf("middleware blocked a permitted read: %v", err)
	} else if !called {
		t.Error("permitted read never reached the handler")
	}

	// Non-tool methods are not gated.
	if err := call("tools/list", ""); err != nil {
		t.Errorf("middleware interfered with tools/list: %v", err)
	}
}

func hasToolName(haystack []string, want string) bool {
	for _, s := range haystack {
		if s == want {
			return true
		}
	}
	return false
}

// The guard is only worth anything if it is actually in the chain the server
// serves behind. This exercises the REAL stack from mcpMiddlewareStack rather
// than the bare middleware, so removing it from the wiring fails here.
func TestMCPMiddlewareStack_EnforcesScopes(t *testing.T) {
	logger := bolt.New(bolt.NewJSONHandler(io.Discard))

	reached := false
	handler := middleware.HandlerFunc(func(ctx context.Context, req *protocol.Request) (*protocol.Response, error) {
		reached = true
		return &protocol.Response{}, nil
	})
	// Compose exactly as mcp.WithMiddleware does: last registered wraps closest
	// to the handler.
	stack := mcpMiddlewareStack(logger)
	for i := len(stack) - 1; i >= 0; i-- {
		handler = stack[i](handler)
	}

	params, _ := json.Marshal(toolCallParams{Name: "process_text"})
	ctx := withClaims(context.Background(), &auth.Claims{Scopes: []string{"claims:read"}})
	if _, err := handler(ctx, &protocol.Request{JSONRPC: "2.0", Method: protocol.MethodToolsCall, Params: params}); err == nil {
		t.Error("the served middleware stack let a read-scoped token call process_text")
	}
	if reached {
		t.Error("process_text handler ran despite the read-only token")
	}
}
