// Package capability is the machine-readable contract of what Mnemos exposes,
// and on which transport (#382, Phase 3).
//
// Mnemos is reachable seven ways: the embedded Go library ([Go], the Memory
// interface), REST (`mnemos serve`), gRPC, the full MCP server (`mnemos mcp`),
// the lightweight MCP and HTTP adapters in the root mcp and http packages, and
// the Go REST client. They were written independently and drift: an operation
// ships on three of them, a fourth forgets it, and nothing notices. A Markdown
// table describing the drift drifts too.
//
// So the contract is data. Every [Capability] names, for EVERY transport,
// either the concrete operations that implement it or the reason it is not
// there. There is no third state: a transport cannot be silently absent from a
// capability. cmd/mnemos's parity test reads each transport's real surface
// mechanically — the Memory interface and the client by reflection, gRPC from
// its service descriptor, MCP from the scope map that is itself locked to the
// tool registrations, REST from the mux registrations and its sub-resource
// dispatchers via the AST — and fails in BOTH directions:
//
//   - an operation a transport exposes that no capability claims (a new
//     endpoint shipped without deciding where else it belongs), and
//   - a binding no transport implements any more (a rename or removal that
//     would otherwise leave this registry describing a surface that is gone).
//
// What it cannot see: that two bindings of one capability BEHAVE the same.
// [Capability.Notes] records known behavioural differences; behaviour is
// guarded where each operation is built, not here.
package capability

import (
	"fmt"
	"sort"
	"strings"
)

// Transport identifies one way Mnemos is reached.
type Transport string

// The transports, in the order reports list them.
const (
	Go       Transport = "go"        // the embedded library: mnemos.Memory methods
	REST     Transport = "rest"      // `mnemos serve`: "METHOD /path" (path params as {id})
	GRPC     Transport = "grpc"      // mnemos.v1.MnemosService RPC names
	MCP      Transport = "mcp"       // `mnemos mcp` (stdio and --http) tool names
	MCPLite  Transport = "mcp-lite"  // root package mcp: tool names
	HTTPLite Transport = "http-lite" // root package http: "METHOD /path" patterns
	Client   Transport = "client"    // go.klarlabs.de/mnemos/client: *Client method names
)

// Transports lists every transport in report order.
var Transports = []Transport{Go, REST, GRPC, MCP, MCPLite, HTTPLite, Client}

// Effect classifies what a capability does to the brain.
type Effect string

// The effects a capability can have.
const (
	Read  Effect = "read"  // reads knowledge
	Write Effect = "write" // changes knowledge
	Infra Effect = "infra" // process, server or library plumbing, not knowledge
)

// Binding is how one transport provides a capability: the operations that
// implement it, or why it does not. Exactly one of the two is set.
type Binding struct {
	Ops    []string
	Reason string
}

// Bound reports whether the transport implements the capability.
func (b Binding) Bound() bool { return len(b.Ops) > 0 }

// IsGap reports whether the omission is an acknowledged gap rather than a
// decision.
func (b Binding) IsGap() bool { return !b.Bound() && strings.HasPrefix(b.Reason, GapPrefix) }

// Has binds operations.
func Has(ops ...string) Binding { return Binding{Ops: ops} }

// No records a deliberate or acknowledged omission. The reason is mandatory.
func No(reason string) Binding { return Binding{Reason: reason} }

// GapPrefix starts the reason of every omission that is NOT a decision — an
// operation that should exist on the transport and does not yet.
const GapPrefix = "GAP:"

// Gap is an acknowledged, unintended omission.
func Gap(detail string) Binding { return No(GapPrefix + " " + detail) }

// Capability is one logical operation and its binding on every transport.
type Capability struct {
	ID      string
	Summary string
	Effect  Effect
	On      map[Transport]Binding
	// Notes records behavioural differences between bindings that share the
	// capability, which a name-level contract cannot see.
	Notes string
}

// Validate checks the registry's own invariants: unique ids, every transport
// declared on every capability, exactly one of ops or reason per binding.
func Validate(caps []Capability) error {
	seen := map[string]bool{}
	var errs []string
	for _, c := range caps {
		if c.ID == "" || c.Summary == "" {
			errs = append(errs, fmt.Sprintf("capability %q: id and summary are required", c.ID))
		}
		if seen[c.ID] {
			errs = append(errs, fmt.Sprintf("capability %q is declared twice", c.ID))
		}
		seen[c.ID] = true
		switch c.Effect {
		case Read, Write, Infra:
		default:
			errs = append(errs, fmt.Sprintf("capability %q: effect %q is not read, write or infra", c.ID, c.Effect))
		}
		bound := 0
		for _, t := range Transports {
			b, ok := c.On[t]
			switch {
			case !ok:
				errs = append(errs, fmt.Sprintf("capability %q: transport %s is undeclared — bind it or say why not", c.ID, t))
			case b.Bound() && b.Reason != "":
				errs = append(errs, fmt.Sprintf("capability %q: transport %s has both operations and a reason", c.ID, t))
			case !b.Bound() && strings.TrimSpace(strings.TrimPrefix(b.Reason, GapPrefix)) == "":
				errs = append(errs, fmt.Sprintf("capability %q: transport %s is omitted without a reason", c.ID, t))
			}
			if b.Bound() {
				bound++
			}
		}
		if bound == 0 {
			errs = append(errs, fmt.Sprintf("capability %q is bound on no transport", c.ID))
		}
		for t := range c.On {
			if !knownTransport(t) {
				errs = append(errs, fmt.Sprintf("capability %q: unknown transport %q", c.ID, t))
			}
		}
	}
	sort.Strings(errs)
	if len(errs) > 0 {
		return fmt.Errorf("capability registry:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}

func knownTransport(t Transport) bool {
	for _, k := range Transports {
		if k == t {
			return true
		}
	}
	return false
}

// Ops returns every operation bound on t, across all capabilities, deduplicated
// and sorted. For REST and HTTPLite these are "METHOD /path" strings.
func Ops(caps []Capability, t Transport) []string {
	set := map[string]bool{}
	for _, c := range caps {
		for _, op := range c.On[t].Ops {
			set[op] = true
		}
	}
	out := make([]string, 0, len(set))
	for op := range set {
		out = append(out, op)
	}
	sort.Strings(out)
	return out
}

// Gaps returns every acknowledged gap as "capability/transport".
func Gaps(caps []Capability) []string {
	var out []string
	for _, c := range caps {
		for _, t := range Transports {
			if c.On[t].IsGap() {
				out = append(out, c.ID+"/"+string(t))
			}
		}
	}
	sort.Strings(out)
	return out
}
