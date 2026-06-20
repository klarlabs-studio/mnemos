package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// storageWriteMethods are the repository methods that mutate durable
// storage. A delivery adapter (cmd/mnemos, internal/server/grpc) calling
// any of these reaches into storage directly and therefore bypasses the
// governed write path (axi evidence + token budget). The spec's
// non-negotiable is that every write flows through the governed library
// API (mnemos.Memory) or the shared kernel; the Store interface is the
// stable API, and delivery adapters are packaging conveniences that must
// not reach past it.
var storageWriteMethods = map[string]bool{
	"Append":           true,
	"Upsert":           true,
	"UpsertEvidence":   true,
	"PersistArtifacts": true,
}

// adapterDirs are the delivery-adapter packages the no-bypass rule
// governs, relative to the module root.
var adapterDirs = []string{
	"cmd/mnemos",
	"internal/server/grpc",
}

// bypassException is a documented, deliberately-allowed direct storage
// write that has no governed Memory equivalent today. Each carries a
// rationale and a TODO so the debt is visible and tracked rather than
// silently tolerated. The key is "<relpath>:<method>" with the repo
// receiver, e.g. "internal/server/grpc/server.go:Actions.Append".
//
// Routable Event/Claim/Relationship writes are NOT listed here — those
// belong on the governed path and the test fails if a new one appears.
//
// The non-routable repositories (Actions, Outcomes, Lessons, Decisions,
// Playbooks, EntityRels, Incidents, Feedback, Embeddings, Users, Agents,
// Entities, Jobs, RevokedTokens) have no mnemos.Memory method and no
// kernel action today; routing them requires first extending the
// governed surface (TODO #governed-surface). Until then they are flagged
// here so the bypass set is explicit and locked: adding a NEW direct
// write — or a new Event/Claim/Relationship bypass — fails this test.
var bypassExceptions = map[string]string{
	// --- cmd/mnemos: free-text / structured ingestion with custom
	// event-id + dedup semantics the public Memory surface doesn't
	// expose (e.g. ev_git_<sha>, SHA-dedup, PR ids). TODO: extend the
	// governed surface with an ingest action that accepts pre-built
	// events so these route through the kernel.
	"cmd/mnemos/gitcontext.go:PersistArtifacts":    "git-log ingestor: custom ev_git_<sha> ids + SHA dedup; no governed equivalent. TODO #governed-ingest",
	"cmd/mnemos/prcontext.go:PersistArtifacts":     "PR ingestor: custom pr-context ids + dedup; no governed equivalent. TODO #governed-ingest",
	"cmd/mnemos/autoingest.go:PersistArtifacts":    "autoingest: watch-driven batch with own ids; no governed equivalent. TODO #governed-ingest",
	"cmd/mnemos/main.go:PersistArtifacts":          "process_text CLI path: pre-kernel ingestion; TODO route through Memory.Remember",
	"cmd/mnemos/mcp.go:PersistArtifacts":           "process_text MCP tool: governed via its own MCP kernel action, persists after kernel dispatch. TODO converge with library executors",
	"cmd/mnemos/main.go:Events.Append":             "CLI event add: pre-built event with explicit id; TODO route via Memory.RememberEvent",
	"cmd/mnemos/main.go:Claims.Upsert":             "CLI claim import: bulk import; TODO route via Memory.RememberClaim",
	"cmd/mnemos/main.go:Claims.UpsertEvidence":     "CLI claim import evidence links; pairs with Claims.Upsert above",
	"cmd/mnemos/main.go:Relationships.Upsert":      "CLI relationship import: no governed equivalent",
	"cmd/mnemos/serve.go:Events.Append":            "HTTP POST /v1/events: pre-built event; TODO route via Memory.RememberEvent",
	"cmd/mnemos/serve.go:Claims.Upsert":            "HTTP POST /v1/claims: bulk; TODO route via Memory.RememberClaim",
	"cmd/mnemos/serve.go:Claims.UpsertEvidence":    "HTTP claim evidence links; pairs with serve Claims.Upsert",
	"cmd/mnemos/serve.go:Relationships.Upsert":     "HTTP relationship upsert: no governed equivalent",
	"cmd/mnemos/serve.go:Embeddings.Upsert":        "HTTP embedding upsert: no governed equivalent (embeddings are derived state)",
	"cmd/mnemos/serve.go:Feedback.Upsert":          "HTTP feedback state: side table, no governed equivalent",
	"cmd/mnemos/serve.go:Incidents.Upsert":         "HTTP incident upsert: no governed equivalent",
	"cmd/mnemos/registry.go:Events.Append":         "federation import: remote-sourced events; no governed equivalent",
	"cmd/mnemos/registry.go:Claims.Upsert":         "federation import: remote-sourced claims; no governed equivalent",
	"cmd/mnemos/registry.go:Claims.UpsertEvidence": "federation import evidence links",
	"cmd/mnemos/registry.go:Embeddings.Upsert":     "federation import embeddings (derived state)",
	"cmd/mnemos/registry.go:Relationships.Upsert":  "federation import relationships",
	"cmd/mnemos/actions.go:Actions.Append":         "action log: no Memory equivalent. TODO #governed-surface",
	"cmd/mnemos/actions.go:Outcomes.Append":        "outcome log: no Memory equivalent. TODO #governed-surface",
	"cmd/mnemos/decisions.go:Decisions.Append":     "decision log: no Memory equivalent. TODO #governed-surface",
	"cmd/mnemos/incidents.go:Incidents.Upsert":     "incident upsert: no Memory equivalent. TODO #governed-surface",
	"cmd/mnemos/markdown.go:Lessons.Append":        "lesson import: no Memory equivalent. TODO #governed-surface",
	"cmd/mnemos/markdown.go:Playbooks.Append":      "playbook import: no Memory equivalent. TODO #governed-surface",
	"cmd/mnemos/admin.go:Embeddings.Upsert":        "admin re-embed: derived state, no governed equivalent",
	"cmd/mnemos/mcp.go:Actions.Append":             "MCP action log: no Memory equivalent. TODO #governed-surface",
	"cmd/mnemos/mcp.go:Decisions.Append":           "MCP decision log: no Memory equivalent. TODO #governed-surface",
	"cmd/mnemos/mcp.go:Outcomes.Append":            "MCP outcome log: no Memory equivalent. TODO #governed-surface",
	"cmd/mnemos/mcp.go:Events.Append":              "MCP event add: pre-built event; TODO route via Memory.RememberEvent",
	"cmd/mnemos/mcp.go:Claims.UpsertEvidence":      "MCP claim evidence links: no Memory equivalent for standalone link",

	// --- internal/server/grpc: the gRPC daemon mirrors the HTTP/CLI
	// surface. Same rationale per repository.
	"internal/server/grpc/server.go:Events.Append":              "gRPC AddEvent: pre-built event; TODO route via Memory.RememberEvent",
	"internal/server/grpc/server.go:Claims.Upsert":              "gRPC AddClaims: bulk; TODO route via Memory.RememberClaim",
	"internal/server/grpc/server.go:Claims.UpsertEvidence":      "gRPC claim evidence links",
	"internal/server/grpc/server.go:Relationships.Upsert":       "gRPC relationship upsert: no governed equivalent",
	"internal/server/grpc/server.go:Embeddings.Upsert":          "gRPC embedding upsert (derived state)",
	"internal/server/grpc/server_phase2_7.go:Actions.Append":    "gRPC action log: no Memory equivalent. TODO #governed-surface",
	"internal/server/grpc/server_phase2_7.go:Outcomes.Append":   "gRPC outcome log: no Memory equivalent. TODO #governed-surface",
	"internal/server/grpc/server_phase2_7.go:Lessons.Append":    "gRPC lesson log: no Memory equivalent. TODO #governed-surface",
	"internal/server/grpc/server_phase2_7.go:Decisions.Append":  "gRPC decision log: no Memory equivalent. TODO #governed-surface",
	"internal/server/grpc/server_phase2_7.go:Playbooks.Append":  "gRPC playbook log: no Memory equivalent. TODO #governed-surface",
	"internal/server/grpc/server_phase2_7.go:EntityRels.Upsert": "gRPC entity-rel upsert: no Memory equivalent. TODO #governed-surface",
}

