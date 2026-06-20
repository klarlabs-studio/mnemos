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

// The no-bypass guard FAILS CLOSED. Rather than allow-listing a handful
// of known write method names (the previous design, which silently let
// new mutators — MarkVerified, SetValidity, DeleteCascade, Merge, … —
// ship below its sight line), it treats EVERY exported method on a
// governed memory repository as a write UNLESS that method is on the
// explicit read-only allow-list below. A newly-added mutator therefore
// fails the guard by default; the author must either route it through
// the governed Writer or, deliberately, add it to storageReadMethods
// with a rationale. The spec's non-negotiable is that every MEMORY write
// flows through the governed surface (mnemos.Memory or internal/govwrite);
// the Store interface is the stable API and delivery adapters
// (cmd/mnemos, internal/server/grpc) are packaging conveniences that must
// not reach past it.
//
// storageReadMethods is the curated set of NON-mutating repository
// methods — gets, lists, counts, and read-only searches/scorers across
// every port interface in internal/ports. Anything NOT here, called on a
// memory repo, is treated as a write. Keep this list honest: adding a
// real mutator here to dodge the guard defeats the point.
var storageReadMethods = map[string]bool{
	// Single-row / by-key reads.
	"GetByID": true, "GetByEmail": true, "Get": true, "IsRevoked": true,
	"FindByName": true,
	// List / enumeration reads.
	"List": true, "ListAll": true, "ListByIDs": true, "ListByEventIDs": true,
	"ListEvidenceByClaimIDs": true, "ListByRunID": true, "ListBySubject": true,
	"ListByActionID": true, "ListByClaim": true, "ListByClaimIDs": true,
	"ListByEntity": true, "ListByKind": true, "ListByService": true,
	"ListByTrigger": true, "ListByRiskLevel": true, "ListByStatus": true,
	"ListBySeverity": true, "ListByType": true, "ListByTestRequirementRef": true,
	"ListStatusHistoryByClaimID": true, "ListAllEvidence": true,
	"ListAllStatusHistory": true, "ListEvidence": true, "ListBeliefs": true,
	"ListLessons": true, "ListVersions": true, "ListClaimsForEntity": true,
	"ListEntitiesForClaim": true, "ListByEntityType": true,
	"ListIDsMissingEmbedding": true, "ClaimIDsMissingEntityLinks": true,
	// Counts.
	"CountAll": true, "CountByType": true, "Count": true,
	"CountClaimsBelowTrust": true,
	// Read-only searches / scorers (no row mutation).
	"SearchByText": true, "SearchClaimsByVector": true, "AverageTrust": true,
}

// pipelineWriteFuncs are package-level pipeline functions that persist
// artifacts directly. They are not method calls on a repo field, so they
// are matched by name + receiver package rather than by the read-only
// classification above.
var pipelineWriteFuncs = map[string]bool{
	"PersistArtifacts": true,
}

// adapterDirs are the delivery-adapter packages the no-bypass rule
// governs, relative to the module root.
var adapterDirs = []string{
	"cmd/mnemos",
	"internal/server/grpc",
}

// bypassExceptions is the documented allow-list of direct MEMORY writes
// that do NOT flow through the governed kernel path. It is EMPTY: the
// daemon-write bypass is closed for every memory repository. Each memory
// mutation the delivery adapters (cmd/mnemos, internal/server/grpc)
// perform now routes through the governed surface — the public library
// kernel (mnemos.Memory) or the internal/govwrite daemon writer — so the
// spec's non-negotiable (EVERY memory write through axi) holds with zero
// exceptions.
//
// This is honest emptiness, not omission: the guard fails CLOSED (see
// storageReadMethods) so a newly-added mutator is flagged by default, and
// the only writes deliberately left out of scope are the operational/auth
// repos, excluded by REPO NAME with a written rationale (see the SCOPE
// DECISION on memoryRepos) — never by quietly dropping method names.
//
// The key format, if an entry ever has to be added back, is
// "<relpath>:<Repo>.<Method>", e.g.
// "internal/server/grpc/server.go:Actions.Append". Any NEW direct memory
// write fails this test: route it through the kernel (govwrite.Writer /
// mnemos.Memory) or, only if genuinely impossible, add a precisely-
// justified entry here. Stale entries also fail, so the list cannot rot.
var bypassExceptions = map[string]string{}

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

