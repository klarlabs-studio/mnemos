package brainbench

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"gopkg.in/yaml.v3"

	"go.klarlabs.de/mnemos"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/pipeline"
	"go.klarlabs.de/mnemos/internal/store"
	"go.klarlabs.de/mnemos/internal/taskstate"
)

// The task-state experiment (ADR 0050, issue #447) asks one question: does the
// gated task-state block keep what a session later needs better than today's
// FIFO working-memory block, under the same 2,000-character budget?
//
// It is a separate mode from the consolidation A/B because it measures a
// different organ: what is in working memory at question time, not what the
// long-term store knows. Each scenario replays one session of task-state items
// into five arms, every one on its own copy of the same seeded brain:
//
//   - control: the real AppendBlock path — ungated, oldest lines evicted.
//   - control_replica: control again, on another copy. Its disagreement with
//     control is the noise floor; the experiment is deterministic, so anything
//     above zero invalidates the run.
//   - gated: the treatment — gate plus priority eviction.
//   - gate_only and priority_only: ablations, so a win (or loss) can be
//     attributed to the gate or to the eviction policy rather than to both.
//
// Both arms receive exactly the same items. The control is not handicapped
// with chatter the treatment never sees: the only differences are the ones
// under test.

// TaskStateLimitations ships inside every task-state report.
var TaskStateLimitations = []string{
	"Scenarios are hand-written; a win shows the mechanism works where it was designed to, not that real sessions look like this.",
	"\"Needed\" is a substring check on block text at question time. It measures retention, not whether a model would USE the item.",
	"Placement (mean offset) is descriptive: the effect of position on a real model is not measured here.",
	"The gate's surprise source is the rule-based contradiction detector only; the #446 expectation ledger is not wired.",
	"The handoff metric is descriptive: today's blocks have no handoff at all, so the control scores zero by construction.",
}

// TaskStateScenario is one replayed session.
type TaskStateScenario struct {
	ID          string              `yaml:"id"`
	Description string              `yaml:"description"`
	Limit       int                 `yaml:"limit"`
	LongTerm    []string            `yaml:"long_term"`
	Turns       []TaskStateTurn     `yaml:"turns"`
	Questions   []TaskStateQuestion `yaml:"questions"`
}

// TaskStateTurn is one item the session offers to working memory.
type TaskStateTurn struct {
	Kind string `yaml:"kind"`
	Text string `yaml:"text"`
}

// TaskStateQuestion names what a question asked after the session needs.
type TaskStateQuestion struct {
	ID string `yaml:"id"`
	// Implicit marks a question that shares no content with what it needs.
	Implicit bool     `yaml:"implicit"`
	Needs    []string `yaml:"needs"`
}

// Validate checks the scenario is runnable and that every need names exactly
// one turn, so a typo cannot silently turn a need into one nothing can meet.
func (s TaskStateScenario) Validate() error {
	if strings.TrimSpace(s.ID) == "" {
		return fmt.Errorf("task-state scenario: id is required")
	}
	if s.Limit <= 0 {
		return fmt.Errorf("task-state scenario %s: limit must be positive", s.ID)
	}
	if len(s.Turns) == 0 || len(s.Questions) == 0 {
		return fmt.Errorf("task-state scenario %s: needs turns and questions", s.ID)
	}
	for i, t := range s.Turns {
		if !taskstate.Kind(t.Kind).Valid() || strings.TrimSpace(t.Text) == "" {
			return fmt.Errorf("task-state scenario %s: turn %d is invalid", s.ID, i)
		}
	}
	for _, q := range s.Questions {
		if q.ID == "" || len(q.Needs) == 0 {
			return fmt.Errorf("task-state scenario %s: question needs an id and needs", s.ID)
		}
		for _, n := range q.Needs {
			hits := 0
			for _, t := range s.Turns {
				if containsFold(t.Text, n) {
					hits++
				}
			}
			if hits != 1 {
				return fmt.Errorf("task-state scenario %s: need %q matches %d turns, want 1", s.ID, n, hits)
			}
		}
	}
	return nil
}

