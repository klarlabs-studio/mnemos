package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"go.klarlabs.de/mcp/protocol"

	"go.klarlabs.de/mnemos/client"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
)

func (f runScopeFixture) declareDependency(t *testing.T, episodeID, token string, body any) *http.Response {
	t.Helper()
	return postJSON(t, f.srv.URL+"/v1/episodes/"+episodeID+"/dependencies", body, map[string]string{"Authorization": "Bearer " + token})
}

func decodeBody[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	var out T
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

// REST declares, lists and removes a dependency with the same documents the
// CLI and MCP paths produce, attributing each change to the token's subject.
func TestREST_EpisodeDependencies_ShareTheCLIReports(t *testing.T) {
	f := newRunScopeFixture(t)
	f.seedRun(t, "run-a", "ev-src", "cl-src")
	f.seedRun(t, "run-a", "ev-copy", "cl-copy")
	tok := f.openToken(t, []string{domain.ScopeClaimsWrite})

	resp := f.declareDependency(t, "ev-copy", tok, map[string]string{"depends_on": "ev-src", "kind": "copies", "note": "reposted"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("declare: status = %d, want 201", resp.StatusCode)
	}
	declared := decodeBody[dependencyChangeView](t, resp)
	if d := declared.Dependency; d.EpisodeID != "ev-copy" || d.DependsOn != "ev-src" || d.Kind != "copies" || d.Signal != "declared" ||
		d.Reference != "reposted" || d.CreatedBy != "agt_open" {
		t.Errorf("declared = %+v", d)
	}
	if !reflect.DeepEqual(declared.RescoredBeliefs, []string{"cl-copy"}) || declared.SourceRetracted {
		t.Errorf("declared change = %+v", declared)
	}

	list := decodeBody[dependencyListView](t, f.do(t, http.MethodGet, "/v1/episodes/ev-src/dependencies", tok))
	viaCLI, err := runDependencyList(context.Background(), f.conn, "ev-src", false, nil)
	if err != nil {
		t.Fatalf("CLI list: %v", err)
	}
	if !reflect.DeepEqual(list, viaCLI) || len(list.Dependents) != 1 {
		t.Errorf("REST listing %+v differs from the CLI listing %+v", list, viaCLI)
	}

	resp = f.do(t, http.MethodDelete, "/v1/episodes/ev-copy/dependencies/ev-src", tok)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("remove: status = %d, want 200", resp.StatusCode)
	}
	removed := decodeBody[dependencyChangeView](t, resp)
	if removed.Dependency.RemovedBy != "agt_open" || removed.Dependency.RemovedAt == "" {
		t.Errorf("removed = %+v", removed.Dependency)
	}
	if got := decodeBody[dependencyListView](t, f.do(t, http.MethodGet, "/v1/episodes/ev-src/dependencies", tok)); len(got.Dependents) != 0 {
		t.Errorf("live dependents after removal = %+v", got.Dependents)
	}
	audit := decodeBody[dependencyListView](t, f.do(t, http.MethodGet, "/v1/episodes/ev-src/dependencies?include_removed=true", tok))
	if len(audit.Dependents) != 1 || audit.Dependents[0].RemovedBy != "agt_open" {
		t.Errorf("audit listing = %+v, want the tombstone", audit.Dependents)
	}

	// The CLI path sees the same state: the pair is gone, so removing again is
	// not found, and declaring revives it.
	gw, err := govwrite.Wrap(f.conn, nil)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	if _, err := runDependencyRemove(context.Background(), gw, "ev-copy", "ev-src", "", "operator", nil); err == nil {
		t.Error("CLI removal of an already removed dependency succeeded")
	}
	revived, err := runDependencyDeclare(context.Background(), gw, "ev-copy", "ev-src", "cites", "", "operator", nil)
	if err != nil || revived.Dependency.RemovedAt != "" || revived.Dependency.CreatedBy != "operator" {
		t.Errorf("CLI revival = %+v (%v)", revived, err)
	}
}

func TestREST_EpisodeDependencies_RefusalsAreClean(t *testing.T) {
	f := newRunScopeFixture(t)
	f.seedRun(t, "run-a", "ev-a", "cl-a")
	f.seedRun(t, "run-a", "ev-a2", "cl-a2")
	f.seedRun(t, "run-b", "ev-b", "cl-b")
	write := f.openToken(t, []string{domain.ScopeClaimsWrite})
	read := f.openToken(t, []string{"claims:read"})
	scoped := f.runScopedToken(t, []string{domain.ScopeClaimsWrite}, []string{"run-a"})
	if resp := f.declareDependency(t, "ev-a2", write, map[string]string{"depends_on": "ev-a", "kind": "cites"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("seed declaration: status = %d", resp.StatusCode)
	}

	declare := []struct {
		name    string
		episode string
		token   string
		body    any
		want    int
	}{
		{"read-only token", "ev-a", read, map[string]string{"depends_on": "ev-a2", "kind": "cites"}, http.StatusForbidden},
		{"unknown episode", "ev-missing", write, map[string]string{"depends_on": "ev-a", "kind": "cites"}, http.StatusNotFound},
		{"unknown source", "ev-a", write, map[string]string{"depends_on": "ev-missing", "kind": "cites"}, http.StatusNotFound},
		{"source in another run", "ev-a", scoped, map[string]string{"depends_on": "ev-b", "kind": "cites"}, http.StatusNotFound},
		{"bad kind", "ev-a", write, map[string]string{"depends_on": "ev-a2", "kind": "mentions"}, http.StatusBadRequest},
		{"self", "ev-a", write, map[string]string{"depends_on": "ev-a", "kind": "cites"}, http.StatusBadRequest},
		{"unknown field", "ev-a", write, map[string]string{"depends_on": "ev-a2", "kind": "cites", "why": "x"}, http.StatusBadRequest},
		{"live pair", "ev-a2", write, map[string]string{"depends_on": "ev-a", "kind": "copies"}, http.StatusConflict},
	}
	for _, tc := range declare {
		resp := f.declareDependency(t, tc.episode, tc.token, tc.body)
		_ = resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("declare, %s: status = %d, want %d", tc.name, resp.StatusCode, tc.want)
		}
	}
	other := []struct {
		name, method, path, token string
		want                      int
	}{
		{"remove with a read-only token", http.MethodDelete, "/v1/episodes/ev-a2/dependencies/ev-a", read, http.StatusForbidden},
		{"remove a pair never declared", http.MethodDelete, "/v1/episodes/ev-a/dependencies/ev-a2", write, http.StatusNotFound},
		{"list an episode in another run", http.MethodGet, "/v1/episodes/ev-b/dependencies", scoped, http.StatusNotFound},
		{"list an unknown episode", http.MethodGet, "/v1/episodes/ev-missing/dependencies", read, http.StatusNotFound},
		{"list with a bad flag", http.MethodGet, "/v1/episodes/ev-a/dependencies?include_removed=maybe", read, http.StatusBadRequest},
		{"PUT", http.MethodPut, "/v1/episodes/ev-a/dependencies", write, http.StatusMethodNotAllowed},
		{"GET one pair", http.MethodGet, "/v1/episodes/ev-a2/dependencies/ev-a", write, http.StatusMethodNotAllowed},
		{"third segment elsewhere", http.MethodPost, "/v1/episodes/ev-a/retract/x", write, http.StatusNotFound},
	}
	for _, tc := range other {
		if got := f.status(t, tc.method, tc.path, tc.token); got != tc.want {
			t.Errorf("%s: status = %d, want %d", tc.name, got, tc.want)
		}
	}
	if got := decodeBody[dependencyListView](t, f.do(t, http.MethodGet, "/v1/episodes/ev-a/dependencies", read)); len(got.Dependents) != 1 {
		t.Errorf("dependents after refused writes = %+v, want the one seeded", got.Dependents)
	}
}

// The Go client against a real `serve` over SQLite.
func TestClient_EventDependencies_AgainstServe(t *testing.T) {
	f := newRunScopeFixture(t)
	f.seedRun(t, "run-a", "ev-src", "cl-src")
	f.seedRun(t, "run-a", "ev-copy", "cl-copy")
	ctx := context.Background()
	var apiErr *client.APIError

	reader := client.New(f.srv.URL, client.WithToken(f.openToken(t, []string{"claims:read"})))
	if _, err := reader.DeclareEventDependency(ctx, "ev-copy", "ev-src", "copies", ""); !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
		t.Fatalf("claims:read declare: err = %v, want 403", err)
	}
	writer := client.New(f.srv.URL, client.WithToken(f.openToken(t, []string{domain.ScopeClaimsWrite})))
	got, err := writer.DeclareEventDependency(ctx, "ev-copy", "ev-src", "copies", "a repost")
	if err != nil {
		t.Fatalf("DeclareEventDependency: %v", err)
	}
	if got.Dependency.Signal != "declared" || got.Dependency.Reference != "a repost" || !reflect.DeepEqual(got.RescoredClaims, []string{"cl-copy"}) {
		t.Errorf("declared = %+v", got)
	}
	if _, err := writer.DeclareEventDependency(ctx, "ev-copy", "ev-src", "cites", ""); !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict {
		t.Errorf("second declaration: err = %v, want 409", err)
	}
	list, err := reader.EventDependencies(ctx, "ev-copy", false)
	if err != nil || len(list.DependsOn) != 1 || list.DependsOn[0].DependsOn != "ev-src" {
		t.Fatalf("EventDependencies = %+v (%v)", list, err)
	}
	stored, err := f.conn.Dependencies.ListByEventIDs(ctx, []string{"ev-copy"})
	if err != nil || len(stored) != 1 {
		t.Fatalf("stored: %v", err)
	}
	if at, err := client.ParseTime(list.DependsOn[0].CreatedAt); err != nil || !at.Equal(stored[0].CreatedAt) {
		t.Errorf("client created_at %q differs from stored %v (%v)", list.DependsOn[0].CreatedAt, stored[0].CreatedAt, err)
	}
	removed, err := writer.RemoveEventDependency(ctx, "ev-copy", "ev-src", "")
	if err != nil || removed.Dependency.RemovedAt == "" {
		t.Fatalf("RemoveEventDependency = %+v (%v)", removed, err)
	}
	if _, err := writer.RemoveEventDependency(ctx, "ev-copy", "ev-src", ""); !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Errorf("second removal: err = %v, want 404", err)
	}
	if audit, err := reader.EventDependencies(ctx, "ev-copy", true); err != nil || len(audit.DependsOn) != 1 || audit.DependsOn[0].RemovedAt == "" {
		t.Errorf("audit = %+v (%v)", audit, err)
	}
}

