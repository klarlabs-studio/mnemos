package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"go.klarlabs.de/mnemos/internal/dependency"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/store"
	"go.klarlabs.de/mnemos/internal/workflow"
)

// Editing source dependence between episodes on the CLI and over MCP (ADR
// 0038). Every surface calls internal/dependency (List, Declare, Remove),
// which validates the request once and, for the writes, calls govwrite's one
// implementation; the reports render through dependency.ListReport and
// dependency.ChangeReport, so `mnemos dependencies --json`, the MCP tools and
// REST return the same documents.

type (
	dependencyListView   = dependency.ListReport
	dependencyChangeView = dependency.ChangeReport
)

// runDependencyList is the one call path from a daemon surface to the listing.
// runAllowed, when non-nil, confines the caller to its runs.
func runDependencyList(ctx context.Context, conn *store.Conn, eventID string, includeRemoved bool, runAllowed func(string) bool) (dependencyListView, error) {
	l, err := dependency.List(ctx, conn.Events, conn.Dependencies, dependency.ListRequest{
		EventID: eventID, IncludeRemoved: includeRemoved, RunAllowed: runAllowed,
	})
	if err != nil {
		return dependencyListView{}, err
	}
	return dependency.NewListReport(l), nil
}

// runDependencyDeclare is the one call path from a daemon surface to a
// declaration.
func runDependencyDeclare(ctx context.Context, w *govwrite.Writer, eventID, dependsOn, kind, note, actor string, runAllowed func(string) bool) (dependencyChangeView, error) {
	conn := w.Conn()
	change, err := dependency.Declare(ctx, conn.Events, conn.Dependencies, w.DeclareEventDependency, dependency.DeclareRequest{
		EventID: eventID, DependsOn: dependsOn, Kind: kind, Reference: note, Actor: actor, RunAllowed: runAllowed,
	})
	if err != nil {
		return dependencyChangeView{}, err
	}
	return dependency.NewChangeReport(change), nil
}

// runDependencyRemove is the one call path from a daemon surface to a removal.
func runDependencyRemove(ctx context.Context, w *govwrite.Writer, eventID, dependsOn, reason, actor string, runAllowed func(string) bool) (dependencyChangeView, error) {
	conn := w.Conn()
	change, err := dependency.Remove(ctx, conn.Events, conn.Dependencies, w.RemoveEventDependency, dependency.RemoveRequest{
		EventID: eventID, DependsOn: dependsOn, Reason: reason, Actor: actor, RunAllowed: runAllowed,
	})
	if err != nil {
		return dependencyChangeView{}, err
	}
	return dependency.NewChangeReport(change), nil
}

// --- MCP ---

type mcpEpisodeDependenciesInput struct {
	EpisodeID      string `json:"episode_id" jsonschema:"required,description=ID of the episode (event) whose source dependencies to list"`
	IncludeRemoved bool   `json:"include_removed,omitempty" jsonschema:"description=Also list dependencies removed by hand, with who removed them, when and why, and every earlier interval of a dependency declared again"`
}

type mcpDeclareEpisodeDependencyInput struct {
	EpisodeID string `json:"episode_id" jsonschema:"required,description=ID of the dependent episode: the one that cites, copies or derives from the source"`
	DependsOn string `json:"depends_on" jsonschema:"required,description=ID of the source episode it depends on"`
	Kind      string `json:"kind" jsonschema:"required,enum=cites,enum=copies,enum=derived_from,description=How the episode depends on the source"`
	Note      string `json:"note,omitempty" jsonschema:"description=Why (at most 512 characters); stored as the dependency's reference"`
}

type mcpRemoveEpisodeDependencyInput struct {
	EpisodeID string `json:"episode_id" jsonschema:"required,description=ID of the dependent episode"`
	DependsOn string `json:"depends_on" jsonschema:"required,description=ID of the source episode whose dependency to remove"`
	Reason    string `json:"reason,omitempty" jsonschema:"description=Why it is wrong (at most 512 characters); stored with the removal"`
}

func mcpRunEpisodeDependencies(ctx context.Context, input mcpEpisodeDependenciesInput) (dependencyListView, error) {
	conn, err := openConn(ctx)
	if err != nil {
		return dependencyListView{}, err
	}
	defer closeConn(conn)
	view, err := runDependencyList(ctx, conn, input.EpisodeID, input.IncludeRemoved, func(runID string) bool {
		return mcpRunAllowed(ctx, runID)
	})
	return view, mcpDependencyError(err, input.EpisodeID, "")
}

