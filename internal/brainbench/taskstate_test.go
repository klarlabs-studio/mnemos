package brainbench

import (
	"context"
	"strings"
	"testing"
)

const taskStateFixtures = "../../data/eval/brainbench/taskstate"

func TestTaskStateScenario_Validate(t *testing.T) {
	valid := TaskStateScenario{
		ID: "s", Limit: 100,
		Turns:     []TaskStateTurn{{Kind: "goal", Text: "ship it"}, {Kind: "constraint", Text: "never on fridays"}},
		Questions: []TaskStateQuestion{{ID: "q", Needs: []string{"fridays"}}},
	}
	tests := []struct {
		name    string
		mutate  func(*TaskStateScenario)
		wantErr string
	}{
		{"valid", func(*TaskStateScenario) {}, ""},
		{"no limit", func(s *TaskStateScenario) { s.Limit = 0 }, "limit"},
		{"bad kind", func(s *TaskStateScenario) { s.Turns[0].Kind = "mood" }, "turn 0"},
		{"need matches nothing", func(s *TaskStateScenario) { s.Questions[0].Needs = []string{"mondays"} }, "matches 0"},
		{"need matches two turns", func(s *TaskStateScenario) { s.Questions[0].Needs = []string{"i"} }, "matches 2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := valid
			s.Turns = append([]TaskStateTurn(nil), valid.Turns...)
			s.Questions = []TaskStateQuestion{{ID: "q", Needs: append([]string(nil), valid.Questions[0].Needs...)}}
			tt.mutate(&s)
			err := s.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestTaskStateVerdict(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	ctrl := TaskStateArmResult{NeededInBlock: 0.5, NeededAvailable: 0.5, ImplicitInBlock: f(0.5)}
	tests := []struct {
		name    string
		treated TaskStateArmResult
		noise   float64
		want    string
	}{
		{"better everywhere", TaskStateArmResult{NeededInBlock: 1, NeededAvailable: 1, ImplicitInBlock: f(1)}, 0, "improved"},
		{"worse everywhere", TaskStateArmResult{NeededInBlock: 0, NeededAvailable: 0, ImplicitInBlock: f(0)}, 0, "regressed"},
		{"mixed", TaskStateArmResult{NeededInBlock: 1, NeededAvailable: 0, ImplicitInBlock: f(0.5)}, 0, "mixed"},
		{"identical", ctrl, 0, "no-effect"},
		{"nondeterministic control", ctrl, 0.1, "invalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := taskStateVerdict(ctrl, tt.treated, tt.noise); got != tt.want {
				t.Fatalf("verdict = %q, want %q", got, tt.want)
			}
		})
	}
}

// The committed scenarios, run end to end on real SQLite brains. The verdicts
// are pinned so a change to the gate, the eviction policy or the harness that
// moves a result has to say so. question_burst pins a REGRESSION: the harness
// must be able to report the treatment losing.
func TestRunTaskState_CommittedScenarios(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds three SQLite brains per scenario")
	}
	scenarios, err := LoadTaskStateScenarios(taskStateFixtures)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := RunTaskState(context.Background(), scenarios, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"implicit_constraints": "improved",
		"question_burst":       "regressed",
		"within_budget":        "no-effect",
	}
	if len(rep.Results) != len(want) {
		t.Fatalf("results = %d, want %d", len(rep.Results), len(want))
	}
	for _, res := range rep.Results {
		if res.NoiseFloor != 0 {
			t.Errorf("%s: noise floor %v; the experiment is deterministic", res.Scenario, res.NoiseFloor)
		}
		if res.Verdict != want[res.Scenario] {
			t.Errorf("%s: verdict %q, want %q", res.Scenario, res.Verdict, want[res.Scenario])
		}
		for _, a := range res.Arms {
			if a.BlockChars > 2000 {
				t.Errorf("%s/%s: block %d chars exceeds the budget", res.Scenario, a.Arm, a.BlockChars)
			}
			if a.Arm == "gated" && a.HandedOff == 0 {
				t.Errorf("%s: gated arm handed nothing off", res.Scenario)
			}
		}
	}
	if len(rep.Limitations) == 0 {
		t.Error("limitations must ship inside the report")
	}
}

func TestLoadTaskStateScenarios_EmptyDirIsAnError(t *testing.T) {
	if _, err := LoadTaskStateScenarios(t.TempDir()); err == nil {
		t.Fatal("an empty scenario dir must fail, not report nothing")
	}
}
