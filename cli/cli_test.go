package cli_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"go.klarlabs.de/mnemos"
	"go.klarlabs.de/mnemos/cli"

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

// TestRun_RememberThenRecall drives the CLI dispatcher over the store:
// remember a fact, then recall it, asserting output flows to the writer.
func TestRun_RememberThenRecall(t *testing.T) {
	store := newStore(t, "cli_roundtrip")
	ctx := context.Background()

	var out bytes.Buffer
	app := cli.New(store, &out)

	if err := app.Run(ctx, []string{"remember", "--type", "fact", "The user prefers Go for backend."}); err != nil {
		t.Fatalf("remember: %v", err)
	}

	out.Reset()
	if err := app.Run(ctx, []string{"recall", "user Go backend"}); err != nil {
		t.Fatalf("recall: %v", err)
	}
	if !strings.Contains(strings.ToLower(out.String()), "go") {
		t.Errorf("recall output did not mention the remembered fact: %q", out.String())
	}
}

// TestRun_UnknownCommand returns an error rather than exiting the process.
func TestRun_UnknownCommand(t *testing.T) {
	store := newStore(t, "cli_unknown")
	var out bytes.Buffer
	app := cli.New(store, &out)
	if err := app.Run(context.Background(), []string{"frobnicate"}); err == nil {
		t.Fatal("expected error for unknown command")
	}
}

// TestRun_NoArgs returns a usage error.
func TestRun_NoArgs(t *testing.T) {
	store := newStore(t, "cli_noargs")
	var out bytes.Buffer
	app := cli.New(store, &out)
	if err := app.Run(context.Background(), nil); err == nil {
		t.Fatal("expected error for no command")
	}
}
