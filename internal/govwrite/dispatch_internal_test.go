package govwrite

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.klarlabs.de/axi"
	axidomain "go.klarlabs.de/axi/domain"

	"go.klarlabs.de/mnemos/internal/kernel"
)

// These white-box tests drive the four terminal branches of [dispatch]
// directly — the kernel-error path, the res.Failure path, the
// nil-result path, and the type-mismatch path — without needing a real
// store. Each fake executor is registered under a test-only action so we
// exercise the dispatch chokepoint in isolation. A failed dispatch must
// surface as a Go error to the caller; the success-shaped result type is
// only returned on the happy path.

const (
	testActionOK       = "test_ok"
	testActionFail     = "test_fail"
	testActionNilRes   = "test_nil_result"
	testActionWrongTyp = "test_wrong_type"
)

// okExec returns a well-formed result whose Data is a string.
type okExec struct{}

func (okExec) Execute(_ context.Context, _ any, _ axidomain.CapabilityInvoker) (axidomain.ExecutionResult, []axidomain.EvidenceRecord, error) {
	return axidomain.ExecutionResult{Data: "ok"}, ev("test.ok", nil), nil
}

// failExec returns a Go error; axi turns this into a failed session that
// dispatch must surface as an error.
type failExec struct{}

func (failExec) Execute(_ context.Context, _ any, _ axidomain.CapabilityInvoker) (axidomain.ExecutionResult, []axidomain.EvidenceRecord, error) {
	return axidomain.ExecutionResult{}, nil, errors.New("boom")
}

// nilResExec succeeds but returns a zero-value result with nil Data and
// no failure — dispatch's "kernel returned no result" / unexpected-type
// guard must still surface a non-nil error rather than a zero value the
// caller could mistake for success.
type nilResExec struct{}

func (nilResExec) Execute(_ context.Context, _ any, _ axidomain.CapabilityInvoker) (axidomain.ExecutionResult, []axidomain.EvidenceRecord, error) {
	return axidomain.ExecutionResult{Data: nil}, ev("test.nil", nil), nil
}

// wrongTypeExec returns a Result whose Data type does not match the
// dispatch[Out] type parameter — exercises the type-mismatch branch.
type wrongTypeExec struct{}

func (wrongTypeExec) Execute(_ context.Context, _ any, _ axidomain.CapabilityInvoker) (axidomain.ExecutionResult, []axidomain.EvidenceRecord, error) {
	return axidomain.ExecutionResult{Data: 42}, ev("test.wrong", nil), nil
}

// newTestWriter builds a Writer whose kernel is registered with the
// test-only actions/executors above. conn is nil — these executors never
// touch storage.
func newTestWriter(t *testing.T) *Writer {
	t.Helper()
	acts := []kernel.Action{
		{Name: testActionOK, Effect: axidomain.EffectWriteLocal, Idempotent: true, Description: "ok"},
		{Name: testActionFail, Effect: axidomain.EffectWriteLocal, Idempotent: true, Description: "fail"},
		{Name: testActionNilRes, Effect: axidomain.EffectWriteLocal, Idempotent: true, Description: "nil"},
		{Name: testActionWrongTyp, Effect: axidomain.EffectWriteLocal, Idempotent: true, Description: "wrong"},
	}
	execs := map[string]axidomain.ActionExecutor{
		kernel.ExecutorRef(testActionOK):       okExec{},
		kernel.ExecutorRef(testActionFail):     failExec{},
		kernel.ExecutorRef(testActionNilRes):   nilResExec{},
		kernel.ExecutorRef(testActionWrongTyp): wrongTypeExec{},
	}
	k, err := kernel.Build(nil, acts, execs, axi.Budget{}, "")
	if err != nil {
		t.Fatalf("kernel.Build: %v", err)
	}
	t.Cleanup(func() { _ = k.Close() })
	return &Writer{kernel: k}
}

func TestDispatch_HappyPath_ReturnsTypedResult(t *testing.T) {
	t.Parallel()
	w := newTestWriter(t)
	got, err := dispatch[string](context.Background(), w, testActionOK, nil)
	if err != nil {
		t.Fatalf("dispatch ok: %v", err)
	}
	if got != "ok" {
		t.Errorf("got %q, want ok", got)
	}
}

func TestDispatch_KernelError_SurfacesError(t *testing.T) {
	t.Parallel()
	w := newTestWriter(t)
	// Pre-cancelled context drives the kernel-level error return.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := dispatch[string](ctx, w, testActionOK, nil)
	if err == nil {
		t.Fatal("expected error from cancelled kernel execution")
	}
}

func TestDispatch_BusinessFailure_SurfacesError(t *testing.T) {
	t.Parallel()
	w := newTestWriter(t)
	_, err := dispatch[string](context.Background(), w, testActionFail, nil)
	if err == nil {
		t.Fatal("expected error from failing executor")
	}
	if !strings.Contains(err.Error(), testActionFail) {
		t.Errorf("error %q should name the action", err)
	}
}

func TestDispatch_NilResult_SurfacesError(t *testing.T) {
	t.Parallel()
	w := newTestWriter(t)
	// Out=string; the executor returns Data=nil. A nil Data is not a
	// string, so dispatch surfaces the no-result / type guard error
	// rather than a zero value masquerading as success.
	_, err := dispatch[string](context.Background(), w, testActionNilRes, nil)
	if err == nil {
		t.Fatal("expected error when executor returns no result data")
	}
}

func TestDispatch_TypeMismatch_SurfacesError(t *testing.T) {
	t.Parallel()
	w := newTestWriter(t)
	// Out=string but Data is an int.
	_, err := dispatch[string](context.Background(), w, testActionWrongTyp, nil)
	if err == nil {
		t.Fatal("expected error on result type mismatch")
	}
	if !strings.Contains(err.Error(), "unexpected result type") {
		t.Errorf("error %q should mention the type mismatch", err)
	}
}