// foundWrite is a discovered direct storage write.
type foundWrite struct {
	key string // "<relpath>:<Repo>.<Method>" or "<relpath>:<Method>"
	pos string
}

// TestNoBypass_DeliveryAdaptersDoNotReachStorage walks the delivery
// adapter packages and asserts every direct storage write is on the
// documented exception allow-list. A NEW direct write (or one not yet
// flagged) fails the test, forcing the author to either route it through
// the governed Memory/kernel path or justify it as an exception. Stale
// exceptions (listed but no longer present) also fail, so the list can't
// rot.
func TestNoBypass_DeliveryAdaptersDoNotReachStorage(t *testing.T) {
	root := moduleRoot(t)

	var found []foundWrite
	for _, dir := range adapterDirs {
		found = append(found, scanDirForStorageWrites(t, root, dir)...)
	}

	seen := map[string]bool{}
	for _, fw := range found {
		seen[fw.key] = true
		if _, ok := bypassExceptions[fw.key]; !ok {
			t.Errorf("UNGOVERNED direct storage write at %s (%s)\n"+
				"  route it through mnemos.Memory / the kernel, or add a documented "+
				"bypassExceptions entry with a rationale + TODO.", fw.pos, fw.key)
		}
	}

	// Detect stale exceptions so the allow-list stays honest.
	var stale []string
	for key := range bypassExceptions {
		if !seen[key] {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	for _, key := range stale {
		t.Errorf("stale bypassExceptions entry %q no longer matches any code — remove it", key)
	}
}

// scanDirForStorageWrites parses every non-test .go file under
// root/dir and returns the storage-write call sites it finds.
func scanDirForStorageWrites(t *testing.T, root, dir string) []foundWrite {
	t.Helper()
	var out []foundWrite
	abs := filepath.Join(root, dir)
	entries, err := os.ReadDir(abs)
	if err != nil {
		t.Fatalf("read dir %s: %v", abs, err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(abs, name)
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		rel := filepath.ToSlash(filepath.Join(dir, name))
		// First pass: collect local aliases of repository handles, e.g.
		// `repo := conn.Events` or `claimRepo := conn.Claims`, so a write
		// through the alias (repo.Append) is still attributed to its
		// repository and can't evade the guard by hiding behind a local.
		aliases := collectRepoAliases(file)
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if !storageWriteMethods[sel.Sel.Name] {
				return true
			}
			key, ok := classifyWrite(rel, sel, aliases)
			if !ok {
				return true
			}
			out = append(out, foundWrite{key: key, pos: fset.Position(call.Pos()).String()})
			return true
		})
	}
	return out
}

// collectRepoAliases finds `ident := <recv>.<Repo>` assignments that
// alias a *store.Conn repository field to a local variable, returning a
// map of local name -> repository name.
func collectRepoAliases(file *ast.File) map[string]string {
	aliases := map[string]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		lhs, ok := assign.Lhs[0].(*ast.Ident)
		if !ok {
			return true
		}
		sel, ok := assign.Rhs[0].(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if storageRepos[sel.Sel.Name] {
			aliases[lhs.Name] = sel.Sel.Name
		}
		return true
	})
	return aliases
}

