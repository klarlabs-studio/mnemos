package main

import (
	"context"
	"os"
	"testing"

	"github.com/felixgeelhaar/axi-go"
	"github.com/felixgeelhaar/axi-go/domain"
	"github.com/felixgeelhaar/bolt"
)

// TestApprovalFlow_WriteExternalActionAwaitsThenApproves exercises
// axi-go's approval gate end-to-end against a Mnemos-style kernel
// (same wiring as buildMCPKernel, just with one synthetic action
// flagged as EffectWriteExternal so the gate engages).
//
// Mnemos has no EffectWriteExternal tool yet, but the roadmap item
// "approval flow for any future write-external tool" requires a pinned
// demonstration that we know the API and have it wired. Future tools
// (federation push, outbound webhook, anything that calls a third
// party with side effects) inherit this behaviour by setting the
// effect level on their action definition — no further plumbing.
func TestApprovalFlow_WriteExternalActionAwaitsThenApproves(t *testing.T) {
	t.Parallel()

	var executorCalls int
	kernel, sessionID, err := buildApprovalTestKernel(t, &executorCalls)
	if err != nil {
		t.Fatalf("build kernel: %v", err)
	}
	defer cleanupKernel(kernel)

	res, err := kernel.Execute(context.Background(), axi.Invocation{
		Action: "send_webhook",
		Input:  map[string]any{"url": "https://example.invalid/notify"},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !res.RequiresApproval {
		t.Fatalf("RequiresApproval = false, want true for EffectWriteExternal")
	}
	if res.Status != domain.StatusAwaitingApproval {
		t.Fatalf("status = %s, want %s", res.Status, domain.StatusAwaitingApproval)
	}
	if executorCalls != 0 {
		t.Fatalf("executor invoked %d times before approval; want 0", executorCalls)
	}
	_ = sessionID

	approved, err := kernel.Approve(context.Background(), string(res.SessionID), domain.ApprovalDecision{
		Principal: "ops-on-call",
		Rationale: "test approval",
	})
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if approved.Status != domain.StatusSucceeded {
		t.Errorf("post-approve status = %s, want %s", approved.Status, domain.StatusSucceeded)
	}
	if executorCalls != 1 {
		t.Errorf("executor invoked %d times after approval; want 1", executorCalls)
	}
	if approved.ApprovalDecision == nil || approved.ApprovalDecision.Principal != "ops-on-call" {
		t.Errorf("approval decision not recorded; got %+v", approved.ApprovalDecision)
	}
}

// TestApprovalFlow_WriteExternalActionCanBeRejected verifies the
// other half of the gate: an operator rejecting a pending session
// transitions it to Rejected and the executor never runs. Symmetric
// with Approve so any future tool inherits both paths.
func TestApprovalFlow_WriteExternalActionCanBeRejected(t *testing.T) {
	t.Parallel()

	var executorCalls int
	kernel, _, err := buildApprovalTestKernel(t, &executorCalls)
	if err != nil {
		t.Fatalf("build kernel: %v", err)
	}
	defer cleanupKernel(kernel)

	res, err := kernel.Execute(context.Background(), axi.Invocation{
		Action: "send_webhook",
		Input:  map[string]any{"url": "https://example.invalid/notify"},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != domain.StatusAwaitingApproval {
		t.Fatalf("status = %s, want %s", res.Status, domain.StatusAwaitingApproval)
	}

	rejected, err := kernel.Reject(context.Background(), string(res.SessionID), domain.ApprovalDecision{
		Principal: "ops-on-call",
		Rationale: "policy denies outbound webhooks",
	})
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if rejected.Status != domain.StatusRejected {
		t.Errorf("post-reject status = %s, want %s", rejected.Status, domain.StatusRejected)
	}
	if executorCalls != 0 {
		t.Errorf("executor invoked %d times despite rejection; want 0", executorCalls)
	}
}

// buildApprovalTestKernel constructs a kernel with the same publisher
// wiring buildMCPKernel uses (bolt + the optional JSONL sink), plus a
// single synthetic EffectWriteExternal action backed by a counting
// executor. The action is contributed via a tiny inline plugin so the
// real mnemos plugin tree isn't disturbed.
func buildApprovalTestKernel(_ *testing.T, calls *int) (*axi.Kernel, string, error) {
	logger := bolt.New(bolt.NewJSONHandler(os.Stderr))

	plugin, err := newApprovalTestPlugin()
	if err != nil {
		return nil, "", err
	}

	kernel := axi.New().
		WithLogger(boltAxiLogger{logger: logger}).
		WithDomainEventPublisher(boltAxiPublisher{logger: logger}).
		WithBudget(axi.Budget{MaxCapabilityInvocations: 10})

	kernel.RegisterActionExecutor("exec.send_webhook", approvalTestExecutor{calls: calls})
	if err := kernel.RegisterPlugin(plugin); err != nil {
		return nil, "", err
	}
	return kernel, "", nil
}

func cleanupKernel(_ *axi.Kernel) {
	// Kernels have no explicit Close today; placeholder so the test
	// surface stays symmetric if one is added later.
}

// approvalTestPlugin is a stripped-down Plugin that contributes one
// action: send_webhook with EffectWriteExternal.
type approvalTestPlugin struct {
	contribution *domain.PluginContribution
}

func newApprovalTestPlugin() (approvalTestPlugin, error) {
	name, err := domain.NewActionName("send_webhook")
	if err != nil {
		return approvalTestPlugin{}, err
	}
	action, err := domain.NewActionDefinition(
		name,
		"Synthetic write-external action used to exercise the approval gate.",
		domain.EmptyContract(),
		domain.EmptyContract(),
		nil,
		domain.EffectProfile{Level: domain.EffectWriteExternal},
		domain.IdempotencyProfile{IsIdempotent: false},
	)
	if err != nil {
		return approvalTestPlugin{}, err
	}
	if err := action.BindExecutor("exec.send_webhook"); err != nil {
		return approvalTestPlugin{}, err
	}
	contrib, err := domain.NewPluginContribution("test.approval", []*domain.ActionDefinition{action}, nil)
	if err != nil {
		return approvalTestPlugin{}, err
	}
	return approvalTestPlugin{contribution: contrib}, nil
}

// Contribute satisfies domain.Plugin.
func (p approvalTestPlugin) Contribute() (*domain.PluginContribution, error) {
	return p.contribution, nil
}

// approvalTestExecutor counts invocations so the test can assert the
// gate only releases the executor after Approve (and never after
// Reject).
type approvalTestExecutor struct{ calls *int }

func (e approvalTestExecutor) Execute(_ context.Context, _ any, _ domain.CapabilityInvoker) (domain.ExecutionResult, []domain.EvidenceRecord, error) {
	*e.calls++
	return domain.ExecutionResult{Data: "delivered"}, nil, nil
}
