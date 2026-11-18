package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"go.klarlabs.de/mnemos"
	"go.klarlabs.de/mnemos/client"
	"go.klarlabs.de/mnemos/internal/capability"
	"go.klarlabs.de/mnemos/internal/sourceguard"
	mnemosv1 "go.klarlabs.de/mnemos/proto/gen/mnemos/v1"
)

// TestCapabilityRegistry_MatchesEveryTransport is the capability contract's
// enforcement (#382 Phase 3). Each transport's REAL surface is read
// mechanically and compared with internal/capability.Registry in BOTH
// directions:
//
//   - exposed but unregistered: a transport gained an operation and nobody
//     decided which capability it is or where else it belongs;
//   - registered but not exposed: a binding outlived a rename or removal, so
//     the contract describes a surface that is gone.
//
// It replaces TestAPISurfaceParity, which compared regex-extracted names in
// one direction only against a hand matrix — so a stale row passed, REST
// sub-routes were invisible, and nine rows had drifted wrong (GET
// /v1/beliefs/{id} recorded as absent, for one).
//
// How each surface is read:
//
//	go        reflection over the mnemos.Memory interface
//	client    reflection over *client.Client's exported methods
//	grpc      the generated service descriptor
//	mcp       mcpToolScopes — itself held equal to the srv.Tool registrations
//	          by TestMCPToolScopes_CoversEveryRegisteredTool
//	mcp-lite  srv.Tool literals in the root mcp package (AST)
//	http-lite mux.HandleFunc literals in the root http package (AST)
//	rest      mux.Handle/HandleFunc literals in this package, with each prefix
//	          dispatcher expanded from its own sub-resource literals (AST)
//
// REST is compared by PATH: which methods a path accepts is decided inside its
// handler, where no static read can follow it. The method in each REST binding
// is documentation, checked by the handler tests.
func TestCapabilityRegistry_MatchesEveryTransport(t *testing.T) {
	if err := capability.Validate(capability.Registry); err != nil {
		t.Fatal(err)
	}
	for _, tr := range capability.Transports {
		t.Run(string(tr), func(t *testing.T) {
			exposed, err := exposedOps(tr)
			if err != nil {
				t.Fatalf("read the %s surface: %v", tr, err)
			}
			registered := capability.Ops(capability.Registry, tr)
			if tr == capability.REST {
				registered = restPaths(registered)
			}
			for _, op := range missing(exposed, registered) {
				t.Errorf("%s exposes %q, but no capability claims it — add it to internal/capability.Registry "+
					"(and decide which other transports should have it)", tr, op)
			}
			for _, op := range missing(registered, exposed) {
				t.Errorf("internal/capability.Registry binds %s %q, but the transport no longer exposes it — "+
					"update the binding or record why it is gone", tr, op)
			}
		})
	}
}

// One tool name must not mean two different things on the two MCP servers. The
// lite adapter predates the full server and reused two names for different
// capabilities; those are recorded here with the reason, and any NEW collision
// fails. An entry that no longer collides fails as stale.
var knownMCPNameCollisions = map[string]string{
	"remember": "lite `remember` extracts beliefs from text (ingest.text); full `remember` stores one fact (belief.remember_fact). " +
		"The lite adapter is a public package, so renaming its tool is a breaking change deferred to the next major version.",
	"recall": "lite `recall` is plain Recall (recall.basic); full `recall` is the mode-switched advanced recall (recall.advanced). " +
		"Deferred with `remember` for the same reason.",
}

func TestCapabilityRegistry_MCPToolNamesMeanOneThing(t *testing.T) {
	capOf := func(tr capability.Transport) map[string]string {
		out := map[string]string{}
		for _, c := range capability.Registry {
			for _, op := range c.On[tr].Ops {
				out[op] = c.ID
			}
		}
		return out
	}
	full, lite := capOf(capability.MCP), capOf(capability.MCPLite)
	colliding := map[string]bool{}
	for name, liteCap := range lite {
		if fullCap, ok := full[name]; ok && fullCap != liteCap {
			colliding[name] = true
			if reason := knownMCPNameCollisions[name]; reason == "" {
				t.Errorf("MCP tool %q is %s on the full server but %s on the lite adapter — one name, two meanings",
					name, fullCap, liteCap)
			}
		}
	}
	for name := range knownMCPNameCollisions {
		if !colliding[name] {
			t.Errorf("knownMCPNameCollisions[%q] no longer collides; remove the entry", name)
		}
	}
}

// The acknowledged gaps are reported, not hidden: each is a decision still to
// be made. The count is pinned so closing a gap, or opening one, is a visible
// change to this file rather than drift.
func TestCapabilityRegistry_GapsAreCounted(t *testing.T) {
	gaps := capability.Gaps(capability.Registry)
	const want = 84
	if len(gaps) != want {
		t.Errorf("acknowledged gaps: %d, pinned at %d — update the pin with the change that opened or closed one:\n  %s",
			len(gaps), want, strings.Join(gaps, "\n  "))
	}
}

