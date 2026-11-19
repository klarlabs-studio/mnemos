package kernel_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"go.klarlabs.de/axi"
	"go.klarlabs.de/axi/domain"
	"go.klarlabs.de/mnemos/internal/kernel"
)

var errSentinel = errors.New("sentinel the store raised")

// failingExecutor returns a wrapped sentinel, the way a governed write
// returns a store conflict.
type failingExecutor struct{}

func (failingExecutor) Execute(_ context.Context, _ any, _ domain.CapabilityInvoker) (domain.ExecutionResult, []domain.EvidenceRecord, error) {
	return domain.ExecutionResult{}, nil, fmt.Errorf("declare: %w", errSentinel)
}

// nestingExecutor runs another governed call inside its own execution and
// fails with a different error, so each call's cause must stay its own.
type nestingExecutor struct{ inner **kernel.Governed }

var errOuter = errors.New("outer executor failed")

func (e nestingExecutor) Execute(ctx context.Context, _ any, _ domain.CapabilityInvoker) (domain.ExecutionResult, []domain.EvidenceRecord, error) {
	_, cause, err := (*e.inner).ExecuteWithCause(ctx, axi.Invocation{Action: kernel.ActionName("remember_event"), Input: map[string]any{}})
	if err != nil || !errors.Is(cause, errSentinel) {
		return domain.ExecutionResult{}, nil, fmt.Errorf("nested cause = %v (%v), want the sentinel", cause, err)
	}
	return domain.ExecutionResult{}, nil, errOuter
}

// An executor's error reaches the caller with its type: the kernel still
// records the failure as text, and the caller still matches the sentinel.
func TestExecuteWithCause_KeepsTheExecutorsError(t *testing.T) {
	t.Parallel()
	var inner *kernel.Governed
	k, err := kernel.Build(nil, testActions(), map[string]domain.ActionExecutor{
		kernel.ExecutorRef("remember"):       nestingExecutor{inner: &inner},
		kernel.ExecutorRef("remember_event"): failingExecutor{},
	}, axi.Budget{}, "")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	inner = k

	res, cause, err := k.ExecuteWithCause(context.Background(), axi.Invocation{Action: kernel.ActionName("remember_event"), Input: map[string]any{}})
	if err != nil {
		t.Fatalf("ExecuteWithCause: %v", err)
	}
	if res.Failure == nil || res.Failure.Code != "EXECUTION_ERROR" || res.Failure.Message != "declare: "+errSentinel.Error() {
		t.Errorf("failure = %+v, want the executor's error as EXECUTION_ERROR text", res.Failure)
	}
	if !errors.Is(cause, errSentinel) {
		t.Errorf("cause = %v, want the sentinel", cause)
	}

	_, cause, err = k.ExecuteWithCause(context.Background(), axi.Invocation{Action: kernel.ActionName("remember"), Input: map[string]any{}})
	if err != nil {
		t.Fatalf("nested ExecuteWithCause: %v", err)
	}
	if !errors.Is(cause, errOuter) || errors.Is(cause, errSentinel) {
		t.Errorf("outer cause = %v, want the outer executor's own error", cause)
	}
}

// A success carries no cause, and a plain Execute still works.
func TestExecuteWithCause_NoCauseOnSuccess(t *testing.T) {
	t.Parallel()
	k, err := kernel.Build(nil, testActions(), map[string]domain.ActionExecutor{
		kernel.ExecutorRef("remember"):       stubExecutor{},
		kernel.ExecutorRef("remember_event"): stubExecutor{},
	}, axi.Budget{}, "")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	res, cause, err := k.ExecuteWithCause(context.Background(), axi.Invocation{Action: kernel.ActionName("remember"), Input: map[string]any{}})
	if err != nil || cause != nil || res.Failure != nil || res.Result == nil {
		t.Errorf("success = %+v, cause %v, err %v", res, cause, err)
	}
}