// The MCP tools and the CLI share one call path: what one writes, the other
// reads, with the same documents.
func TestEpisodeDependencies_MCPAndCLIShareOneOperation(t *testing.T) {
	setIsolatedDB(t)
	ctx := context.Background()
	conn, err := openConn(ctx)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { closeConn(conn) })
	f := runScopeFixture{conn: conn}
	f.seedRun(t, "run-a", "ev-src", "cl-src")
	f.seedRun(t, "run-a", "ev-copy", "cl-copy")

	viaMCP, err := mcpRunDeclareEpisodeDependency(ctx, "agent-1", mcpDeclareEpisodeDependencyInput{EpisodeID: "ev-copy", DependsOn: "ev-src", Kind: "derived_from"})
	if err != nil {
		t.Fatalf("declare_episode_dependency: %v", err)
	}
	if viaMCP.Dependency.CreatedBy != "agent-1" || viaMCP.Dependency.Kind != "derived_from" {
		t.Errorf("MCP change = %+v", viaMCP)
	}
	listMCP, err := mcpRunEpisodeDependencies(ctx, mcpEpisodeDependenciesInput{EpisodeID: "ev-copy"})
	if err != nil {
		t.Fatalf("episode_dependencies: %v", err)
	}
	listCLI, err := runDependencyList(ctx, f.conn, "ev-copy", false, nil)
	if err != nil || !reflect.DeepEqual(listMCP, listCLI) {
		t.Errorf("MCP listing %+v differs from CLI listing %+v (%v)", listMCP, listCLI, err)
	}
	if _, err := mcpRunRemoveEpisodeDependency(ctx, "agent-2", mcpRemoveEpisodeDependencyInput{EpisodeID: "ev-copy", DependsOn: "ev-src"}); err != nil {
		t.Fatalf("remove_episode_dependency: %v", err)
	}
	all, err := runDependencyList(ctx, f.conn, "ev-copy", true, nil)
	if err != nil || len(all.DependsOn) != 1 || all.DependsOn[0].RemovedBy != "agent-2" {
		t.Errorf("listing after MCP removal = %+v (%v)", all, err)
	}
}

