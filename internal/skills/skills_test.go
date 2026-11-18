package skills_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/skills"
	"go.klarlabs.de/mnemos/internal/store"
	_ "go.klarlabs.de/mnemos/internal/store/memory"
)

func openStore(t *testing.T) *store.Conn {
	t.Helper()
	conn, err := store.Open(context.Background(), "memory://")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func seedActions(t *testing.T, conn *store.Conn) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	for _, a := range []domain.Action{
		{ID: "a-mine", RunID: "run-mine", Kind: domain.ActionKind("deploy"), Subject: "api", Actor: "x", At: now},
		{ID: "a-theirs", RunID: "run-theirs", Kind: domain.ActionKind("deploy"), Subject: "api", Actor: "x", At: now},
		{ID: "a-none", Kind: domain.ActionKind("rollback"), Subject: "db", Actor: "x", At: now},
	} {
		if err := conn.Actions.Append(ctx, a); err != nil {
			t.Fatalf("seed action %s: %v", a.ID, err)
		}
	}
	for _, o := range []domain.Outcome{
		{ID: "o-mine", ActionID: "a-mine", Result: domain.OutcomeResultSuccess, ObservedAt: now, Source: "push"},
		{ID: "o-theirs", ActionID: "a-theirs", Result: domain.OutcomeResultFailure, ObservedAt: now, Source: "push"},
	} {
		if err := conn.Outcomes.Append(ctx, o); err != nil {
			t.Fatalf("seed outcome %s: %v", o.ID, err)
		}
	}
}

func ids[T any](xs []T, id func(T) string) map[string]bool {
	out := map[string]bool{}
	for _, x := range xs {
		out[id(x)] = true
	}
	return out
}

func actionID(a domain.Action) string   { return a.ID }
func outcomeID(o domain.Outcome) string { return o.ID }

func TestActions_FiltersAndRunScope(t *testing.T) {
	conn := openStore(t)
	seedActions(t, conn)
	ctx := context.Background()
	mine := skills.RunScope(func(r string) bool { return r == "run-mine" })

	tests := []struct {
		name string
		f    skills.ActionFilter
		want []string
	}{
		{"all", skills.ActionFilter{}, []string{"a-mine", "a-theirs", "a-none"}},
		{"by subject", skills.ActionFilter{Subject: "api"}, []string{"a-mine", "a-theirs"}},
		{"by run", skills.ActionFilter{RunID: "run-theirs"}, []string{"a-theirs"}},
		{"subject and run narrow each other", skills.ActionFilter{Subject: "api", RunID: "run-mine"}, []string{"a-mine"}},
		{"run scope drops other runs and unassigned", skills.ActionFilter{Runs: mine}, []string{"a-mine"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := skills.Actions(ctx, conn.Actions, tt.f)
			if err != nil {
				t.Fatalf("Actions: %v", err)
			}
			gotIDs := ids(got, actionID)
			if len(gotIDs) != len(tt.want) {
				t.Fatalf("got %v, want %v", gotIDs, tt.want)
			}
			for _, w := range tt.want {
				if !gotIDs[w] {
					t.Errorf("missing %s in %v", w, gotIDs)
				}
			}
		})
	}

	if _, err := skills.Actions(ctx, conn.Actions, skills.ActionFilter{RunID: "run-theirs", Runs: mine}); !errors.Is(err, skills.ErrRunNotAllowed) {
		t.Errorf("naming a forbidden run: got %v, want ErrRunNotAllowed", err)
	}
	if _, err := skills.Actions(ctx, nil, skills.ActionFilter{}); !errors.Is(err, skills.ErrUnsupported) {
		t.Errorf("nil repo: got %v, want ErrUnsupported", err)
	}
}

