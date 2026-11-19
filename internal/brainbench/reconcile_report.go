package brainbench

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
)

// WriteJSON writes the machine-readable report.
// Numbers are rounded to reportDecimals places: a ratio printed at full float64
// precision is noise past the fourth decimal, and its 16-digit runs pass the
// Luhn check, so the security gate reads them as card numbers.
func (r ReconcileReport) WriteJSON(w io.Writer) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		return err
	}
	_, err := w.Write(RoundJSONNumbers(buf.Bytes()))
	return err
}

const reportDecimals = 4

// longNumber matches a JSON number with more than reportDecimals fractional
// digits, outside a string: preceded by ": ", "[", "," or indentation.
var longNumber = regexp.MustCompile(`([:\[,]\s*|\n\s+)(-?\d+\.\d{5,}(?:[eE][-+]?\d+)?)`)

// RoundJSONNumbers rounds every number in an encoded JSON document that has
// more than reportDecimals fractional digits. String values are untouched: a
// number inside a string is never preceded by a JSON separator.
func RoundJSONNumbers(doc []byte) []byte {
	return longNumber.ReplaceAllFunc(doc, func(m []byte) []byte {
		sub := longNumber.FindSubmatch(m)
		v, err := strconv.ParseFloat(string(sub[2]), 64)
		if err != nil {
			return m
		}
		out := strconv.FormatFloat(v, 'f', reportDecimals, 64)
		out = strings.TrimRight(strings.TrimRight(out, "0"), ".")
		if out == "" || out == "-" {
			out = "0"
		}
		return append(append([]byte(nil), sub[1]...), out...)
	})
}

func fmtRatio(v *float64) string {
	if v == nil {
		return "unknown"
	}
	return fmt.Sprintf("%.3f", *v)
}

func fmtCI(iv *Interval) string {
	if iv == nil {
		return "[unknown]"
	}
	return fmt.Sprintf("[%.3f, %.3f]", iv.Lo, iv.Hi)
}

func armName(a ArmResult) string {
	if a.Model == "" {
		return a.Arm
	}
	return a.Arm + " (" + a.Model + ")"
}