func mcpRunDeclareEpisodeDependency(ctx context.Context, actor string, input mcpDeclareEpisodeDependencyInput) (dependencyChangeView, error) {
	gw, err := openWriter(ctx)
	if err != nil {
		return dependencyChangeView{}, err
	}
	defer closeWriter(gw)
	view, err := runDependencyDeclare(ctx, gw, input.EpisodeID, input.DependsOn, input.Kind, input.Note, actor, func(runID string) bool {
		return mcpRunAllowed(ctx, runID)
	})
	return view, mcpDependencyError(err, input.EpisodeID, input.DependsOn)
}

func mcpRunRemoveEpisodeDependency(ctx context.Context, actor string, input mcpRemoveEpisodeDependencyInput) (dependencyChangeView, error) {
	gw, err := openWriter(ctx)
	if err != nil {
		return dependencyChangeView{}, err
	}
	defer closeWriter(gw)
	view, err := runDependencyRemove(ctx, gw, input.EpisodeID, input.DependsOn, input.Reason, actor, func(runID string) bool {
		return mcpRunAllowed(ctx, runID)
	})
	return view, mcpDependencyError(err, input.EpisodeID, input.DependsOn)
}

// --- CLI ---

const dependenciesUsage = "  mnemos dependencies <episode-id> [--include-removed]\n" +
	"  mnemos dependencies <episode-id> --declare <source-id> --kind cites|copies|derived_from [--note <text>]\n" +
	"  mnemos dependencies <episode-id> --remove <source-id> [--reason <text>]"

type dependenciesArgs struct {
	id, declare, remove, kind, note, reason string
	includeRemoved                          bool
}

func parseDependenciesArgs(args []string) (dependenciesArgs, error) {
	var a dependenciesArgs
	value := func(i *int, name string) (string, error) {
		arg := args[*i]
		if v, ok := strings.CutPrefix(arg, name+"="); ok {
			return v, nil
		}
		if *i+1 >= len(args) {
			return "", NewUserError("%s requires a value\n%s", name, dependenciesUsage)
		}
		*i++
		return args[*i], nil
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, _, _ := strings.Cut(arg, "=")
		var err error
		switch {
		case name == "--declare":
			a.declare, err = value(&i, name)
		case name == "--remove":
			a.remove, err = value(&i, name)
		case name == "--kind":
			a.kind, err = value(&i, name)
		case name == "--note":
			a.note, err = value(&i, name)
		case name == "--reason":
			a.reason, err = value(&i, name)
		case arg == "--include-removed":
			a.includeRemoved = true
		case strings.HasPrefix(arg, "-"):
			return a, NewUserError("unknown argument %q for dependencies\n%s", arg, dependenciesUsage)
		case a.id == "":
			a.id = arg
		default:
			return a, NewUserError("dependencies takes one episode id\n%s", dependenciesUsage)
		}
		if err != nil {
			return a, err
		}
	}
	switch {
	case a.id == "":
		return a, NewUserError("dependencies requires an episode id\n%s", dependenciesUsage)
	case a.declare != "" && a.remove != "":
		return a, NewUserError("--declare and --remove cannot be combined\n%s", dependenciesUsage)
	case a.declare == "" && (a.kind != "" || a.note != ""):
		return a, NewUserError("--kind and --note go with --declare\n%s", dependenciesUsage)
	case a.remove == "" && a.reason != "":
		return a, NewUserError("--reason goes with --remove\n%s", dependenciesUsage)
	case a.declare != "" && a.kind == "":
		return a, NewUserError("--declare requires --kind cites|copies|derived_from\n%s", dependenciesUsage)
	case a.includeRemoved && (a.declare != "" || a.remove != ""):
		return a, NewUserError("--include-removed lists; it does not go with --declare or --remove\n%s", dependenciesUsage)
	}
	return a, nil
}

// handleDependencies implements `mnemos dependencies`.
func handleDependencies(args []string, f Flags) {
	a, err := parseDependenciesArgs(args)
	if err != nil {
		exitWithMnemosError(false, err)
		return
	}
	if a.declare == "" && a.remove == "" {
		exitWithMnemosError(f.Verbose, listDependencies(a, f))
		return
	}
	err = runJob("dependencies", map[string]string{"id": a.id}, f.Verbose, func(ctx context.Context, _ *workflow.Job, w *govwrite.Writer) error {
		actor, err := resolveActor(ctx, w.Conn().Users, f.Actor)
		if err != nil {
			return NewUserError("%v", err)
		}
		var view dependencyChangeView
		if a.declare != "" {
			view, err = runDependencyDeclare(ctx, w, a.id, a.declare, a.kind, a.note, actor, nil)
		} else {
			view, err = runDependencyRemove(ctx, w, a.id, a.remove, a.reason, actor, nil)
		}
		if err := dependencyCLIError(err, a); err != nil {
			return err
		}
		if f.JSON {
			return printJSON(view)
		}
		printDependencyChange(view)
		return nil
	})
	exitWithMnemosError(f.Verbose, err)
}