// LoadTaskStateScenarios reads every *.yaml in dir, strictly.
func LoadTaskStateScenarios(dir string) ([]TaskStateScenario, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	var out []TaskStateScenario
	for _, p := range paths {
		data, err := os.ReadFile(p) //nolint:gosec // repo-local fixture path
		if err != nil {
			return nil, err
		}
		var s TaskStateScenario
		dec := yaml.NewDecoder(strings.NewReader(string(data)))
		dec.KnownFields(true)
		if err := dec.Decode(&s); err != nil {
			return nil, fmt.Errorf("brainbench: %s: %w", p, err)
		}
		if err := s.Validate(); err != nil {
			return nil, fmt.Errorf("brainbench: %s: %w", p, err)
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("brainbench: no task-state scenarios in %s", dir)
	}
	return out, nil
}

// taskStateArm is one way of running working memory.
type taskStateArm struct {
	name     string
	fifo     bool // today's AppendBlock path
	gate     bool
	eviction taskstate.Eviction
}

var taskStateArms = []taskStateArm{
	{name: "control", fifo: true},
	{name: "control_replica", fifo: true},
	{name: "gated", gate: true, eviction: taskstate.EvictByPriority},
	{name: "gate_only", gate: true, eviction: taskstate.EvictOldest},
	{name: "priority_only", eviction: taskstate.EvictByPriority},
}

// TaskStateArmResult is what one arm held at question time.
type TaskStateArmResult struct {
	Arm string `json:"arm"`
	// NeededInBlock: fraction of (question, need) pairs present in the block.
	NeededInBlock float64 `json:"needed_in_block"`
	// ImplicitInBlock: the same over implicit questions; nil when none.
	ImplicitInBlock *float64 `json:"implicit_in_block"`
	// NeededAvailable: present in the block OR already in long-term memory.
	NeededAvailable float64 `json:"needed_available"`
	// MeanOffset: mean relative position (0 = start) of needs found. Descriptive.
	MeanOffset *float64 `json:"mean_offset"`
	BlockChars int      `json:"block_chars"`
	Admitted   int      `json:"admitted"`
	Rejected   int      `json:"rejected"`
	Evicted    int      `json:"evicted"`
	// HandedOff: candidate claims the handoff left in the store. Descriptive.
	HandedOff int `json:"handed_off"`
	// HandoffNeeded: fraction of needs that reached long-term memory through
	// the handoff episode. Descriptive; the control has no handoff.
	HandoffNeeded float64 `json:"handoff_needed"`
}

// TaskStateResult is one scenario's outcome.
type TaskStateResult struct {
	Scenario    string               `json:"scenario"`
	Description string               `json:"description"`
	OfferedChar int                  `json:"offered_chars"`
	Arms        []TaskStateArmResult `json:"arms"`
	NoiseFloor  float64              `json:"noise_floor"`
	// Verdict compares gated against control on the scored metrics:
	// improved | regressed | mixed | no-effect | invalid.
	Verdict string `json:"verdict"`
}

// TaskStateReport is the whole task-state experiment.
type TaskStateReport struct {
	Results     []TaskStateResult `json:"results"`
	Limitations []string          `json:"limitations"`
}

// RunTaskState runs every scenario through every arm under workDir.
func RunTaskState(ctx context.Context, scenarios []TaskStateScenario, workDir string) (TaskStateReport, error) {
	rep := TaskStateReport{Limitations: TaskStateLimitations}
	for _, sc := range scenarios {
		res, err := runTaskStateScenario(ctx, sc, workDir)
		if err != nil {
			return TaskStateReport{}, err
		}
		rep.Results = append(rep.Results, res)
	}
	return rep, nil
}

const taskStateOwner = "brainbench"

func runTaskStateScenario(ctx context.Context, sc TaskStateScenario, workDir string) (TaskStateResult, error) {
	dir := filepath.Join(workDir, "taskstate-"+sc.ID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return TaskStateResult{}, err
	}
	pristine := filepath.Join(dir, "pristine.db")
	if err := seedLongTerm(ctx, "sqlite://"+pristine, sc); err != nil {
		return TaskStateResult{}, err
	}
	res := TaskStateResult{Scenario: sc.ID, Description: sc.Description}
	for _, t := range sc.Turns {
		res.OfferedChar += len(itemLine(t))
	}
	for _, arm := range taskStateArms {
		path := filepath.Join(dir, arm.name+".db")
		if err := copyDB(pristine, path); err != nil {
			return TaskStateResult{}, err
		}
		r, err := runTaskStateArm(ctx, "sqlite://"+path, sc, arm)
		if err != nil {
			return TaskStateResult{}, fmt.Errorf("brainbench: task-state %s/%s: %w", sc.ID, arm.name, err)
		}
		res.Arms = append(res.Arms, r)
	}
	res.NoiseFloor = scoredDistance(res.Arms[0], res.Arms[1])
	res.Verdict = taskStateVerdict(res.Arms[0], res.Arms[2], res.NoiseFloor)
	return res, nil
}

// seedLongTerm writes the scenario's long-term beliefs, one episode each.
func seedLongTerm(ctx context.Context, dsn string, sc TaskStateScenario) error {
	conn, err := store.Open(ctx, dsn)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, text := range sc.LongTerm {
		id := fmt.Sprintf("%s_ltm_%d", sc.ID, i)
		ev := domain.Event{ID: "ev_" + id, RunID: "brainbench", Content: text, SourceInputID: id, Timestamp: at, IngestedAt: at, CreatedBy: SeedActor}
		c := domain.Claim{ID: id, Text: text, Type: domain.ClaimTypeFact, Confidence: 0.9, Status: domain.ClaimStatusActive, CreatedAt: at, ValidFrom: at, CreatedBy: SeedActor}
		if err := pipeline.PersistArtifacts(ctx, conn, []domain.Event{ev}, []domain.Claim{c}, []domain.ClaimEvidence{{ClaimID: id, EventID: ev.ID}}, nil); err != nil {
			return fmt.Errorf("brainbench: seed long-term %s: %w", id, err)
		}
	}
	return nil
}

func itemLine(t TaskStateTurn) string { return "- [" + t.Kind + "] " + t.Text }

// admitAll is the priority_only ablation's probe: everything is new.
type admitAll struct{}

// Assess reports every statement as new to long-term memory.
func (admitAll) Assess(context.Context, string) (taskstate.Verdict, error) {
	return taskstate.Verdict{}, nil
}

// Missing reports every claim id as present.
func (admitAll) Missing(context.Context, []string) ([]string, error) { return nil, nil }

func runTaskStateArm(ctx context.Context, dsn string, sc TaskStateScenario, arm taskStateArm) (TaskStateArmResult, error) {
	r := TaskStateArmResult{Arm: arm.name}
	var block string
	var handoffText []string
	if arm.fifo {
		b, err := runFIFO(ctx, dsn, sc)
		if err != nil {
			return r, err
		}
		block, r.Admitted = b, len(sc.Turns)
		// AppendBlock does not report evictions; count the lines it dropped.
		for _, t := range sc.Turns {
			if !strings.Contains(block, itemLine(t)) {
				r.Evicted++
			}
		}
	} else {
		out, err := runGated(ctx, dsn, sc, arm)
		if err != nil {
			return r, err
		}
		block, handoffText = out.block, out.handoff
		r.Admitted, r.Rejected, r.Evicted, r.HandedOff = out.admitted, out.rejected, out.evicted, out.handedOff
	}
	r.BlockChars = len(block)
	scoreNeeds(&r, sc, block, handoffText)
	return r, nil
}

// runFIFO replays the session through today's AppendBlock.
func runFIFO(ctx context.Context, dsn string, sc TaskStateScenario) (string, error) {
	mem, err := mnemos.New(mnemos.WithStorage(dsn), mnemos.WithPassiveMode(), mnemos.WithActor(SeedActor))
	if err != nil {
		return "", err
	}
	defer func() { _ = mem.Close() }()
	for _, t := range sc.Turns {
		if err := mem.AppendBlock(ctx, taskStateOwner, "task_state_fifo", itemLine(t)); err != nil {
			return "", err
		}
	}
	blocks, err := mem.Blocks(ctx, taskStateOwner)
	if err != nil || len(blocks) == 0 {
		return "", err
	}
	return blocks[0].Value, nil
}

type gatedOutcome struct {
	block                                  string
	handoff                                []string
	admitted, rejected, evicted, handedOff int
}

// runGated replays the session through the task-state service, then hands it
// off through the governed writer and reads back what the store kept.
func runGated(ctx context.Context, dsn string, sc TaskStateScenario, arm taskStateArm) (gatedOutcome, error) {
	var out gatedOutcome
	conn, err := store.Open(ctx, dsn)
	if err != nil {
		return out, err
	}
	defer func() { _ = conn.Close() }()
	var probe taskstate.Probe = admitAll{}
	if arm.gate {
		probe = taskstate.StoreProbe{Conn: conn}
	}
	svc := taskstate.Service{Blocks: conn.Blocks, Probe: probe, Limit: sc.Limit, Eviction: arm.eviction}
	for _, t := range sc.Turns {
		res, err := svc.Admit(ctx, taskStateOwner, sc.ID, taskstate.Item{Kind: taskstate.Kind(t.Kind), Text: t.Text})
		if err != nil {
			return out, err
		}
		if res.Decision.Admit {
			out.admitted++
		} else {
			out.rejected++
		}
		out.evicted += len(res.Evicted)
	}
	st, err := svc.Load(ctx, taskStateOwner, sc.ID)
	if err != nil {
		return out, err
	}
	out.block = taskstate.Render(st)

	w, err := govwrite.Wrap(conn, nil)
	if err != nil {
		return out, err
	}
	h, err := svc.HandOff(ctx, w, taskStateOwner, sc.ID, SeedActor)
	if err != nil {
		return out, err
	}
	stored, err := conn.Claims.ListByIDs(ctx, h.Candidates)
	if err != nil {
		return out, err
	}
	for _, c := range stored {
		if c.Lifecycle == domain.ClaimLifecycleCandidate {
			out.handedOff++
		}
	}
	if h.EventID != "" {
		ev, err := conn.Events.GetByID(ctx, h.EventID)
		if err != nil {
			return out, err
		}
		out.handoff = append(out.handoff, ev.Content)
	}
	return out, nil
}

func scoreNeeds(r *TaskStateArmResult, sc TaskStateScenario, block string, handoff []string) {
	var total, inBlock, available, implicitTotal, implicitIn, handed int
	var offsets []float64
	for _, q := range sc.Questions {
		for _, n := range q.Needs {
			total++
			idx := strings.Index(strings.ToLower(block), strings.ToLower(n))
			found := idx >= 0
			if found {
				inBlock++
				if len(block) > 0 {
					offsets = append(offsets, float64(idx)/float64(len(block)))
				}
			}
			if found || anyContainsFold(sc.LongTerm, n) {
				available++
			}
			if q.Implicit {
				implicitTotal++
				if found {
					implicitIn++
				}
			}
			if anyContainsFold(handoff, n) {
				handed++
			}
		}
	}
	r.NeededInBlock = ratio(inBlock, total)
	r.NeededAvailable = ratio(available, total)
	r.HandoffNeeded = ratio(handed, total)
	if implicitTotal > 0 {
		v := ratio(implicitIn, implicitTotal)
		r.ImplicitInBlock = &v
	}
	if len(offsets) > 0 {
		sum := 0.0
		for _, o := range offsets {
			sum += o
		}
		m := sum / float64(len(offsets))
		r.MeanOffset = &m
	}
}

// scored returns the scored metrics; an unmeasurable one is absent, never 0.
func scored(r TaskStateArmResult) map[string]float64 {
	m := map[string]float64{"needed_in_block": r.NeededInBlock, "needed_available": r.NeededAvailable}
	if r.ImplicitInBlock != nil {
		m["implicit_in_block"] = *r.ImplicitInBlock
	}
	return m
}

func scoredDistance(a, b TaskStateArmResult) float64 {
	worst := 0.0
	sb := scored(b)
	for k, v := range scored(a) {
		if d := abs(v - sb[k]); d > worst {
			worst = d
		}
	}
	return worst
}

func taskStateVerdict(control, treated TaskStateArmResult, noise float64) string {
	if noise > 0 {
		return "invalid"
	}
	up, down := 0, 0
	st := scored(treated)
	for k, v := range scored(control) {
		switch d := st[k] - v; {
		case d > noise:
			up++
		case d < -noise:
			down++
		}
	}
	switch {
	case up > 0 && down > 0:
		return "mixed"
	case up > 0:
		return "improved"
	case down > 0:
		return "regressed"
	default:
		return "no-effect"
	}
}

func anyContainsFold(texts []string, sub string) bool {
	for _, t := range texts {
		if containsFold(t, sub) {
			return true
		}
	}
	return false
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// WriteJSON writes the machine-readable report.
func (r TaskStateReport) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WriteHuman writes one table per scenario, then the limitations.
func (r TaskStateReport) WriteHuman(w io.Writer) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, res := range r.Results {
		_, _ = fmt.Fprintf(tw, "\n%s — %s\noffered %d chars; verdict (gated vs control): %s; noise floor %.3f\n",
			res.Scenario, res.Description, res.OfferedChar, res.Verdict, res.NoiseFloor)
		_, _ = fmt.Fprintln(tw, "arm\tneeded_in_block\timplicit_in_block\tneeded_available\tmean_offset\tchars\tadmitted\trejected\tevicted\thanded_off\thandoff_needed")
		for _, a := range res.Arms {
			_, _ = fmt.Fprintf(tw, "%s\t%.3f\t%s\t%.3f\t%s\t%d\t%d\t%d\t%d\t%d\t%.3f\n",
				a.Arm, a.NeededInBlock, optional(a.ImplicitInBlock), a.NeededAvailable, optional(a.MeanOffset),
				a.BlockChars, a.Admitted, a.Rejected, a.Evicted, a.HandedOff, a.HandoffNeeded)
		}
	}
	_, _ = fmt.Fprintln(tw, "\nlimitations:")
	for _, l := range r.Limitations {
		_, _ = fmt.Fprintf(tw, "  - %s\n", l)
	}
	// tabwriter buffers: a write error surfaces here.
	return tw.Flush()
}

func optional(v *float64) string {
	if v == nil {
		return "unknown"
	}
	return fmt.Sprintf("%.3f", *v)
}
