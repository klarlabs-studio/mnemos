package brainbench

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// outcomeMark is the one-character form of an outcome in the per-question grid.
func outcomeMark(o Outcome) string {
	switch o {
	case OutcomeSuccess:
		return "+"
	case OutcomeGoldOverBudget:
		return "b"
	case OutcomeGoldNotRetrieved:
		return "m"
	case OutcomeForbidden:
		return "f"
	default:
		return "?"
	}
}

// WriteJSON writes the machine-readable report.
func (r AblationReport) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WriteHuman writes the success table, the component effects and the
// per-question outcomes.
func (r AblationReport) WriteHuman(w io.Writer) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Retrieval ablation\n\nHypothesis: %s\nPass condition: %s\nSuccess: %s\n\n",
		r.Hypothesis, r.PassCondition, r.Success)
	if !r.Deterministic {
		fmt.Fprintf(&b, "INVALID: two copies of %s ranked these questions differently, so no difference below is attributable: %s\n\n",
			PolicyDefault, strings.Join(r.Drifting, ", "))
	}
	n := len(r.Questions)
	fmt.Fprintf(&b, "Questions: %d. A cell is successes/%d; [k] is the number of questions whose ranked list the budget cut.\n\n", n, n)

	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprint(tw, "policy\tkind")
	for _, bud := range r.Budgets {
		_, _ = fmt.Fprintf(tw, "\t%d", bud)
	}
	_, _ = fmt.Fprintln(tw)
	for _, p := range r.Policies {
		_, _ = fmt.Fprintf(tw, "%s\t%s", p.Policy.Name, p.Policy.Kind)
		for _, c := range p.Cells {
			_, _ = fmt.Fprintf(tw, "\t%d/%d [%d]", c.Successes, c.N, c.Binding)
		}
		_, _ = fmt.Fprintln(tw)
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	b.WriteString("\nAgainst mnemos_default (delta in successes; lost = default succeeds and this policy fails).\n")
	b.WriteString("Budgets with the same outcome are merged into one range:\n")
	etw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	for _, run := range mergeEffects(r.Effects) {
		e := run.effect
		if e.Delta == 0 && len(e.Lost) == 0 && len(e.Gained) == 0 {
			continue
		}
		_, _ = fmt.Fprintf(etw, "  %s\t@%s\t%+d", e.Policy, run.budgets(), e.Delta)
		if len(e.Lost) > 0 {
			_, _ = fmt.Fprintf(etw, "\tlost %s", strings.Join(sortedFlipKeys(e.Lost), " "))
		}
		if len(e.Gained) > 0 {
			_, _ = fmt.Fprintf(etw, "\tgained %s", strings.Join(sortedFlipKeys(e.Gained), " "))
		}
		_, _ = fmt.Fprintln(etw)
	}
	if err := etw.Flush(); err != nil {
		return err
	}
	b.WriteString("  (a policy and budget not listed is identical to mnemos_default on every question)\n")

	b.WriteString("\nPer-question outcomes (+ success, b gold over budget, m gold not retrieved, f gold-invalid in context).\n")
	b.WriteString("Columns are policies in table order:\n")
	for i, p := range r.Policies {
		fmt.Fprintf(&b, "  %2d %s\n", i+1, p.Policy.Name)
	}
	for bi, bud := range r.Budgets {
		fmt.Fprintf(&b, "\nbudget %d\n", bud)
		tw := tabwriter.NewWriter(&b, 0, 0, 1, ' ', 0)
		_, _ = fmt.Fprint(tw, "question\tkind\t")
		for i := range r.Policies {
			_, _ = fmt.Fprintf(tw, "%2d ", i+1)
		}
		_, _ = fmt.Fprintln(tw)
		for _, q := range r.Questions {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t", q.Key, q.Kind)
			for _, p := range r.Policies {
				_, _ = fmt.Fprintf(tw, "%2s ", outcomeMark(p.Cells[bi].Outcomes[q.Key].Outcome))
			}
			_, _ = fmt.Fprintln(tw)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}

	b.WriteString("\nLimitations:\n")
	for _, l := range r.Limitations {
		fmt.Fprintf(&b, "  - %s\n", l)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// effectRun is consecutive budgets of one policy with the same flips.
type effectRun struct {
	effect      ComponentEffect
	first, last int
}

func (r effectRun) budgets() string {
	if r.first == r.last {
		return fmt.Sprint(r.first)
	}
	return fmt.Sprintf("%d-%d", r.first, r.last)
}

// mergeEffects merges a policy's consecutive budgets whose delta and flipped
// questions are identical, so a component that matters equally at every
// budget is one line rather than one per budget.
func mergeEffects(effects []ComponentEffect) []effectRun {
	var out []effectRun
	for _, e := range effects {
		if n := len(out); n > 0 {
			prev := &out[n-1]
			if prev.effect.Policy == e.Policy && sameFlips(prev.effect, e) {
				prev.last = e.Budget
				continue
			}
		}
		out = append(out, effectRun{effect: e, first: e.Budget, last: e.Budget})
	}
	return out
}

func sameFlips(a, b ComponentEffect) bool {
	return a.Delta == b.Delta &&
		strings.Join(sortedFlipKeys(a.Lost), " ") == strings.Join(sortedFlipKeys(b.Lost), " ") &&
		strings.Join(sortedFlipKeys(a.Gained), " ") == strings.Join(sortedFlipKeys(b.Gained), " ")
}
