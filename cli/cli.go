// Package cli is the optional CLI delivery adapter for a mnemos
// [mnemos.Store]. It provides an embeddable command dispatcher over the
// store's core operations for operator inspection and management.
//
// As the spec states, this delivery adapter is NOT part of the stable API
// — it is a packaging convenience over the stable [mnemos.Store] port.
// Every write still routes through the store's governed axi kernel, so
// the no-bypass guarantee holds.
//
// Unlike a process-level main, [App.Run] returns an error instead of
// exiting, so it can be embedded, tested, and composed.
//
//	store, _ := mnemos.New(mnemos.WithSQLite("./mnemos.db"))
//	app := cli.New(store, os.Stdout)
//	if err := app.Run(ctx, os.Args[1:]); err != nil { ... }
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"go.klarlabs.de/mnemos"
)

// App is an embeddable CLI over a [mnemos.Store]. Construct it with [New].
type App struct {
	store mnemos.Store
	out   io.Writer
}

// New returns an [App] that dispatches commands against store, writing
// human-readable output to out. When out is nil, output is discarded.
func New(store mnemos.Store, out io.Writer) *App {
	if out == nil {
		out = io.Discard
	}
	return &App{store: store, out: out}
}

// Run dispatches a single command from args (args[0] is the command, the
// rest are its arguments). It returns an error rather than exiting the
// process, so callers control termination.
//
// Commands:
//
//	remember --type T --source S <text...>     ingest text
//	claim --event <id> [--type T] <text...>    persist a claim with evidence
//	event --at RFC3339 --type T <content...>   append a temporal event
//	recall [--limit N] [--as-of T] [--recorded-as-of T] <text...>
//	get <claim-id>                             exact lookup
//	scan [--from T] [--until T] [--limit N]    valid-time range
//	timeline [--from T] [--to T] [--limit N]   chronological events
func (a *App) Run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("mnemos: no command given (try: remember, claim, event, recall, get, scan, timeline)")
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "remember":
		return a.remember(ctx, rest)
	case "claim":
		return a.claim(ctx, rest)
	case "event":
		return a.event(ctx, rest)
	case "recall":
		return a.recall(ctx, rest)
	case "get":
		return a.get(ctx, rest)
	case "scan":
		return a.scan(ctx, rest)
	case "timeline":
		return a.timeline(ctx, rest)
	default:
		return fmt.Errorf("mnemos: unknown command %q", cmd)
	}
}

