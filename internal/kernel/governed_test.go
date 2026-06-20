package kernel

import (
	"context"
	"errors"
	"testing"

	"go.klarlabs.de/axi"
	"go.klarlabs.de/axi/domain"
	"go.klarlabs.de/bolt"
)

// erroringExec returns a hard error so the execution fails as a business
// failure (axi surfaces it as res.Failure with a saved session).
type erroringExec struct{}

func (erroringExec) Execute(_ context.Context, _ any, _ domain.CapabilityInvoker) (domain.ExecutionResult, []domain.EvidenceRecord, error) {
	return domain.ExecutionResult{}, nil, errors.New("executor boom")
}

// TestGoverned_LastSession_OnExecutorNotFound proves the hard-error path
// (an action registered with no executor) still leaves a retrievable
// session via LastSession, even though Execute returns (nil, err) with no
// result to read SessionID from. This is the truthful version of the old
// "a failed write still leaves an auditable trail" promise.
func TestGoverned_LastSession_OnExecutorNotFound(t *testing.T) {
	t.Parallel()
	// Register the action via the plugin but bind NO executor.
	k, err := Build(nil, internalActions(), map[string]domain.ActionExecutor{}, axi.Budget{}, "")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if k.LastSession() != nil {
		t.Fatal("LastSession should be nil before any write")
	}

	res, execErr := k.Execute(context.Background(), axi.Invocation{Action: ActionName("remember")})
	if execErr == nil {
		t.Fatal("expected hard error for unregistered executor")
	}
	if res != nil {
		t.Fatalf("expected nil result on hard error, got %+v", res)
	}
	got := k.LastSession()
	if got == nil {
		t.Fatal("LastSession is nil after a hard-error write — failed write left no trail")
	}
	if got.Status() != domain.StatusFailed {
		t.Errorf("recovered session status = %q, want failed", got.Status())
	}
}

// TestGoverned_LastSession_OnCtxCancel proves a ctx-cancel surfaced
// mid-execution also leaves a retrievable session.
func TestGoverned_LastSession_OnCtxCancel(t *testing.T) {
	t.Parallel()
	execs := map[string]domain.ActionExecutor{ExecutorRef("remember"): internalStub{}}
	k, err := Build(nil, internalActions(), execs, axi.Budget{}, "")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, execErr := k.Execute(ctx, axi.Invocation{Action: ActionName("remember")})
	if execErr == nil {
		t.Fatal("expected ctx-cancel error")
	}
	if res != nil {
		t.Fatalf("expected nil result, got %+v", res)
	}
	if k.LastSession() == nil {
		t.Fatal("LastSession is nil after ctx-cancel — failed write left no trail")
	}
}

// TestGoverned_LastSession_OnBusinessFailure proves an executor returning
// an error (a post-execution business failure) leaves a session — the
// common already-working path, pinned so a refactor can't regress it.
func TestGoverned_LastSession_OnBusinessFailure(t *testing.T) {
	t.Parallel()
	execs := map[string]domain.ActionExecutor{ExecutorRef("remember"): erroringExec{}}
	k, err := Build(nil, internalActions(), execs, axi.Budget{}, "")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	res, execErr := k.Execute(context.Background(), axi.Invocation{Action: ActionName("remember")})
	if execErr != nil {
		t.Fatalf("business failure should not be a hard error: %v", execErr)
	}
	if res == nil || res.Failure == nil {
		t.Fatal("expected res.Failure for executor error")
	}
	s, gerr := k.GetSession(string(res.SessionID))
	if gerr != nil || s == nil {
		t.Fatalf("GetSession after business failure: %v", gerr)
	}
}

// TestGoverned_NilSafety verifies LastSession / Close tolerate a nil
// Governed.
func TestGoverned_NilSafety(t *testing.T) {
	t.Parallel()
	var g *Governed
	if g.LastSession() != nil {
		t.Error("nil Governed.LastSession should be nil")
	}
	if err := g.Close(); err != nil {
		t.Errorf("nil Governed.Close: %v", err)
	}
}

// TestBuild_RegisterPluginError surfaces a register error path. An empty
// action name fails plugin construction; assert Build returns the wrapped
// error rather than a kernel.
func TestNewPlugin_InvalidActionName(t *testing.T) {
	t.Parallel()
	_, err := newPlugin([]Action{{Name: "bad name", Effect: domain.EffectWriteLocal}})
	if err == nil {
		t.Fatal("expected error for empty action name")
	}
}

// TestBuild_PluginError makes Build fail by passing an action whose name
// can't be turned into a valid axi action name.
func TestBuild_PluginError(t *testing.T) {
	t.Parallel()
	_, err := Build(nil, []Action{{Name: "bad name", Effect: domain.EffectWriteLocal}}, nil, axi.Budget{}, "")
	if err == nil {
		t.Fatal("expected Build to fail on invalid action name")
	}
}

// TestBoltAdapters_AllLevels drives every bolt logger adapter level so
// the Debug/Warn/Error branches (0% before) execute, including a field.
func TestBoltAdapters_AllLevels(t *testing.T) {
	t.Parallel()
	lg := bolt.New(bolt.NewJSONHandler(noopWriter{}))
	bl := boltLogger{logger: lg}
	f := domain.Field{Key: "k", Value: "v"}
	bl.Debug("debug", f)
	bl.Info("info", f)
	bl.Warn("warn", f)
	bl.Error("error", f)

	// multiPublisher tolerates nil entries and fans out in order.
	var hits int
	counter := publisherFn(func(domain.DomainEvent) { hits++ })
	mp := multiPublisher{counter, nil, counter}
	mp.Publish(domain.SessionStarted{})
	if hits != 2 {
		t.Errorf("multiPublisher fanned to %d publishers, want 2", hits)
	}

	// boltPublisher publishes without panicking.
	boltPublisher{logger: lg}.Publish(domain.SessionCompleted{})
}

type publisherFn func(domain.DomainEvent)

func (f publisherFn) Publish(e domain.DomainEvent) { f(e) }
