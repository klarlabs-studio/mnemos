package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/extract"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/pipeline"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/workflow"
)

// handleEntities is the dispatcher for `mnemos entities <subcommand>`.
// Subcommands stay narrow: list, show, merge. Anything richer (add /
// rename / re-classify) is intentionally absent — the LLM extraction
// path is the canonical writer; the CLI exists for browsing and
// canonicalisation only.
func handleEntities(args []string, f Flags) {
	if len(args) == 0 {
		exitWithMnemosError(false, NewUserError("entities requires a subcommand: list, show, merge"))
		return
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		handleEntitiesList(rest, f)
	case "show":
		handleEntitiesShow(rest, f)
	case "merge":
		handleEntitiesMerge(rest, f)
	default:
		exitWithMnemosError(false, NewUserError("unknown entities subcommand %q (want list, show, merge)", sub))
	}
}

func handleEntitiesList(args []string, f Flags) {
	var typeFilter string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--type":
			if i+1 >= len(args) {
				exitWithMnemosError(false, NewUserError("--type requires a value"))
				return
			}
			typeFilter = strings.ToLower(args[i+1])
			i++
		default:
			exitWithMnemosError(false, NewUserError("unknown entities list flag %q", args[i]))
			return
		}
	}

	err := runJob("entities-list", map[string]string{"type": typeFilter}, f.Verbose, func(ctx context.Context, _ *workflow.Job, w *govwrite.Writer) error {
		conn := w.Conn()
		repo := conn.Entities
		var (
			ents []domain.Entity
			err  error
		)
		if typeFilter != "" {
			ents, err = repo.ListByType(ctx, domain.EntityType(typeFilter))
		} else {
			ents, err = repo.List(ctx)
		}
		if err != nil {
			return NewSystemError(err, "list entities")
		}
		if f.Human {
			if len(ents) == 0 {
				fmt.Println("(no entities yet — run `mnemos process --llm` or `mnemos extract-entities --all`)")
				return nil
			}
			for _, e := range ents {
				fmt.Printf("  %s  [%s]  %s\n", e.ID, e.Type, e.Name)
			}
			return nil
		}
		return json.NewEncoder(os.Stdout).Encode(ents)
	})
	exitWithMnemosError(f.Verbose, err)
}

func handleEntitiesShow(args []string, f Flags) {
	if len(args) == 0 {
		exitWithMnemosError(false, NewUserError("entities show requires a name or id"))
		return
	}
	target := strings.Join(args, " ")

	err := runJob("entities-show", map[string]string{"target": target}, f.Verbose, func(ctx context.Context, _ *workflow.Job, w *govwrite.Writer) error {
		conn := w.Conn()
		repo := conn.Entities
		entity, ok, err := resolveEntity(ctx, repo, target)
		if err != nil {
			return NewSystemError(err, "resolve entity")
		}
		if !ok {
			return NewUserError("no entity matching %q (try `mnemos entities list`)", target)
		}
		claims, err := repo.ListClaimsForEntity(ctx, entity.ID)
		if err != nil {
			return NewSystemError(err, "load claims for entity")
		}
		if f.Human {
			fmt.Printf("%s  [%s]  %s\n", entity.ID, entity.Type, entity.Name)
			fmt.Printf("created %s by %s\n\n", entity.CreatedAt.UTC().Format("2006-01-02"), entity.CreatedBy)
			fmt.Printf("claims (%d):\n", len(claims))
			for i, c := range claims {
				fmt.Printf("  %d. [%s] %s\n", i+1, c.Type, c.Text)
			}
			return nil
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"entity": entity,
			"claims": claims,
		})
	})
	exitWithMnemosError(f.Verbose, err)
}

func handleEntitiesMerge(args []string, f Flags) {
	if len(args) < 2 {
		exitWithMnemosError(false, NewUserError("entities merge requires <winner-id> <loser-id>"))
		return
	}
	winnerID, loserID := args[0], args[1]

	err := runJob("entities-merge", map[string]string{"winner": winnerID, "loser": loserID}, f.Verbose, func(ctx context.Context, _ *workflow.Job, w *govwrite.Writer) error {
		if err := w.MergeEntities(ctx, winnerID, loserID); err != nil {
			return NewSystemError(err, "merge entities")
		}
		fmt.Printf("merged: %s absorbed %s (loser deleted)\n", winnerID, loserID)
		return nil
	})
	exitWithMnemosError(f.Verbose, err)
}

