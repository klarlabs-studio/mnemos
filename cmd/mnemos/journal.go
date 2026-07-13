package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// handleJournal reads the append-only cognitive journal (ADR 0018): the record of what
// the learning mechanisms did over time, for research + constant tuning. Default view
// is the per-pass consolidation stream (the free-energy-over-time curve);
// `--belief <id>` shows one belief's trust trajectory. Read-only.
//
//	mnemos journal [--limit N] [--kind consolidation|belief_trust|health] [--belief <claim-id>] [--human]
func handleJournal(args []string, f Flags) {
	limit := 50
	belief := ""
	kind := domain.JournalKindConsolidation
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
		case "--kind":
			if i+1 >= len(args) {
				exitWithMnemosError(false, NewUserError("--kind requires a value (consolidation|belief_trust|health)"))
				return
			}
			kind = args[i+1]
			i++
		case "--belief":
			if i+1 >= len(args) {
				exitWithMnemosError(false, NewUserError("--belief requires a claim id"))
				return
			}
			belief = args[i+1]
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
	if conn.Journal == nil {
		exitWithMnemosError(false, NewSystemError(fmt.Errorf("this backend has no cognitive journal"), "journal"))
		return
	}

	var entries []domain.JournalEntry
	if belief != "" {
		entries, err = conn.Journal.ListBySubject(ctx, belief, limit)
	} else {
		entries, err = conn.Journal.List(ctx, kind, limit)
	}
	if err != nil {
		exitWithMnemosError(f.Verbose, NewSystemError(err, "read journal"))
		return
	}

	if f.Human {
		printJournalHuman(entries)
		return
	}
	out := make([]journalEntryOut, 0, len(entries))
	for _, e := range entries {
		data := json.RawMessage(e.Data)
		if len(data) == 0 {
			data = json.RawMessage("{}")
		}
		out = append(out, journalEntryOut{
			ID: e.ID, At: e.At, RunID: e.RunID, Kind: e.Kind, SubjectID: e.SubjectID, Data: data,
		})
	}
	emitJSON(out)
}

// journalEntryOut is the JSON export shape: Data is emitted as raw nested JSON rather
// than an escaped string, so research tooling reads it directly.
type journalEntryOut struct {
	ID        string          `json:"id"`
	At        time.Time       `json:"at"`
	RunID     string          `json:"run_id,omitempty"`
	Kind      string          `json:"kind"`
	SubjectID string          `json:"subject_id,omitempty"`
	Data      json.RawMessage `json:"data"`
}

func printJournalHuman(entries []domain.JournalEntry) {
	if len(entries) == 0 {
		fmt.Println("Cognitive journal is empty (run `consolidate --journal` to record a pass).")
		return
	}
	fmt.Println("Cognitive journal (ADR 0018) — newest first")
	fmt.Println()
	for _, e := range entries {
		ts := e.At.Format("2006-01-02 15:04:05")
		switch e.Kind {
		case domain.JournalKindConsolidation:
			var pass struct {
				Result struct {
					Credited, Forgotten, Refuted, Validated, Replayed, AssociationsDecayed, InhibitionDecayed int
					PlasticityGain                                                                            float64
				} `json:"result"`
				PredictiveError struct {
					Total   float64 `json:"total"`
					Hotspot string  `json:"hotspot"`
				} `json:"predictive_error"`
			}
			_ = json.Unmarshal([]byte(e.Data), &pass)
			hotspot := pass.PredictiveError.Hotspot
			if hotspot == "" {
				hotspot = "—"
			}
			fmt.Printf("%s  pass   free-energy=%.3f hotspot=%-11s  credited=%d forgotten=%d validated=%d replayed=%d assoc_decayed=%d inhib_decayed=%d gain=%.2f\n",
				ts, pass.PredictiveError.Total, hotspot,
				pass.Result.Credited, pass.Result.Forgotten, pass.Result.Validated, pass.Result.Replayed,
				pass.Result.AssociationsDecayed, pass.Result.InhibitionDecayed, pass.Result.PlasticityGain)
		case domain.JournalKindBeliefTrust:
			var bt struct {
				Before float64 `json:"before"`
				After  float64 `json:"after"`
				Delta  float64 `json:"delta"`
			}
			_ = json.Unmarshal([]byte(e.Data), &bt)
			fmt.Printf("%s  trust  %-24s  %.4f -> %.4f  (Δ %+.4f)\n", ts, e.SubjectID, bt.Before, bt.After, bt.Delta)
		case domain.JournalKindHealth:
			var h struct {
				Status string `json:"status"`
				Vitals []struct {
					Name  string  `json:"name"`
					Value float64 `json:"value"`
				} `json:"vitals"`
			}
			_ = json.Unmarshal([]byte(e.Data), &h)
			fe := 0.0
			for _, v := range h.Vitals {
				if v.Name == "free_energy" {
					fe = v.Value
				}
			}
			fmt.Printf("%s  health %-11s  free-energy=%.3f\n", ts, h.Status, fe)
		default:
			fmt.Printf("%s  %-6s %s  %s\n", ts, e.Kind, e.SubjectID, e.Data)
		}
	}
}