func (a *App) remember(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("remember", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	typ := fs.String("type", "", "claim type")
	source := fs.String("source", "", "origin label")
	runID := fs.String("run", "", "run id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	content := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if content == "" {
		return fmt.Errorf("mnemos: remember: text is required")
	}
	if err := a.store.Remember(ctx, mnemos.Item{
		Type:    *typ,
		Content: content,
		Source:  *source,
		RunID:   *runID,
	}); err != nil {
		return err
	}
	a.println("remembered")
	return nil
}

func (a *App) claim(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("claim", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	typ := fs.String("type", "", "claim type")
	runID := fs.String("run", "", "run id")
	var events multiFlag
	fs.Var(&events, "event", "evidence event id (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	text := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if text == "" {
		return fmt.Errorf("mnemos: claim: text is required")
	}
	id, err := a.store.RememberClaim(ctx, mnemos.ClaimItem{
		Text:     text,
		Type:     *typ,
		EventIDs: events,
		RunID:    *runID,
	})
	if err != nil {
		return err
	}
	a.printf("recorded claim %s\n", id)
	return nil
}

func (a *App) event(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("event", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	at := fs.String("at", "", "RFC3339 timestamp")
	typ := fs.String("type", "", "event type")
	id := fs.String("id", "", "stable event id")
	runID := fs.String("run", "", "run id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	content := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if content == "" {
		return fmt.Errorf("mnemos: event: content is required")
	}
	when := time.Now()
	if *at != "" {
		t, err := time.Parse(time.RFC3339, *at)
		if err != nil {
			return fmt.Errorf("mnemos: event: --at must be RFC3339: %w", err)
		}
		when = t
	}
	if err := a.store.RememberEvent(ctx, mnemos.Event{
		ID:      *id,
		At:      when,
		Type:    *typ,
		Content: content,
		RunID:   *runID,
	}); err != nil {
		return err
	}
	a.println("remembered event")
	return nil
}

func (a *App) recall(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("recall", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	limit := fs.Int("limit", 0, "max results")
	runID := fs.String("run", "", "run scope")
	asOf := fs.String("as-of", "", "RFC3339 valid-time instant")
	recordedAsOf := fs.String("recorded-as-of", "", "RFC3339 transaction-time instant")
	if err := fs.Parse(args); err != nil {
		return err
	}
	text := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if text == "" {
		return fmt.Errorf("mnemos: recall: query text is required")
	}
	q := mnemos.Query{Text: text, Limit: *limit, RunID: *runID}
	if *asOf != "" {
		t, err := time.Parse(time.RFC3339, *asOf)
		if err != nil {
			return fmt.Errorf("mnemos: recall: --as-of must be RFC3339: %w", err)
		}
		q.AsOf = t
	}
	if *recordedAsOf != "" {
		t, err := time.Parse(time.RFC3339, *recordedAsOf)
		if err != nil {
			return fmt.Errorf("mnemos: recall: --recorded-as-of must be RFC3339: %w", err)
		}
		q.RecordedAsOf = t
	}
	results, err := a.store.Recall(ctx, q)
	if err != nil {
		return err
	}
	if len(results) == 0 {
		a.println("(no results)")
		return nil
	}
	for _, r := range results {
		a.printf("%s\t%.2f\t%s\n", r.ClaimID, r.TrustScore, r.Text)
	}
	return nil
}

func (a *App) get(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("mnemos: get: claim id is required")
	}
	c, err := a.store.Get(ctx, args[0])
	if err != nil {
		return err
	}
	a.printf("%s\t%s\t%s\n", c.ID, c.Type, c.Statement)
	return nil
}

func (a *App) scan(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	from := fs.String("from", "", "RFC3339 lower bound")
	until := fs.String("until", "", "RFC3339 upper bound")
	limit := fs.Int("limit", 0, "max results")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var q mnemos.ScanQuery
	q.Limit = *limit
	if *from != "" {
		t, err := time.Parse(time.RFC3339, *from)
		if err != nil {
			return fmt.Errorf("mnemos: scan: --from must be RFC3339: %w", err)
		}
		q.ValidFrom = t
	}
	if *until != "" {
		t, err := time.Parse(time.RFC3339, *until)
		if err != nil {
			return fmt.Errorf("mnemos: scan: --until must be RFC3339: %w", err)
		}
		q.ValidUntil = t
	}
	claims, err := a.store.Scan(ctx, q)
	if err != nil {
		return err
	}
	for _, c := range claims {
		a.printf("%s\t%s\t%s\n", c.ID, c.ValidFrom.UTC().Format(time.RFC3339), c.Statement)
	}
	return nil
}

func (a *App) timeline(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("timeline", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	from := fs.String("from", "", "RFC3339 lower bound")
	to := fs.String("to", "", "RFC3339 upper bound")
	runID := fs.String("run", "", "run scope")
	limit := fs.Int("limit", 0, "max results")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var q mnemos.TimelineQuery
	q.Limit = *limit
	q.RunID = *runID
	if *from != "" {
		t, err := time.Parse(time.RFC3339, *from)
		if err != nil {
			return fmt.Errorf("mnemos: timeline: --from must be RFC3339: %w", err)
		}
		q.From = t
	}
	if *to != "" {
		t, err := time.Parse(time.RFC3339, *to)
		if err != nil {
			return fmt.Errorf("mnemos: timeline: --to must be RFC3339: %w", err)
		}
		q.To = t
	}
	events, err := a.store.Timeline(ctx, q)
	if err != nil {
		return err
	}
	for _, e := range events {
		a.printf("%s\t%s\t%s\n", e.At.UTC().Format(time.RFC3339), e.Type, e.Content)
	}
	return nil
}

// println writes a line to the App's output sink. Write errors on a
// human-output sink are non-actionable for a CLI, so they are discarded.
func (a *App) println(s string) {
	_, _ = fmt.Fprintln(a.out, s)
}

// printf writes formatted output to the App's output sink. Write errors
// are discarded for the same reason as [App.println].
func (a *App) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(a.out, format, args...)
}

// multiFlag collects a repeatable string flag (e.g. --event a --event b).
type multiFlag []string

// String renders the collected values as a comma-joined list.
func (m *multiFlag) String() string { return strings.Join(*m, ",") }

// Set appends one occurrence of the flag's value.
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}
