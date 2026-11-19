package kernel

import (
	"context"
	"sync"

	"go.klarlabs.de/axi"
	"go.klarlabs.de/axi/domain"
)

// Typed executor errors through the kernel.
//
// axi turns an executor's error into domain.FailureReason{Code:
// "EXECUTION_ERROR", Message: err.Error()} (axi@v1.4.0
// domain/execution.go:256): two strings, so the error's type is gone by the
// time the caller reads the result. A sentinel the store raises inside the
// governed write (a conflict a concurrent writer caused after the caller's
// own check, say) then reached every transport as an opaque internal error.
//
// [Build] wraps every executor so the error it returns is also kept in a
// per-call slot carried by the context, and [Governed.ExecuteWithCause]
// returns it next to the result. The kernel's behaviour is unchanged: the
// session still fails, its evidence chain and audit log record the same
// failure, and the message is the same text. Only the caller gets the error
// itself back, so errors.Is and errors.As work across the governed path.

type causeKey struct{}

// causeSlot holds the error the executor of one Execute call returned.
type causeSlot struct {
	mu  sync.Mutex
	err error
}

func (s *causeSlot) set(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}

func (s *causeSlot) get() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// causeKeepingExecutor records the error its executor returns in the call's
// slot, when the call has one, and returns it unchanged.
type causeKeepingExecutor struct {
	inner domain.ActionExecutor
}

// Execute runs the wrapped executor.
func (e causeKeepingExecutor) Execute(ctx context.Context, input any, invoker domain.CapabilityInvoker) (domain.ExecutionResult, []domain.EvidenceRecord, error) {
	res, evidence, err := e.inner.Execute(ctx, input, invoker)
	if err != nil {
		if slot, ok := ctx.Value(causeKey{}).(*causeSlot); ok {
			slot.set(err)
		}
	}
	return res, evidence, err
}

// ExecuteWithCause runs inv like Execute and, when the executor failed
// (res.Failure set by the executor's error), also returns that error as
// cause, so a typed error survives the kernel. cause is nil on success, on a
// failure the kernel raised itself (rejection, budget, panic) and on a hard
// kernel error (err non-nil).
//
// Each call has its own slot, so a governed write nested inside another
// executor reports its own cause, not its caller's.
func (g *Governed) ExecuteWithCause(ctx context.Context, inv axi.Invocation) (res *axi.Result, cause error, err error) {
	slot := &causeSlot{}
	res, err = g.Execute(context.WithValue(ctx, causeKey{}, slot), inv)
	if err != nil || res == nil || res.Failure == nil {
		return res, nil, err
	}
	if res.Failure.Code != executionErrorCode {
		return res, nil, nil
	}
	return res, slot.get(), nil
}

// executionErrorCode is the failure code axi gives an executor's error.
const executionErrorCode = "EXECUTION_ERROR"
