package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"go.klarlabs.de/mnemos"
	"go.klarlabs.de/mnemos/internal/taskstate"
)

// The gated task-state block (ADR 0050, #447) is an experiment: its tools are
// always registered, so scopes and capability parity stay checkable, but they
// refuse to run until MNEMOS_TASK_STATE (working_memory.task_state) opts in.

var errTaskStateDisabled = errors.New("the task-state block is an experiment (ADR 0050); enable it with MNEMOS_TASK_STATE=1 or working_memory.task_state: true")

func taskStateEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MNEMOS_TASK_STATE"))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

func taskStateOwner(owner string) string {
	if owner = strings.TrimSpace(owner); owner != "" {
		return owner
	}
	return taskstate.DefaultOwner
}

type mcpTaskItem struct {
	Kind    string   `json:"kind"`
	Text    string   `json:"text"`
	RestsOn []string `json:"rests_on,omitempty"`
	Against []string `json:"against,omitempty"`
}

func toMCPTaskItems(items []taskstate.Item) []mcpTaskItem {
	out := make([]mcpTaskItem, 0, len(items))
	for _, it := range items {
		out = append(out, mcpTaskItem{Kind: string(it.Kind), Text: it.Text, RestsOn: it.RestsOn, Against: it.Against})
	}
	return out
}

type mcpGetTaskStateInput struct {
	Owner   string `json:"owner,omitempty" jsonschema:"description=The agent whose task state to read (default claude-code)"`
	Session string `json:"session,omitempty" jsonschema:"description=The session key (default current)"`
}

type mcpGetTaskStateOutput struct {
	Owner   string        `json:"owner"`
	Session string        `json:"session"`
	Block   string        `json:"block"`
	Items   []mcpTaskItem `json:"items"`
}

func mcpGetTaskState(ctx context.Context, in mcpGetTaskStateInput) (mcpGetTaskStateOutput, error) {
	if !taskStateEnabled() {
		return mcpGetTaskStateOutput{}, errTaskStateDisabled
	}
	conn, err := openConn(ctx)
	if err != nil {
		return mcpGetTaskStateOutput{}, err
	}
	defer closeConn(conn)
	owner := taskStateOwner(in.Owner)
	st, err := taskstate.Service{Blocks: conn.Blocks}.Load(ctx, owner, in.Session)
	if err != nil {
		return mcpGetTaskStateOutput{}, err
	}
	session, _ := taskstate.SessionOf(taskstate.Label(in.Session))
	out := mcpGetTaskStateOutput{Owner: owner, Session: session, Items: toMCPTaskItems(st.Items)}
	if len(st.Items) > 0 {
		out.Block = taskstate.Render(st)
	}
	return out, nil
}

type mcpUpdateTaskStateInput struct {
	Owner   string   `json:"owner,omitempty" jsonschema:"description=The agent that owns the task state (default claude-code)"`
	Session string   `json:"session,omitempty" jsonschema:"description=The session key (default current)"`
	Action  string   `json:"action" jsonschema:"required,description=admit | remove | handoff"`
	Kind    string   `json:"kind,omitempty" jsonschema:"description=admit: goal | constraint | commitment | hypothesis | question"`
	Text    string   `json:"text,omitempty" jsonschema:"description=admit/remove: the item statement (one line, at most 300 characters)"`
	RestsOn []string `json:"rests_on,omitempty" jsonschema:"description=admit: claim ids a hypothesis rests on (must exist)"`
}

type mcpTaskHandoff struct {
	EventID    string   `json:"event_id,omitempty"`
	Candidates []string `json:"candidates"`
}

type mcpUpdateTaskStateOutput struct {
	Action   string          `json:"action"`
	Admitted bool            `json:"admitted,omitempty"`
	Reasons  []string        `json:"reasons,omitempty"`
	KnownAs  string          `json:"known_as,omitempty"`
	Against  []string        `json:"against,omitempty"`
	Evicted  []mcpTaskItem   `json:"evicted,omitempty"`
	Removed  bool            `json:"removed,omitempty"`
	Handoff  *mcpTaskHandoff `json:"handoff,omitempty"`
}

func mcpUpdateTaskState(ctx context.Context, actor string, in mcpUpdateTaskStateInput) (mcpUpdateTaskStateOutput, error) {
	if !taskStateEnabled() {
		return mcpUpdateTaskStateOutput{}, errTaskStateDisabled
	}
	w, err := openWriter(ctx)
	if err != nil {
		return mcpUpdateTaskStateOutput{}, err
	}
	defer closeWriter(w)
	conn := w.Conn()
	svc := taskstate.Service{Blocks: conn.Blocks, Probe: taskstate.StoreProbe{Conn: conn}, Limit: mnemos.WorkingMemoryBlockLimit}
	owner := taskStateOwner(in.Owner)
	out := mcpUpdateTaskStateOutput{Action: in.Action}
	switch in.Action {
	case "admit":
		res, err := svc.Admit(ctx, owner, in.Session, taskstate.Item{Kind: taskstate.Kind(in.Kind), Text: in.Text, RestsOn: in.RestsOn})
		if err != nil {
			return out, err
		}
		out.Admitted, out.KnownAs, out.Against = res.Decision.Admit, res.Decision.KnownAs, res.Decision.Against
		for _, r := range res.Decision.Reasons {
			out.Reasons = append(out.Reasons, string(r))
		}
		out.Evicted = toMCPTaskItems(res.Evicted)
	case "remove":
		if out.Removed, err = svc.Remove(ctx, owner, in.Session, in.Text); err != nil {
			return out, err
		}
	case "handoff":
		h, err := svc.HandOff(ctx, w, owner, in.Session, actor)
		if err != nil {
			return out, err
		}
		out.Handoff = &mcpTaskHandoff{EventID: h.EventID, Candidates: append([]string{}, h.Candidates...)}
	default:
		return out, fmt.Errorf("update_task_state: unknown action %q (want admit, remove or handoff)", in.Action)
	}
	return out, nil
}

// handOffTaskStateOnSessionEnd hands every task-state block of the default
// owner to consolidation as candidate claims. Fail-open like the rest of the
// capture hook: an error is reported and the session ends normally. Hosted
// brains are skipped — the REST surface has no task-state endpoint while the
// experiment runs.
func handOffTaskStateOnSessionEnd() {
	if !taskStateEnabled() || hostedConfigured() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	w, err := openWriter(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mnemos: task-state handoff: %v\n", err)
		return
	}
	defer closeWriter(w)
	conn := w.Conn()
	svc := taskstate.Service{Blocks: conn.Blocks, Probe: taskstate.StoreProbe{Conn: conn}, Limit: mnemos.WorkingMemoryBlockLimit}
	if _, err := svc.HandOffAll(ctx, w, taskstate.DefaultOwner, captureActor); err != nil {
		fmt.Fprintf(os.Stderr, "mnemos: task-state handoff: %v\n", err)
	}
}
