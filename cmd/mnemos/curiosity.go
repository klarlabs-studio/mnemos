package main

import (
	"context"
	"fmt"
	"strconv"
	"time"

	mnemos "go.klarlabs.de/mnemos"
	"go.klarlabs.de/mnemos/internal/curiosity"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
	"go.klarlabs.de/mnemos/internal/trust"
)

// handleCuriosity implements `mnemos curiosity` — the gap-driven active
// acquisition queue of ADR 0013 §3. Mnemos already computes what it does not
// know (knowledge-gaps, calibration, trust/staleness) but does nothing with it;
// this turns that passive metacognition into a prioritized "what to learn /
// verify next" list. It is strictly read-only: it surfaces the queue; acting on
// it (re-verify, go gather evidence) is the agent's job.
//
// It reuses the existing computations rather than reinventing them: the
// unresolved-hypothesis classification comes from Memory.KnowledgeGaps, the
// source over-confidence bump from Memory.Calibration, and salience/staleness
// from internal/trust. Ranking lives in internal/curiosity.
func handleCuriosity(args []string, f Flags) {
	limit := 20
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--limit":
			if i+1 >= len(args) {
				exitWithMnemosError(false, NewUserError("--limit requires a value"))
				return
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n <= 0 {
				exitWithMnemosError(false, NewUserError("invalid --limit %q: want a positive integer", args[i+1]))
				return
			}
			limit = n
			i++
		default:
			exitWithMnemosError(false, NewUserError("unknown argument %q", args[i]))
			return
		}
	}

	ctx := context.Background()
	conn, err := openConn(ctx)
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "open store"))
		return
	}
	defer closeConn(conn)

	beliefs, cal, err := gatherCuriositySignals(ctx, conn)
	if err != nil {
		exitWithMnemosError(f.Verbose, err)
		return
	}

	queue := curiosity.Prioritize(beliefs, limit)

	if f.Human {
		printCuriosityHuman(queue, cal)
		return
	}
	emitJSON(curiosityEnvelope(queue, cal))
}

// curiosityCalibration is the aggregate calibration context echoed alongside the
// queue — a caller reads it to gauge how much to trust stated confidence overall.
type curiosityCalibration struct {
	Samples int     `json:"samples"`
	ECE     float64 `json:"ece"`
	Brier   float64 `json:"brier"`
}

func curiosityEnvelope(queue []curiosity.Item, cal curiosityCalibration) map[string]any {
	// Never emit a null JSON array — an empty store yields [] not null.
	if queue == nil {
		queue = []curiosity.Item{}
	}
	return map[string]any{
		"queue":       queue,
		"count":       len(queue),
		"calibration": cal,
	}
}

