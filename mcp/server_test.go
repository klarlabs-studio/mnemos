package mcp_test

import (
	"testing"

	"go.klarlabs.de/mnemos"
	mnemosmcp "go.klarlabs.de/mnemos/mcp"

	_ "go.klarlabs.de/mnemos/internal/store/memory"
)

func newStore(t *testing.T, ns string) mnemos.Store {
	t.Helper()
	t.Setenv("MNEMOS_DB_URL", "")
	store, err := mnemos.New(
		mnemos.WithStorage("memory://?namespace="+ns),
		mnemos.WithPassiveMode(),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// TestNewServer_BuildsAndExposesTools verifies the spec's
// mcp.NewServer(store) returns a usable MCP server with the core memory
// tools registered.
func TestNewServer_BuildsAndExposesTools(t *testing.T) {
	store := newStore(t, "mcp_build")

	srv := mnemosmcp.NewServer(store)
	if srv == nil {
		t.Fatal("NewServer returned nil")
	}

	tools := srv.Tools()
	if len(tools) == 0 {
		t.Fatal("expected the MCP server to register at least one tool")
	}
	want := map[string]bool{
		"remember": false,
		"recall":   false,
		"get":      false,
	}
	names := make([]string, 0, len(tools))
	for _, ti := range tools {
		names = append(names, ti.Name)
		if _, ok := want[ti.Name]; ok {
			want[ti.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("expected tool %q to be registered; got tools %v", name, names)
		}
	}
}