func TestParseDependenciesArgs(t *testing.T) {
	ok := []struct {
		args []string
		want dependenciesArgs
	}{
		{[]string{"ev"}, dependenciesArgs{id: "ev"}},
		{[]string{"ev", "--include-removed"}, dependenciesArgs{id: "ev", includeRemoved: true}},
		{[]string{"ev", "--declare", "src", "--kind=cites", "--note", "why"}, dependenciesArgs{id: "ev", declare: "src", kind: "cites", note: "why"}},
		{[]string{"--remove=src", "ev"}, dependenciesArgs{id: "ev", remove: "src"}},
		{[]string{"ev", "--remove", "src", "--reason", "not a copy"}, dependenciesArgs{id: "ev", remove: "src", reason: "not a copy"}},
	}
	for _, tc := range ok {
		got, err := parseDependenciesArgs(tc.args)
		if err != nil || got != tc.want {
			t.Errorf("%v: %+v (%v), want %+v", tc.args, got, err, tc.want)
		}
	}
	for _, bad := range [][]string{
		{}, {"ev", "other"}, {"ev", "--declare", "src"}, {"ev", "--declare", "a", "--remove", "b", "--kind", "cites"},
		{"ev", "--kind", "cites"}, {"ev", "--remove", "src", "--include-removed"}, {"ev", "--declare"}, {"ev", "--bogus"},
		{"ev", "--reason", "why"}, {"ev", "--declare", "src", "--kind", "cites", "--reason", "why"},
	} {
		if _, err := parseDependenciesArgs(bad); err == nil {
			t.Errorf("%v: accepted", bad)
		}
	}
}