func exposedOps(tr capability.Transport) ([]string, error) {
	switch tr {
	case capability.Go:
		return interfaceMethods(reflect.TypeOf((*mnemos.Memory)(nil)).Elem()), nil
	case capability.Client:
		return exportedMethods(reflect.TypeOf(&client.Client{})), nil
	case capability.GRPC:
		var out []string
		for _, m := range mnemosv1.MnemosService_ServiceDesc.Methods {
			out = append(out, m.MethodName)
		}
		for _, s := range mnemosv1.MnemosService_ServiceDesc.Streams {
			out = append(out, s.StreamName)
		}
		return sorted(out), nil
	case capability.MCP:
		out := make([]string, 0, len(mcpToolScopes))
		for name := range mcpToolScopes {
			out = append(out, name)
		}
		return sorted(out), nil
	case capability.MCPLite:
		return sourceguard.CallStringArgs(filepath.Join("..", "..", "mcp"), "Tool", 0)
	case capability.HTTPLite:
		return sourceguard.CallStringArgs(filepath.Join("..", "..", "http"), "HandleFunc", 0)
	case capability.REST:
		return restSurface()
	}
	return nil, fmt.Errorf("no reader for transport %s", tr)
}

// restSurface reads every path `mnemos serve` routes. Exact registrations are
// taken as they are. A registration ending in "/" is a prefix dispatcher; it is
// replaced by the paths its handler actually serves, read from the literals the
// handler dispatches on. A prefix dispatcher this function does not know how
// to expand is an error, not a silent omission.
func restSurface() ([]string, error) {
	handleFunc, err := sourceguard.CallStringArgs(".", "HandleFunc", 0)
	if err != nil {
		return nil, err
	}
	handle, err := sourceguard.CallStringArgs(".", "Handle", 0)
	if err != nil {
		return nil, err
	}
	expand := map[string]func() ([]string, error){
		"/v1/beliefs/": func() ([]string, error) {
			subs, err := sourceguard.SwitchCaseStrings("serve.go", "subresource")
			if err != nil {
				return nil, err
			}
			out := []string{"/v1/beliefs/{id}"}
			for _, s := range subs {
				out = append(out, "/v1/beliefs/{id}/"+s)
			}
			return out, nil
		},
		"/v1/incidents/": func() ([]string, error) {
			subs, err := comparedLiterals("serve.go", "makeIncidentSubresourceHandler", "sub")
			if err != nil {
				return nil, err
			}
			var out []string
			for _, s := range subs {
				if s == "" {
					out = append(out, "/v1/incidents/{id}")
				} else {
					out = append(out, "/v1/incidents/{id}/"+s)
				}
			}
			return out, nil
		},
		"/v1/actions/": func() ([]string, error) {
			subs, err := comparedLiterals("serve_batch4.go", "makeActionSubresourceHandler", "parts[1]")
			if err != nil {
				return nil, err
			}
			var out []string
			for _, s := range subs {
				out = append(out, "/v1/actions/{id}/"+s)
			}
			return out, nil
		},
		"/v1/decisions/": func() ([]string, error) { return []string{"/v1/decisions/{id}"}, nil },
	}
	var out []string
	for _, p := range append(handleFunc, handle...) {
		if !strings.HasSuffix(p, "/") || p == "/" {
			out = append(out, p)
			continue
		}
		fn, ok := expand[p]
		if !ok {
			return nil, fmt.Errorf("prefix route %q has no expansion in restSurface; teach the test which paths it serves", p)
		}
		paths, err := fn()
		if err != nil {
			return nil, fmt.Errorf("expand %s: %w", p, err)
		}
		out = append(out, paths...)
	}
	return sorted(out), nil
}

// comparedLiterals returns the string literals compared with == or != against
// the expression expr (rendered as source, e.g. "sub" or "parts[1]") inside
// function fn in file. Finding none is an error: the dispatcher changed shape
// and the guard would otherwise compare against an empty set.
func comparedLiterals(file, fn, expr string) ([]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		return nil, err
	}
	var body *ast.BlockStmt
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == fn {
			body = fd.Body
		}
	}
	if body == nil {
		return nil, fmt.Errorf("function %s not found in %s", fn, file)
	}
	set := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		be, ok := n.(*ast.BinaryExpr)
		if !ok || (be.Op != token.EQL && be.Op != token.NEQ) {
			return true
		}
		for _, pair := range [][2]ast.Expr{{be.X, be.Y}, {be.Y, be.X}} {
			lit, ok := pair[1].(*ast.BasicLit)
			if ok && lit.Kind == token.STRING && types.ExprString(pair[0]) == expr {
				if s, err := strconv.Unquote(lit.Value); err == nil {
					set[s] = true
				}
			}
		}
		return true
	})
	if len(set) == 0 {
		return nil, fmt.Errorf("no string literal is compared with %s in %s", expr, fn)
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	return sorted(out), nil
}

// restPaths strips the method from "METHOD /path" bindings.
func restPaths(ops []string) []string {
	set := map[string]bool{}
	for _, op := range ops {
		if _, path, ok := strings.Cut(op, " "); ok {
			set[path] = true
		} else {
			set[op] = true
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	return sorted(out)
}

func interfaceMethods(t reflect.Type) []string {
	out := make([]string, 0, t.NumMethod())
	for i := range t.NumMethod() {
		out = append(out, t.Method(i).Name)
	}
	return sorted(out)
}

func exportedMethods(t reflect.Type) []string {
	out := make([]string, 0, t.NumMethod())
	for i := range t.NumMethod() {
		out = append(out, t.Method(i).Name)
	}
	return sorted(out)
}

// missing returns the members of want absent from got.
func missing(want, got []string) []string {
	have := map[string]bool{}
	for _, g := range got {
		have[g] = true
	}
	var out []string
	for _, w := range want {
		if !have[w] {
			out = append(out, w)
		}
	}
	return out
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