// handleExtractEntities runs the LLM extraction pipeline over claims
// that don't yet have entity links. Useful after an upgrade brings a
// better entity-extracting prompt online (v0.9 baseline) or after
// `mnemos reset --keep-events` rebuilds claims without entities.
func handleExtractEntities(args []string, f Flags) {
	all := false
	for _, a := range args {
		switch a {
		case "--all":
			all = true
		default:
			exitWithMnemosError(false, NewUserError("unknown extract-entities flag %q", a))
			return
		}
	}

	err := runJob("extract-entities", map[string]string{"all": fmt.Sprintf("%t", all)}, f.Verbose, func(ctx context.Context, _ *workflow.Job, w *govwrite.Writer) error {
		conn := w.Conn()
		repo := conn.Entities
		var ids []string
		if all {
			claims, err := conn.Claims.ListAll(ctx)
			if err != nil {
				return NewSystemError(err, "list all claims")
			}
			for _, c := range claims {
				ids = append(ids, c.ID)
			}
		} else {
			missing, err := repo.ClaimIDsMissingEntityLinks(ctx)
			if err != nil {
				return NewSystemError(err, "list claims missing entity links")
			}
			ids = missing
		}
		if len(ids) == 0 {
			fmt.Println("All claims already have entity links. Nothing to do.")
			return nil
		}

		// We can't re-run the LLM extraction over claim text alone —
		// it expects events as input. Reconstruct events from the
		// stored claims by treating each claim's text as a synthetic
		// event so the entity-tagging pass runs in isolation.
		ext, extErr := pipeline.NewExtractor(true)
		if extErr != nil {
			return extErr
		}
		claimRepo := conn.Claims

		const batchSize = 16
		linkedTotal := 0
		for start := 0; start < len(ids); start += batchSize {
			end := start + batchSize
			if end > len(ids) {
				end = len(ids)
			}
			batchIDs := ids[start:end]
			claims, err := claimRepo.ListByIDs(ctx, batchIDs)
			if err != nil {
				return NewSystemError(err, "load batch claims")
			}
			synthetic := make([]domain.Event, 0, len(claims))
			for _, c := range claims {
				synthetic = append(synthetic, domain.Event{
					ID:            "syn_" + c.ID,
					RunID:         "extract-entities",
					SchemaVersion: "v1",
					Content:       c.Text,
					SourceInputID: c.ID,
					Timestamp:     c.CreatedAt,
					IngestedAt:    c.CreatedAt,
					Metadata:      map[string]string{"synthetic": "true", "source_claim_id": c.ID},
				})
			}
			_, _, entities, err := ext.ExtractFn(ctx, synthetic)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  extract batch %d-%d: %v (continuing)\n", start, end, err)
				continue
			}
			// The synthetic claim ids in the entities map are NOT
			// the real claim ids — they're freshly minted by the
			// extractor. Remap by matching synthetic event id back
			// to the source claim id we stamped above.
			remapped := remapEntitiesToOriginalClaims(entities, synthetic, claims)
			n, mErr := pipeline.MaterializeEntities(ctx, conn, remapped, "")
			if mErr != nil {
				fmt.Fprintf(os.Stderr, "  materialise batch %d-%d: %v (continuing)\n", start, end, mErr)
				continue
			}
			linkedTotal += n
		}

		fmt.Printf("Linked %d entity reference(s) across %d claim(s).\n", linkedTotal, len(ids))
		return nil
	})
	exitWithMnemosError(f.Verbose, err)
}

// remapEntitiesToOriginalClaims fixes up the entity map keys: the
// extractor minted fresh claim ids when re-extracting from synthetic
// events; we want the entries to point at the *original* claim ids
// the user already has stored. Match by claim text (the synthetic
// event content equals the original claim text).
func remapEntitiesToOriginalClaims(
	entities map[string][]extract.ExtractedEntity,
	synthetic []domain.Event,
	originals []domain.Claim,
) map[string][]extract.ExtractedEntity {
	if len(entities) == 0 {
		return entities
	}
	textToOriginal := make(map[string]string, len(originals))
	for _, c := range originals {
		textToOriginal[strings.TrimSpace(c.Text)] = c.ID
	}
	out := make(map[string][]extract.ExtractedEntity, len(entities))
	// The extractor creates one claim per source event when text
	// matches well, but dedup may collapse some. Walk the entities
	// map; each freshly-minted claim id produced by the LLM run will
	// have associated text we can recover via the synthetic event,
	// since matchEventForClaim picked the closest one. Approximation:
	// if any synthetic event's content equals an original's text,
	// re-key the entities to that original.
	for newClaimID, ents := range entities {
		mapped := false
		for _, ev := range synthetic {
			if origID, ok := textToOriginal[strings.TrimSpace(ev.Content)]; ok {
				out[origID] = append(out[origID], ents...)
				mapped = true
				break
			}
		}
		if !mapped {
			// Couldn't re-key safely — drop rather than poison the
			// link table. Logged for diagnostic visibility.
			fmt.Fprintf(os.Stderr, "  extract-entities: dropped %d entities for unmappable claim %s\n", len(ents), newClaimID)
		}
	}
	return out
}

// resolveEntity resolves a CLI-supplied target — either an entity id
// (starts with "en_") or a name — to a stored entity. Returns
// (entity, true, nil) on a hit, (zero, false, nil) on a miss,
// (zero, false, err) on a transport error.
func resolveEntity(ctx context.Context, repo ports.EntityRepository, target string) (domain.Entity, bool, error) {
	if strings.HasPrefix(target, "en_") {
		ents, err := repo.List(ctx)
		if err != nil {
			return domain.Entity{}, false, err
		}
		for _, e := range ents {
			if e.ID == target {
				return e, true, nil
			}
		}
		return domain.Entity{}, false, nil
	}
	return repo.FindByName(ctx, target)
}
