package workflow

import "go.klarlabs.de/statekit"

// newStatusMachine returns the workflow state machine used to validate job
// status transitions.
func newStatusMachine() (*statekit.Interpreter[struct{}], error) {
	machine, err := statekit.NewMachine[struct{}]("workflow").
		WithInitial("pending").
		State("pending").On("running").Target("running").Done().
		State("running").
		On("loading").Target("loading").
		On("querying").Target("querying").
		On("completed").Target("completed").
		On("failed").Target("failed").
		On("retrying").Target("retrying").
		Done().
		State("loading").
		On("extracting").Target("extracting").
		On("querying").Target("querying").
		On("saving").Target("saving").
		On("relating").Target("relating").
		On("embedding").Target("embedding").
		On("completed").Target("completed").
		On("failed").Target("failed").
		On("retrying").Target("retrying").
		Done().
		State("extracting").
		On("saving").Target("saving").
		On("relating").Target("relating").
		On("embedding").Target("embedding").
		On("completed").Target("completed").
		On("failed").Target("failed").
		On("retrying").Target("retrying").
		Done().
		State("relating").
		On("saving").Target("saving").
		On("completed").Target("completed").
		On("failed").Target("failed").
		On("retrying").Target("retrying").
		Done().
		State("saving").
		On("embedding").Target("embedding").
		On("completed").Target("completed").
		On("failed").Target("failed").
		On("retrying").Target("retrying").
		Done().
		State("querying").
		On("completed").Target("completed").
		On("failed").Target("failed").
		On("retrying").Target("retrying").
		Done().
		State("embedding").
		On("completed").Target("completed").
		On("failed").Target("failed").
		On("retrying").Target("retrying").
		Done().
		State("retrying").
		On("running").Target("running").
		On("loading").Target("loading").
		On("querying").Target("querying").
		On("extracting").Target("extracting").
		On("relating").Target("relating").
		On("saving").Target("saving").
		On("embedding").Target("embedding").
		On("failed").Target("failed").
		Done().
		State("completed").Final().Done().
		State("failed").Final().Done().
		Build()
	if err != nil {
		return nil, err
	}

	return statekit.NewInterpreter(machine), nil
}