// classifyWrite decides whether a call is a GOVERNED-MEMORY write that
// bypasses the kernel, and if so returns a stable exception key. It fails
// closed: a call on a memory repo counts as a write unless its method is
// on storageReadMethods. It resolves four shapes:
//
//   - pipeline.PersistArtifacts(...) — a package-level write func.
//   - <recv>.<Repo>.<Method> (conn.Claims.Upsert) — a repo-field method.
//   - <alias>.<Method> where <alias> := conn.Claims — aliased repo.
//
// Calls on operationalRepos (auth/agent-registry/jobs) are intentionally
// NOT flagged — see the SCOPE DECISION on memoryRepos. Read-only methods
// on memory repos are not flagged either.
func classifyWrite(rel string, sel *ast.SelectorExpr, aliases map[string]string) (string, bool) {
	method := sel.Sel.Name
	// Package-level pipeline write functions (pipeline.PersistArtifacts).
	if pipelineWriteFuncs[method] {
		if x, ok := sel.X.(*ast.Ident); ok && x.Name == "pipeline" {
			return rel + ":" + method, true
		}
		return "", false
	}
	// Resolve the repository this method is called on, if any.
	repo, ok := repoOfSelector(sel, aliases)
	if !ok {
		return "", false
	}
	// Operational/auth repos are out of scope by the memory-vs-operational
	// decision; never flag them.
	if !memoryRepos[repo] {
		return "", false
	}
	// Fail closed: on a memory repo, anything that is not an explicit
	// read is a write.
	if storageReadMethods[method] {
		return "", false
	}
	return rel + ":" + repo + "." + method, true
}

// repoOfSelector returns the *store.Conn repository field a selector's
// method is invoked on, resolving both the direct <recv>.<Repo>.<Method>
// shape and the aliased <alias>.<Method> shape. ok=false when the call
// isn't on a known repository.
func repoOfSelector(sel *ast.SelectorExpr, aliases map[string]string) (string, bool) {
	// conn.Claims.Upsert / s.conn.Claims.Upsert
	if inner, ok := sel.X.(*ast.SelectorExpr); ok && storageRepos[inner.Sel.Name] {
		return inner.Sel.Name, true
	}
	// aliasVar.Upsert where aliasVar := conn.Claims
	if id, ok := sel.X.(*ast.Ident); ok {
		if repo, aliased := aliases[id.Name]; aliased {
			return repo, true
		}
	}
	return "", false
}

// memoryRepos are the *store.Conn repository fields that hold MEMORY /
// knowledge state — the claims, events, relationships, embeddings,
// outcomes, decisions, lessons, playbooks, incidents, feedback,
// cross-entity edges, entities, and the version chain — plus the DELETES
// thereof. Every mutating call on one of these MUST flow through the
// governed Writer; a direct call fails the guard.
//
// SCOPE DECISION (memory vs operational): the spec governs memory writes,
// not operational/auth bookkeeping. The auth + agent-registry repositories
// (Users, RevokedTokens, Agents) and the workflow-job repository (Jobs)
// are operational tables — API keys, JWT denylist, principal registry,
// and job lifecycle. Their writes (Create / UpdateStatus / UpdateScopes /
// UpdateAllowedRuns / Add / PurgeExpired / Upsert) are NOT memory writes,
// so they are EXCLUDED from the no-bypass rule by REPO NAME below, with
// this written rationale rather than by silently omitting method names.
// If a repo's classification is ever in doubt, move it to memoryRepos and
// govern it — governing an operational write is harmless; missing a
// memory write is the bug this guard exists to prevent.
var memoryRepos = map[string]bool{
	"Events": true, "Claims": true, "Relationships": true, "Embeddings": true,
	"Entities": true, "Actions": true, "Outcomes": true, "Lessons": true,
	"Decisions": true, "Playbooks": true, "EntityRels": true, "Incidents": true,
	"Feedback": true, "ClaimVersions": true,
}

// operationalRepos are the *store.Conn repository fields that hold
// operational / auth state, NOT memory. They are deliberately excluded
// from the no-bypass rule (see the SCOPE DECISION on memoryRepos). Listed
// explicitly — not silently omitted — so the exclusion is auditable and a
// reviewer can challenge it.
var operationalRepos = map[string]bool{
	// Auth: user identities + JWT denylist + non-human principal registry.
	"Users": true, "RevokedTokens": true, "Agents": true,
	// Workflow bookkeeping: compilation-job lifecycle state.
	"Jobs": true,
}

// storageRepos is the union — every *store.Conn repository field name.
// Used to distinguish a real repo call (conn.Claims.Upsert) from an
// unrelated method on some other receiver. classifyWrite then narrows to
// memoryRepos for the actual bypass check.
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