// WriteHuman writes the results table, costs, hard cases and limitations.
func (r ReconcileReport) WriteHuman(w io.Writer) error {
	var b strings.Builder
	d := r.Dataset
	fmt.Fprintf(&b, "Reconciliation: %s\n\nHypothesis: %s\nSuccess: %s\n", r.Experiment, r.Hypothesis, r.Success)
	fmt.Fprintf(&b, "Relate: %s   Prompt: %s   Bootstrap: %d resamples, seed %d\n", r.RelateModelVersion, r.PromptVersion, r.BootstrapIters, r.Seed)
	fmt.Fprintf(&b, "Routing (fixed before results): %s\n\n", r.RoutingRule)
	fmt.Fprintf(&b, "Pairs: %d (%d contradicts, %d compatible; %d hand-written). Split: %d dev, %d test (%d contradicts in test).\n\n",
		d.Total, d.Contradicts, d.Compatible, d.Hand, d.Dev, d.Test, d.TestContradicts)
	if !r.LLMRun {
		b.WriteString("LLM arms NOT RUN (no -llm-model): only the deterministic arm is scored. This is not a zero result.\n\n")
	}

	b.WriteString("Test split, positive class = contradicts (95% bootstrap CI)\n")
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "arm\tP\tP CI\tR\tR CI\tF1\tF1 CI\tTP/FP/FN/TN\tΔF1 vs det [CI]")
	for _, a := range r.Arms {
		m := a.Test
		delta := "-"
		if a.DeltaF1 != nil {
			delta = fmt.Sprintf("%+.3f %s", *a.DeltaF1, fmtCI(a.DeltaF1CI))
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d/%d/%d/%d\t%s\n", armName(a),
			fmtRatio(m.Precision), fmtCI(m.PrecisionCI), fmtRatio(m.Recall), fmtCI(m.RecallCI),
			fmtRatio(m.F1), fmtCI(m.F1CI), m.TP, m.FP, m.FN, m.TN, delta)
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	b.WriteString("\nDev split and all pairs (F1)\n")
	tw = tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "arm\tdev P/R/F1\tdev TP/FP/FN/TN\tall P/R/F1\tall TP/FP/FN/TN")
	for _, a := range r.Arms {
		_, _ = fmt.Fprintf(tw, "%s\t%s/%s/%s\t%d/%d/%d/%d\t%s/%s/%s\t%d/%d/%d/%d\n", armName(a),
			fmtRatio(a.Dev.Precision), fmtRatio(a.Dev.Recall), fmtRatio(a.Dev.F1), a.Dev.TP, a.Dev.FP, a.Dev.FN, a.Dev.TN,
			fmtRatio(a.All.Precision), fmtRatio(a.All.Recall), fmtRatio(a.All.F1), a.All.TP, a.All.FP, a.All.FN, a.All.TN)
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	b.WriteString("\nCost on the test split (spend is 0: local models)\n")
	tw = tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "arm\tdet ms\tLLM calls\tLLM wall s\tp50 ms\tp95 ms\tprompt tok\tcompl tok\terrors\tunparsed")
	for _, a := range r.Arms {
		c := a.CostTest
		tok := func(n int) string {
			if c.LLMCalls == 0 {
				return "-"
			}
			if !c.TokensReported {
				return "not reported"
			}
			return fmt.Sprint(n)
		}
		_, _ = fmt.Fprintf(tw, "%s\t%.1f\t%d\t%.1f\t%.0f\t%.0f\t%s\t%s\t%d\t%d\n", armName(a), c.DeterministicMS,
			c.LLMCalls, c.LLMWallMS/1000, c.LLMP50MS, c.LLMP95MS, tok(c.PromptTokens), tok(c.CompletionTokens), c.CallErrors, c.Unparsed)
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	for _, a := range r.Arms {
		if a.RouteCounts == nil {
			continue
		}
		keys := make([]string, 0, len(a.RouteCounts))
		for k := range a.RouteCounts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s=%d", k, a.RouteCounts[k]))
		}
		fmt.Fprintf(&b, "\nHybrid routes (all pairs): %s\n", strings.Join(parts, " "))
		break
	}

	b.WriteString("\nHand-written hard cases turned into contradiction edges (all splits)\n")
	tw = tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "arm\tcontradicts edged\tcompatible edged (false)")
	for _, a := range r.Arms {
		h := a.HardCases
		_, _ = fmt.Fprintf(tw, "%s\t%d/%d\t%d/%d\n", armName(a), h.ContradictsEdged, h.Contradicts, h.CompatibleEdged, h.Compatible)
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	b.WriteString("\nPer category, correct/n (all splits)\n")
	tw = tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprint(tw, "category\tn\tcontra")
	for _, a := range r.Arms {
		_, _ = fmt.Fprintf(tw, "\t%s", armName(a))
	}
	_, _ = fmt.Fprintln(tw)
	if len(r.Arms) > 0 {
		for ci, c := range r.Arms[0].Categories {
			name := c.Category
			if c.Hand {
				name += " (hand)"
			}
			_, _ = fmt.Fprintf(tw, "%s\t%d\t%d", name, c.N, c.Labelled)
			for _, a := range r.Arms {
				_, _ = fmt.Fprintf(tw, "\t%d/%d", a.Categories[ci].Correct, a.Categories[ci].N)
			}
			_, _ = fmt.Fprintln(tw)
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	if len(r.ThresholdSweep) > 0 {
		b.WriteString("\nDescriptive dev-split threshold sweep (did NOT choose the threshold)\n")
		models := make([]string, 0, len(r.ThresholdSweep))
		for m := range r.ThresholdSweep {
			models = append(models, m)
		}
		sort.Strings(models)
		for _, m := range models {
			parts := []string{}
			for _, p := range r.ThresholdSweep[m] {
				parts = append(parts, fmt.Sprintf("%.2f→F1 %s (%d calls)", p.Threshold, fmtRatio(p.F1), p.LLMCalls))
			}
			fmt.Fprintf(&b, "  %s: %s\n", m, strings.Join(parts, "; "))
		}
	}

	b.WriteString("\nLimitations\n")
	for _, l := range r.Limitations {
		fmt.Fprintf(&b, "  - %s\n", l)
	}
	_, err := io.WriteString(w, b.String())
	return err
}