// gatherCuriositySignals assembles the per-belief acquisition signals from the
// store's existing surfaces, reusing Memory.KnowledgeGaps (unresolved
// hypotheses), Memory.Calibration (source over-confidence) and internal/trust
// (salience, staleness). Read-only.
func gatherCuriositySignals(ctx context.Context, conn *store.Conn) ([]curiosity.Belief, curiosityCalibration, error) {
	claims, err := conn.Claims.ListAll(ctx)
	if err != nil {
		return nil, curiosityCalibration{}, NewSystemError(err, "list claims")
	}
	evidence, err := conn.Claims.ListAllEvidence(ctx)
	if err != nil {
		return nil, curiosityCalibration{}, NewSystemError(err, "list evidence")
	}
	evidenceCount := make(map[string]int, len(claims))
	for _, e := range evidence {
		evidenceCount[e.ClaimID]++
	}

	// Contradiction density per claim (claim↔claim graph), same source the gap
	// detector reads.
	rels, err := conn.Relationships.ListAll(ctx)
	if err != nil {
		return nil, curiosityCalibration{}, NewSystemError(err, "list relationships")
	}
	contradicts := map[string]int{}
	for _, r := range rels {
		if r.Type != domain.RelationshipTypeContradicts {
			continue
		}
		contradicts[r.FromClaimID]++
		contradicts[r.ToClaimID]++
	}

	// The library facade over the same DSN gives us the reusable gap + calibration
	// computations. Both are advisory here: if the facade cannot be built or a
	// read fails, we degrade to the trust/staleness signals alone rather than
	// failing the command.
	unresolved := map[string]struct{}{}
	var cal curiosityCalibration
	overconfidence := map[string]float64{}
	if mem, merr := newLibraryMemory(ctx, ""); merr == nil {
		defer func() { _ = mem.Close() }()
		if gaps, gerr := mem.KnowledgeGaps(ctx, 0); gerr == nil {
			for _, g := range gaps {
				if g.Kind == mnemos.GapUnresolvedHypothesis {
					unresolved[g.ClaimID] = struct{}{}
				}
			}
		}
		if c, cerr := mem.Calibration(ctx); cerr == nil {
			cal = curiosityCalibration{Samples: c.Samples, ECE: c.ECE, Brier: c.Brier}
			for _, s := range c.Sources {
				if s.Gap > 0 { // positive gap = over-confident author
					overconfidence[s.Source] = s.Gap
				}
			}
		}
	}

	now := time.Now().UTC()
	beliefs := make([]curiosity.Belief, 0, len(claims))
	for _, c := range claims {
		if !c.ValidTo.IsZero() {
			continue // only live knowledge
		}
		_, isUnresolved := unresolved[c.ID]
		cc := contradicts[c.ID]
		beliefs = append(beliefs, curiosity.Belief{
			ClaimID:              c.ID,
			Text:                 c.Text,
			Trust:                c.TrustScore,
			Salience:             trust.SalienceOf(c, evidenceCount[c.ID]),
			Staleness:            trust.Staleness(c.ValidFrom, c.LastVerified, now, c.HalfLifeDays),
			StaleDays:            staleDays(c, now),
			Contradictions:       cc,
			Contested:            cc >= mnemos.GapContestedThreshold,
			Unresolved:           isUnresolved,
			Stale:                trust.IsStale(c.ValidFrom, c.LastVerified, now, c.HalfLifeDays, 0),
			SourceOverconfidence: overconfidence[c.CreatedBy],
		})
	}
	return beliefs, cal, nil
}

// staleDays returns the age in whole days of a claim's freshest signal
// (last-verified, else valid-from). 0 when there is no usable timestamp.
func staleDays(c domain.Claim, now time.Time) int {
	ref := c.ValidFrom
	if c.LastVerified.After(ref) {
		ref = c.LastVerified
	}
	if ref.IsZero() {
		return 0
	}
	d := int(now.Sub(ref).Hours() / 24)
	if d < 0 {
		return 0
	}
	return d
}

func printCuriosityHuman(queue []curiosity.Item, cal curiosityCalibration) {
	if len(queue) == 0 {
		fmt.Println("Curiosity queue: empty — nothing uncertain, stale, or contested to learn next.")
		return
	}
	fmt.Printf("Curiosity queue — what to learn / verify next (%d):\n", len(queue))
	for i, it := range queue {
		fmt.Printf("%d. [%s p=%.3f] %s\n", i+1, it.Action, it.Priority, it.Text)
		fmt.Printf("     why: %s (trust %.2f", reasonsLabel(it.Reasons), it.Trust)
		if it.Contradictions > 0 {
			fmt.Printf(", contradicted by %d", it.Contradictions)
		}
		if it.StaleDays > 0 {
			fmt.Printf(", stale %dd", it.StaleDays)
		}
		fmt.Printf(")\n")
		fmt.Printf("     ask: %s\n", it.Question)
	}
	if cal.Samples > 0 {
		fmt.Printf("\nCalibration: %d adjudicated, ECE %.3f, Brier %.3f\n", cal.Samples, cal.ECE, cal.Brier)
	}
}

func reasonsLabel(reasons []string) string {
	out := ""
	for i, r := range reasons {
		if i > 0 {
			out += ", "
		}
		out += r
	}
	return out
}