func TestOutcomes_FollowTheirActionsRun(t *testing.T) {
	conn := openStore(t)
	seedActions(t, conn)
	ctx := context.Background()

	all, err := skills.Outcomes(ctx, conn.Outcomes, conn.Actions, skills.OutcomeFilter{})
	if err != nil || len(all) != 2 {
		t.Fatalf("all outcomes: %v %v", all, err)
	}
	byAction, err := skills.Outcomes(ctx, conn.Outcomes, conn.Actions, skills.OutcomeFilter{ActionID: "a-theirs"})
	if err != nil || len(byAction) != 1 || byAction[0].ID != "o-theirs" {
		t.Fatalf("by action: %v %v", byAction, err)
	}
	scoped, err := skills.Outcomes(ctx, conn.Outcomes, conn.Actions, skills.OutcomeFilter{
		Runs: func(r string) bool { return r == "run-mine" },
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(scoped, outcomeID); len(got) != 1 || !got["o-mine"] {
		t.Errorf("run-scoped outcomes: got %v, want only o-mine", got)
	}
}

func TestSchemasAndReflexes_AppendThenList(t *testing.T) {
	conn := openStore(t)
	ctx := context.Background()

	n, err := skills.AppendSchemas(ctx, func(ctx context.Context, l domain.Lesson) (string, error) {
		return l.ID, conn.Lessons.Append(ctx, l)
	}, "agent-1", []domain.Lesson{
		{ID: "ls-1", Statement: "roll back api on 5xx", Trigger: "5xx", Scope: domain.Scope{Service: "api"}, Evidence: []string{"a1"}, Confidence: 0.8},
		{ID: "ls-2", Statement: "scale db on lag", Trigger: "lag", Scope: domain.Scope{Service: "db"}, Evidence: []string{"a2"}, Confidence: 0.7},
	})
	if err != nil || n != 2 {
		t.Fatalf("AppendSchemas: %d %v", n, err)
	}
	got, err := skills.Schemas(ctx, conn.Lessons, skills.SchemaFilter{Service: "api"})
	if err != nil || len(got) != 1 || got[0].ID != "ls-1" {
		t.Fatalf("Schemas by service: %v %v", got, err)
	}
	if got[0].CreatedBy != "agent-1" || got[0].DerivedAt.IsZero() {
		t.Errorf("defaults not applied on the stored schema: created_by=%q derived_at=%v", got[0].CreatedBy, got[0].DerivedAt)
	}
	if got, _ := skills.Schemas(ctx, conn.Lessons, skills.SchemaFilter{Service: "api", Trigger: "lag"}); len(got) != 0 {
		t.Errorf("service and trigger must both match: got %v", got)
	}

	// All-or-nothing: an invalid element writes nothing.
	_, err = skills.AppendSchemas(ctx, func(ctx context.Context, l domain.Lesson) (string, error) {
		return l.ID, conn.Lessons.Append(ctx, l)
	}, "agent-1", []domain.Lesson{
		{ID: "ls-3", Statement: "ok", Evidence: []string{"a3"}, Confidence: 0.5},
		{ID: "ls-4", Statement: "", Evidence: []string{"a4"}, Confidence: 0.5},
	})
	if !errors.Is(err, skills.ErrInvalidRecord) {
		t.Fatalf("invalid batch: got %v, want ErrInvalidRecord", err)
	}
	if all, _ := skills.Schemas(ctx, conn.Lessons, skills.SchemaFilter{}); len(all) != 2 {
		t.Errorf("an invalid batch must write nothing; store holds %d schemas", len(all))
	}

	n, err = skills.AppendReflexes(ctx, func(ctx context.Context, p domain.Playbook) (string, error) {
		return p.ID, conn.Playbooks.Append(ctx, p)
	}, "agent-1", []domain.Playbook{{
		ID: "pb-1", Trigger: "5xx", Statement: "respond to 5xx", Scope: domain.Scope{Service: "api"}, Confidence: 0.6,
		Steps: []domain.PlaybookStep{{Order: 1, Action: "rollback"}},
	}})
	if err != nil || n != 1 {
		t.Fatalf("AppendReflexes: %d %v", n, err)
	}
	refl, err := skills.Reflexes(ctx, conn.Playbooks, skills.ReflexFilter{Trigger: "5xx"})
	if err != nil || len(refl) != 1 || len(refl[0].Steps) != 1 || refl[0].CreatedBy != "agent-1" {
		t.Fatalf("Reflexes: %+v %v", refl, err)
	}
	if _, err := skills.AppendReflexes(ctx, nil, "a", nil); !errors.Is(err, skills.ErrInvalidRecord) {
		t.Errorf("empty batch: got %v, want ErrInvalidRecord", err)
	}
}

func TestGlobalSchemas_StatusFilter(t *testing.T) {
	conn := openStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, gs := range []domain.GlobalSchema{
		{ID: "gs-a", Statement: "a", Status: domain.GlobalSchemaStatusActive, DistinctTenants: 2, EvidenceCount: 3, Confidence: 0.8, PromotedAt: now},
		{ID: "gs-p", Statement: "p", Status: domain.GlobalSchemaStatusPending, DistinctTenants: 2, EvidenceCount: 3, Confidence: 0.8, PromotedAt: now},
	} {
		if err := conn.GlobalSchemas.Upsert(ctx, gs); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	all, err := skills.GlobalSchemas(ctx, conn.GlobalSchemas, "")
	if err != nil || len(all) != 2 {
		t.Fatalf("all: %v %v", all, err)
	}
	pending, err := skills.GlobalSchemas(ctx, conn.GlobalSchemas, "pending")
	if err != nil || len(pending) != 1 || pending[0].ID != "gs-p" {
		t.Fatalf("pending: %v %v", pending, err)
	}
	if _, err := skills.GlobalSchemas(ctx, conn.GlobalSchemas, "bogus"); !errors.Is(err, skills.ErrInvalidFilter) {
		t.Errorf("bogus status: got %v, want ErrInvalidFilter", err)
	}
	if _, err := skills.GlobalSchemas(ctx, nil, ""); !errors.Is(err, skills.ErrUnsupported) {
		t.Errorf("nil repo: got %v, want ErrUnsupported", err)
	}
}

func TestPageAndWindow(t *testing.T) {
	if l, o := skills.Page(0, -3); l != skills.DefaultLimit || o != 0 {
		t.Errorf("Page(0,-3) = %d,%d", l, o)
	}
	if l, _ := skills.Page(10_000, 0); l != skills.MaxLimit {
		t.Errorf("Page caps at MaxLimit, got %d", l)
	}
	xs := []int{1, 2, 3, 4, 5}
	if got := skills.Window(xs, 2, 1); len(got) != 2 || got[0] != 2 {
		t.Errorf("Window(2,1) = %v", got)
	}
	if got := skills.Window(xs, 0, 3); len(got) != 2 {
		t.Errorf("Window with no cap = %v", got)
	}
	if got := skills.Window(xs, 2, 9); got != nil {
		t.Errorf("Window past the end = %v", got)
	}
}