// A removal carries an optional reason on every daemon surface, the
// include_removed listing shows it, and a revival keeps the removed interval:
// the listing then has one entry per interval, the removal included.
func TestEpisodeDependencies_RemovalReasonAndHistory(t *testing.T) {
	f := newRunScopeFixture(t)
	f.seedRun(t, "run-a", "ev-src", "cl-src")
	f.seedRun(t, "run-a", "ev-copy", "cl-copy")
	f.seedRun(t, "run-a", "ev-third", "cl-third")
	tok := f.openToken(t, []string{domain.ScopeClaimsWrite})
	ctx := context.Background()
	declare := func(episode, source string) {
		t.Helper()
		resp := f.declareDependency(t, episode, tok, map[string]string{"depends_on": source, "kind": "copies"})
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("declare %s -> %s: status = %d", episode, source, resp.StatusCode)
		}
	}
	remove := func(path string, body any) *http.Response {
		t.Helper()
		var reader *bytes.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			reader = bytes.NewReader(raw)
		} else {
			reader = bytes.NewReader(nil)
		}
		req, err := http.NewRequest(http.MethodDelete, f.srv.URL+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	declare("ev-copy", "ev-src")
	long := strings.Repeat("é", domain.MaxDependencyRemovalReasonLen+1)
	for _, tc := range []struct {
		name, path string
		body       any
	}{
		{"an over-long reason", "/v1/episodes/ev-copy/dependencies/ev-src", map[string]string{"reason": long}},
		{"a reason in both places", "/v1/episodes/ev-copy/dependencies/ev-src?reason=a", map[string]string{"reason": "b"}},
		{"an unknown body field", "/v1/episodes/ev-copy/dependencies/ev-src", map[string]string{"why": "x"}},
	} {
		resp := remove(tc.path, tc.body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("remove with %s: status = %d, want 400", tc.name, resp.StatusCode)
		}
	}
	resp := remove("/v1/episodes/ev-copy/dependencies/ev-src", map[string]string{"reason": "it only quotes the title"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("remove with a body reason: status = %d", resp.StatusCode)
	}
	if got := decodeBody[dependencyChangeView](t, resp); got.Dependency.RemovalReason != "it only quotes the title" {
		t.Errorf("removed = %+v, want the reason", got.Dependency)
	}

	// Declared again, then removed with a query reason: two intervals.
	declare("ev-copy", "ev-src")
	resp = remove("/v1/episodes/ev-copy/dependencies/ev-src?reason=wrong+again", nil)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("remove with a query reason: status = %d", resp.StatusCode)
	}
	audit := decodeBody[dependencyListView](t, f.do(t, http.MethodGet, "/v1/episodes/ev-copy/dependencies?include_removed=true", tok))
	var reasons []string
	for _, d := range audit.DependsOn {
		reasons = append(reasons, d.RemovalReason)
	}
	if !reflect.DeepEqual(reasons, []string{"it only quotes the title", "wrong again"}) {
		t.Errorf("audit reasons = %q, want both removals, oldest first", reasons)
	}
	viaCLI, err := runDependencyList(ctx, f.conn, "ev-copy", true, nil)
	if err != nil || !reflect.DeepEqual(audit, viaCLI) {
		t.Errorf("REST audit %+v differs from the CLI audit %+v (%v)", audit, viaCLI, err)
	}

	// The Go client sends the reason in the body.
	writer := client.New(f.srv.URL, client.WithToken(tok))
	declare("ev-third", "ev-src")
	got, err := writer.RemoveEventDependency(ctx, "ev-third", "ev-src", "a coincidence")
	if err != nil || got.Dependency.RemovalReason != "a coincidence" {
		t.Errorf("client removal = %+v (%v)", got, err)
	}
	var apiErr *client.APIError
	declare("ev-third", "ev-src")
	if _, err := writer.RemoveEventDependency(ctx, "ev-third", "ev-src", long); !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		t.Errorf("client removal with an over-long reason: err = %v, want 400", err)
	}
	list, err := writer.EventDependencies(ctx, "ev-third", true)
	if err != nil || len(list.DependsOn) != 2 || list.DependsOn[0].RemovalReason != "a coincidence" || list.DependsOn[1].RemovedAt != "" {
		t.Errorf("client audit = %+v (%v), want the removed interval and the live one", list, err)
	}
}

