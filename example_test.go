package mnemos_test

import (
	"context"
	"fmt"
	"time"

	"github.com/felixgeelhaar/chronos/embed"
	"github.com/felixgeelhaar/mnemos"
	"github.com/felixgeelhaar/mnemos/providers"

	// Tests use the in-memory storage provider.
	_ "github.com/felixgeelhaar/mnemos/internal/store/memory"
)

// ExampleNew demonstrates the simplest possible Mnemos usage: zero
// options, in-memory storage for the doc-example sandbox, and a single
// Remember + Recall roundtrip.
func ExampleNew() {
	mem, err := mnemos.New(
		mnemos.WithStorage("memory://?namespace=example_new"),
		mnemos.WithPassiveMode(),
	)
	if err != nil {
		fmt.Println("setup:", err)
		return
	}
	defer func() { _ = mem.Close() }()

	ctx := context.Background()
	if err := mem.Remember(ctx, mnemos.Item{
		Type:    "fact",
		Content: "The user prefers Go for backend work.",
	}); err != nil {
		fmt.Println("remember:", err)
		return
	}

	results, err := mem.Recall(ctx, mnemos.Query{Text: "user Go work"})
	if err != nil {
		fmt.Println("recall:", err)
		return
	}
	fmt.Println("results:", len(results) > 0)
	// Output: results: true
}

// ExampleNew_passive shows the passive (no-LLM) mode explicitly. This
// is the default; the With option is shown for clarity.
func ExampleNew_passive() {
	mem, _ := mnemos.New(
		mnemos.WithStorage("memory://?namespace=example_passive"),
		mnemos.WithPassiveMode(),
	)
	defer func() { _ = mem.Close() }()

	_ = mem.Remember(context.Background(), mnemos.Item{
		Type:    "decision",
		Content: "Adopt SQLite for local-first storage.",
	})
	// Output:
}

// ExampleNew_sharedProvider shows how to plug an agent runtime's
// model client into Mnemos via a thin TextGenerator adapter. The stub
// here returns a fixed JSON response — real adapters wrap the
// runtime's HTTP call.
func ExampleNew_sharedProvider() {
	tg := stubGen{response: `[{"text":"adopt go", "type":"decision", "confidence":0.9}]`}

	mem, _ := mnemos.New(
		mnemos.WithStorage("memory://?namespace=example_shared"),
		mnemos.WithSharedProvider(&tg, nil),
	)
	defer func() { _ = mem.Close() }()

	_ = mem.Remember(context.Background(), mnemos.Item{
		Type:    "decision",
		Content: fmt.Sprintf("doc-example shared %d", time.Now().UnixNano()),
	})
	// Output:
}

// ExampleNew_withChronos shows supplying a custom Chronos engine for
// durable temporal storage. The default mnemos.New() boots an
// in-memory Chronos automatically; pass WithChronos when you want
// shared state with another consumer or non-default detector config.
func ExampleNew_withChronos() {
	eng, _ := embed.New(embed.WithStorage("memory://?namespace=example_chronos"))
	defer func() { _ = eng.Close() }()

	mem, _ := mnemos.New(
		mnemos.WithStorage("memory://?namespace=example_chronos_mem"),
		mnemos.WithChronos(eng),
	)
	defer func() { _ = mem.Close() }()

	_ = mem.RememberEvent(context.Background(), mnemos.Event{
		At:      time.Now(),
		Type:    "deployment",
		Content: "shipped v2.3.0",
		RunID:   "release-cycle",
	})
	// Output:
}

type stubGen struct {
	response string
}

func (g *stubGen) GenerateText(
	_ context.Context,
	_ providers.GenerateTextInput,
) (providers.GenerateTextOutput, error) {
	return providers.GenerateTextOutput{
		Content: g.response,
		Model:   "stub-v1",
	}, nil
}
