package dependency_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/dependency"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
	_ "go.klarlabs.de/mnemos/internal/store/sqlite"
)

func newWriter(t *testing.T) *govwrite.Writer {
	t.Helper()
	w, err := govwrite.New(context.Background(), "sqlite://"+filepath.Join(t.TempDir(), "d.db"), nil)
	if err != nil {
		t.Fatalf("govwrite.New: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	now := time.Now().UTC()
	var evs []domain.Event
	for _, e := range []struct{ id, run string }{{"ev_src", "run-a"}, {"ev_copy", "run-a"}, {"ev_other", "run-b"}} {
		evs = append(evs, domain.Event{ID: e.id, RunID: e.run, Content: "content of " + e.id, SchemaVersion: "1.0",
			SourceInputID: "src-" + e.id, Timestamp: now, IngestedAt: now, CreatedBy: "x"})
	}
	if _, err := w.Events(context.Background(), evs); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return w
}

func inRun(run string) func(string) bool { return func(r string) bool { return r == run } }

func TestDeclare_ValidatesBeforeWriting(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()
	conn := w.Conn()
	called := 0
	spy := func(ctx context.Context, d domain.EventDependency) (domain.DependencyChange, error) {
		called++
		return w.DeclareEventDependency(ctx, d)
	}
	ok := dependency.DeclareRequest{EventID: "ev_copy", DependsOn: "ev_src", Kind: "copies"}
	with := func(f func(*dependency.DeclareRequest)) dependency.DeclareRequest { r := ok; f(&r); return r }
	cases := []struct {
		name string
		req  dependency.DeclareRequest
		want error
	}{
		{"empty id", with(func(r *dependency.DeclareRequest) { r.EventID = " " }), dependency.ErrEventIDRequired},
		{"no source", with(func(r *dependency.DeclareRequest) { r.DependsOn = "" }), dependency.ErrInvalid},
		{"self", with(func(r *dependency.DeclareRequest) { r.DependsOn = "ev_copy" }), dependency.ErrInvalid},
		{"unknown kind", with(func(r *dependency.DeclareRequest) { r.Kind = "mentions" }), dependency.ErrInvalid},
		{"declared is a signal, not a kind", with(func(r *dependency.DeclareRequest) { r.Kind = "declared" }), dependency.ErrInvalid},
		{"long note", with(func(r *dependency.DeclareRequest) {
			r.Reference = strings.Repeat("é", domain.MaxDependencyReferenceLen+1)
		}), dependency.ErrInvalid},
		{"unknown episode", with(func(r *dependency.DeclareRequest) { r.EventID = "ev_missing" }), dependency.ErrEventNotFound},
		{"unknown source", with(func(r *dependency.DeclareRequest) { r.DependsOn = "ev_missing" }), dependency.ErrEventNotFound},
		{"source outside the run scope", with(func(r *dependency.DeclareRequest) { r.DependsOn = "ev_other"; r.RunAllowed = inRun("run-a") }), dependency.ErrEventNotFound},
		{"episode outside the run scope", with(func(r *dependency.DeclareRequest) { r.RunAllowed = inRun("run-b") }), dependency.ErrEventNotFound},
	}
	for _, tc := range cases {
		if _, err := dependency.Declare(ctx, conn.Events, conn.Dependencies, spy, tc.req); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	if called != 0 {
		t.Fatalf("declaration ran %d time(s) for rejected requests", called)
	}

	change, err := dependency.Declare(ctx, conn.Events, conn.Dependencies, spy, dependency.DeclareRequest{
		EventID: " ev_copy ", DependsOn: " ev_src ", Kind: " copies ", Reference: "  reposted  ", Actor: "alice", RunAllowed: inRun("run-a"),
	})
	if err != nil {
		t.Fatalf("Declare: %v", err)
	}
	d := change.Dependency
	if d.EventID != "ev_copy" || d.DependsOn != "ev_src" || d.Kind != domain.DependencyCopies || d.Reference != "reposted" ||
		d.CreatedBy != "alice" || d.Signal != domain.SignalDeclared {
		t.Errorf("declared = %+v", d)
	}
	if _, err := dependency.Declare(ctx, conn.Events, conn.Dependencies, spy, ok); !errors.Is(err, dependency.ErrExists) {
		t.Errorf("second declaration: err = %v, want ErrExists", err)
	}
	if called != 1 {
		t.Errorf("declaration ran %d times, want 1 (the conflict is decided before the write)", called)
	}
	report := dependency.NewChangeReport(change)
	if report.Dependency.Signal != "declared" || report.Dependency.CreatedAt == "" || report.Dependency.RemovedAt != "" || report.RescoredBeliefs == nil {
		t.Errorf("report = %+v", report)
	}
}

func TestRemove_ValidatesBeforeWriting(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()
	conn := w.Conn()
	called := 0
	spy := func(ctx context.Context, id, source, actor, reason string) (domain.DependencyChange, error) {
		called++
		return w.RemoveEventDependency(ctx, id, source, actor, reason)
	}
	cases := []struct {
		name string
		req  dependency.RemoveRequest
		want error
	}{
		{"empty id", dependency.RemoveRequest{DependsOn: "ev_src"}, dependency.ErrEventIDRequired},
		{"no source", dependency.RemoveRequest{EventID: "ev_copy"}, dependency.ErrInvalid},
		{"unknown episode", dependency.RemoveRequest{EventID: "ev_missing", DependsOn: "ev_src"}, dependency.ErrEventNotFound},
		{"never declared", dependency.RemoveRequest{EventID: "ev_copy", DependsOn: "ev_src"}, dependency.ErrNotFound},
		{"long reason", dependency.RemoveRequest{EventID: "ev_copy", DependsOn: "ev_src",
			Reason: strings.Repeat("é", domain.MaxDependencyRemovalReasonLen+1)}, dependency.ErrInvalid},
	}
	for _, tc := range cases {
		if _, err := dependency.Remove(ctx, conn.Events, conn.Dependencies, spy, tc.req); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	if called != 0 {
		t.Fatalf("removal ran %d time(s) for rejected requests", called)
	}
	if _, err := w.DeclareEventDependency(ctx, domain.EventDependency{EventID: "ev_copy", DependsOn: "ev_src", Kind: domain.DependencyCites}); err != nil {
		t.Fatal(err)
	}
	if _, err := dependency.Remove(ctx, conn.Events, conn.Dependencies, spy, dependency.RemoveRequest{
		EventID: "ev_copy", DependsOn: "ev_src", RunAllowed: inRun("run-b"),
	}); !errors.Is(err, dependency.ErrEventNotFound) {
		t.Errorf("out of scope: err = %v, want ErrEventNotFound", err)
	}
	change, err := dependency.Remove(ctx, conn.Events, conn.Dependencies, spy, dependency.RemoveRequest{EventID: "ev_copy", DependsOn: "ev_src", Actor: "bob", Reason: "  a coincidence  "})
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if change.Dependency.RemovedAt == nil || change.Dependency.RemovedBy != "bob" || change.Dependency.RemovalReason != "a coincidence" {
		t.Errorf("removed = %+v", change.Dependency)
	}
	if v := dependency.NewChangeReport(change).Dependency; v.RemovalReason != "a coincidence" {
		t.Errorf("report = %+v, want the reason", v)
	}
	if _, err := dependency.Remove(ctx, conn.Events, conn.Dependencies, spy, dependency.RemoveRequest{EventID: "ev_copy", DependsOn: "ev_src"}); !errors.Is(err, dependency.ErrNotFound) {
		t.Errorf("removing again: err = %v, want ErrNotFound", err)
	}
}

func TestList_LiveRemovedPendingAndRunScope(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()
	conn := w.Conn()
	for _, d := range []domain.EventDependency{
		{EventID: "ev_copy", DependsOn: "ev_src", Kind: domain.DependencyCopies},
		{EventID: "ev_other", DependsOn: "ev_src", Kind: domain.DependencyCites},
	} {
		if _, err := w.DeclareEventDependency(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.RemoveEventDependency(ctx, "ev_copy", "ev_src", "bob", ""); err != nil {
		t.Fatal(err)
	}
	if err := conn.Dependencies.AppendPending(ctx, []domain.PendingReference{{EventID: "ev_src", LookupKey: "named:vendor advisory", CreatedAt: time.Now().UTC()}}); err != nil {
		t.Fatal(err)
	}

	live, err := dependency.List(ctx, conn.Events, conn.Dependencies, dependency.ListRequest{EventID: "ev_src"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	rep := dependency.NewListReport(live)
	if len(rep.DependsOn) != 0 || len(rep.Dependents) != 1 || rep.Dependents[0].EpisodeID != "ev_other" || len(rep.PendingCitations) != 1 {
		t.Errorf("live listing = %+v", rep)
	}
	all, err := dependency.List(ctx, conn.Events, conn.Dependencies, dependency.ListRequest{EventID: "ev_src", IncludeRemoved: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep := dependency.NewListReport(all); len(rep.Dependents) != 2 || rep.Dependents[0].RemovedBy != "bob" || rep.Dependents[0].RemovedAt == "" {
		t.Errorf("listing with removed = %+v, want the tombstone first with who removed it", rep)
	}
	// A run-scoped caller does not learn about episodes of other runs.
	scoped, err := dependency.List(ctx, conn.Events, conn.Dependencies, dependency.ListRequest{EventID: "ev_src", IncludeRemoved: true, RunAllowed: inRun("run-a")})
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped.Dependents) != 1 || scoped.Dependents[0].EventID != "ev_copy" {
		t.Errorf("run-scoped dependents = %+v, want only ev_copy", scoped.Dependents)
	}
	if _, err := dependency.List(ctx, conn.Events, conn.Dependencies, dependency.ListRequest{EventID: "ev_src", RunAllowed: inRun("run-b")}); !errors.Is(err, dependency.ErrEventNotFound) {
		t.Errorf("listing outside the run scope: err = %v, want ErrEventNotFound", err)
	}
	if _, err := dependency.List(ctx, conn.Events, conn.Dependencies, dependency.ListRequest{EventID: ""}); !errors.Is(err, dependency.ErrEventIDRequired) {
		t.Errorf("no id: err = %v", err)
	}
}

// A concurrent edit that lands after the check and before the governed write
// is the same refusal the check gives: the store's sentinel comes back
// through the kernel with its type, so every surface maps it to 409 / 404.
func TestDeclareAndRemove_ConcurrentEditIsTheSameRefusal(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()
	conn := w.Conn()
	raceDeclare := func(ctx context.Context, d domain.EventDependency) (domain.DependencyChange, error) {
		// Another writer declares the pair between the check and this write.
		other := d
		other.Signal, other.CreatedAt, other.CreatedBy = domain.SignalDeclared, time.Now().UTC(), "other-writer"
		if _, err := conn.Dependencies.Declare(ctx, other); err != nil {
			t.Fatalf("concurrent declaration: %v", err)
		}
		return w.DeclareEventDependency(ctx, d)
	}
	if _, err := dependency.Declare(ctx, conn.Events, conn.Dependencies, raceDeclare, dependency.DeclareRequest{
		EventID: "ev_copy", DependsOn: "ev_src", Kind: "copies",
	}); !errors.Is(err, dependency.ErrExists) {
		t.Errorf("raced declaration: err = %v, want ErrExists", err)
	}

	raceRemove := func(ctx context.Context, id, source, actor, reason string) (domain.DependencyChange, error) {
		if _, err := conn.Dependencies.Remove(ctx, id, source, "other-writer", "", time.Now().UTC()); err != nil {
			t.Fatalf("concurrent removal: %v", err)
		}
		return w.RemoveEventDependency(ctx, id, source, actor, reason)
	}
	if _, err := dependency.Remove(ctx, conn.Events, conn.Dependencies, raceRemove, dependency.RemoveRequest{
		EventID: "ev_copy", DependsOn: "ev_src",
	}); !errors.Is(err, dependency.ErrNotFound) {
		t.Errorf("raced removal: err = %v, want ErrNotFound", err)
	}
}