// The MCP tool and the CLI take the reason too, and an over-long one is the
// caller's to fix (-32602).
func TestEpisodeDependencies_MCPAndCLIRemovalReason(t *testing.T) {
	setIsolatedDB(t)
	ctx := context.Background()
	conn, err := openConn(ctx)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { closeConn(conn) })
	f := runScopeFixture{conn: conn}
	f.seedRun(t, "run-a", "ev-src", "cl-src")
	f.seedRun(t, "run-a", "ev-copy", "cl-copy")
	if _, err := mcpRunDeclareEpisodeDependency(ctx, "agent-1", mcpDeclareEpisodeDependencyInput{EpisodeID: "ev-copy", DependsOn: "ev-src", Kind: "cites"}); err != nil {
		t.Fatalf("declare: %v", err)
	}
	var pe *protocol.Error
	if _, err := mcpRunRemoveEpisodeDependency(ctx, "agent-2", mcpRemoveEpisodeDependencyInput{
		EpisodeID: "ev-copy", DependsOn: "ev-src", Reason: strings.Repeat("x", domain.MaxDependencyRemovalReasonLen+1),
	}); !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidParams {
		t.Errorf("over-long reason: err = %v, want -32602", err)
	}
	removed, err := mcpRunRemoveEpisodeDependency(ctx, "agent-2", mcpRemoveEpisodeDependencyInput{EpisodeID: "ev-copy", DependsOn: "ev-src", Reason: "  not a citation  "})
	if err != nil || removed.Dependency.RemovalReason != "not a citation" {
		t.Fatalf("remove_episode_dependency = %+v (%v)", removed, err)
	}
	listMCP, err := mcpRunEpisodeDependencies(ctx, mcpEpisodeDependenciesInput{EpisodeID: "ev-copy", IncludeRemoved: true})
	if err != nil || len(listMCP.DependsOn) != 1 || listMCP.DependsOn[0].RemovalReason != "not a citation" {
		t.Errorf("episode_dependencies with removed = %+v (%v)", listMCP, err)
	}
}