func listDependencies(a dependenciesArgs, f Flags) error {
	ctx := context.Background()
	conn, err := openConn(ctx)
	if err != nil {
		return NewSystemError(err, "open the store")
	}
	defer closeConn(conn)
	view, err := runDependencyList(ctx, conn, a.id, a.includeRemoved, nil)
	if err := dependencyCLIError(err, a); err != nil {
		return err
	}
	if f.JSON {
		return printJSON(view)
	}
	printDependencyList(view)
	return nil
}

func dependencyCLIError(err error, a dependenciesArgs) error {
	if err == nil {
		return nil
	}
	if msg, ok := dependencyRefusal(err, a.id, a.declare+a.remove); ok {
		return NewUserError("%s", msg)
	}
	return NewSystemError(err, "episode dependencies of %s", a.id)
}

// dependencyRefusal is the caller-facing message for a request the caller
// can fix, and false for a fault. A conflict reads the same whether the
// check in front of the governed write caught it or the store refused the
// write itself, because a concurrent edit landed in between: the governed
// path keeps the store's sentinel (kernel.ExecuteWithCause).
func dependencyRefusal(err error, eventID, source string) (string, bool) {
	switch {
	case errors.Is(err, dependency.ErrEventNotFound),
		errors.Is(err, dependency.ErrInvalid), errors.Is(err, dependency.ErrEventIDRequired):
		return err.Error(), true
	case errors.Is(err, dependency.ErrExists):
		return fmt.Sprintf("episode %s already depends on %s; remove it first to change its kind or note", eventID, source), true
	case errors.Is(err, dependency.ErrNotFound):
		return fmt.Sprintf("episode %s has no live dependency on %s", eventID, source), true
	default:
		return "", false
	}
}

// mcpDependencyError returns a refusal as -32602 with its message (#355),
// and leaves a fault opaque.
func mcpDependencyError(err error, eventID, source string) error {
	if err == nil {
		return nil
	}
	if msg, ok := dependencyRefusal(err, strings.TrimSpace(eventID), strings.TrimSpace(source)); ok {
		return userInputError(errors.New(msg))
	}
	return err
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func printDependencyRow(d dependency.View) {
	line := fmt.Sprintf("  %s %s %s (%s", d.EpisodeID, d.Kind, d.DependsOn, d.Signal)
	if d.Reference != "" {
		line += fmt.Sprintf(": %q", d.Reference)
	}
	line += fmt.Sprintf(", by %s at %s)", d.CreatedBy, d.CreatedAt)
	if d.RemovedAt != "" {
		line += fmt.Sprintf(" removed by %s at %s", d.RemovedBy, d.RemovedAt)
		if d.RemovalReason != "" {
			line += fmt.Sprintf(": %q", d.RemovalReason)
		}
	}
	fmt.Println(line)
}

func printDependencyList(v dependencyListView) {
	fmt.Printf("Episode %s\n", v.EpisodeID)
	fmt.Printf("Depends on (%d):\n", len(v.DependsOn))
	for _, d := range v.DependsOn {
		printDependencyRow(d)
	}
	fmt.Printf("Dependents (%d):\n", len(v.Dependents))
	for _, d := range v.Dependents {
		printDependencyRow(d)
	}
	if len(v.PendingCitations) > 0 {
		fmt.Printf("Citations not resolved yet (%d):\n", len(v.PendingCitations))
		for _, p := range v.PendingCitations {
			fmt.Printf("  %s (since %s)\n", p.LookupKey, p.CreatedAt)
		}
	}
}

func printDependencyChange(v dependencyChangeView) {
	d := v.Dependency
	if d.RemovedAt != "" {
		fmt.Printf("Removed: episode %s no longer %s episode %s (removed by %s).\n", d.EpisodeID, d.Kind, d.DependsOn, d.RemovedBy)
		if d.RemovalReason != "" {
			fmt.Printf("  reason: %s\n", d.RemovalReason)
		}
	} else {
		fmt.Printf("Declared: episode %s %s episode %s (by %s).\n", d.EpisodeID, d.Kind, d.DependsOn, d.CreatedBy)
	}
	fmt.Printf("  %d belief(s) rescored: %s\n", len(v.RescoredBeliefs), strings.Join(v.RescoredBeliefs, ", "))
	if v.SourceRetracted {
		fmt.Printf("  note: episode %s is retracted; retracting or un-retracting it again would now reach a different set of beliefs.\n", d.DependsOn)
	}
}