// classifyWrite turns a write call into a stable exception key. It
// resolves three receiver shapes: pipeline.PersistArtifacts(...);
// <recv>.<Repo>.<Method> (conn.Claims.Upsert); and <alias>.<Method>
// where <alias> was assigned from a repository field. Returns ok=false
// for calls that aren't storage writes.
func classifyWrite(rel string, sel *ast.SelectorExpr, aliases map[string]string) (string, bool) {
	method := sel.Sel.Name
	if method == "PersistArtifacts" {
		if x, ok := sel.X.(*ast.Ident); ok && x.Name == "pipeline" {
			return rel + ":PersistArtifacts", true
		}
		return "", false
	}
	// conn.Claims.Upsert / s.conn.Claims.Upsert
	if inner, ok := sel.X.(*ast.SelectorExpr); ok && storageRepos[inner.Sel.Name] {
		return rel + ":" + inner.Sel.Name + "." + method, true
	}
	// aliasVar.Upsert where aliasVar := conn.Claims
	if id, ok := sel.X.(*ast.Ident); ok {
		if repo, aliased := aliases[id.Name]; aliased {
			return rel + ":" + repo + "." + method, true
		}
	}
	return "", false
}

// storageRepos is the set of *store.Conn repository field names. Used to
// distinguish a real storage write (conn.Claims.Upsert) from an unrelated
// method named Upsert/Append on some other receiver.
var storageRepos = map[string]bool{
	"Events": true, "Claims": true, "Relationships": true, "Embeddings": true,
	"Users": true, "RevokedTokens": true, "Agents": true, "Entities": true,
	"Jobs": true, "Actions": true, "Outcomes": true, "Lessons": true,
	"Decisions": true, "Playbooks": true, "EntityRels": true, "Incidents": true,
	"Feedback": true, "ClaimVersions": true,
}

// moduleRoot returns the repository root (the dir containing go.mod) by
// walking up from the test's working directory.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find module root (go.mod)")
		}
		dir = parent
	}
}
