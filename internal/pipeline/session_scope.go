package pipeline

import (
	"context"
	"fmt"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
)

// SessionMetadataKey tags an event with the agent session that produced it.
// Capture sets it from the Claude Code session id; every other ingestion path
// leaves it empty and is unaffected by anything in this file.
const SessionMetadataKey = "session_id"

// DropIntraSessionContradictions removes `contradicts` edges whose two claims
// were both extracted from the same session.
//
// A conversation is a narrative that moves. Its beginning routinely conflicts
// with its end — "starting item 2" then "item 2 is complete", "key suspicion
// forming: the timeout is 90s" then "root cause confirmed: the field is
// unset" — and pairwise detection reads that progression as disagreement.
// Sampled against a production brain these pairs were overwhelmingly claims
// that AGREE or refine one another, not competing beliefs; 84% of live
// contradiction edges sat less than a day apart, and exactly one pair was more
// than a week apart. So this is not an aging problem that supersession would
// fix — the two claims never disagreed in the first place.
//
// Only contradictions are dropped. `supports` edges within a session are
// exactly right: a conversation corroborating itself is real evidence.
//
// Cross-session contradictions are untouched, and that is the point: two
// different sessions asserting incompatible things is a genuine conflict worth
// surfacing. Claims whose session is unknown (anything ingested before capture
// began tagging, or by another path) are never dropped — the filter has to
// know both sides came from one conversation before it suppresses anything.
func DropIntraSessionContradictions(
	ctx context.Context,
	events ports.EventRepository,
	claims ports.ClaimRepository,
	rels []domain.Relationship,
	sessionID string,
	newClaimIDs map[string]struct{},
) ([]domain.Relationship, int, error) {
	if sessionID == "" || len(rels) == 0 {
		return rels, 0, nil
	}

	// Only claims that actually appear on a contradiction edge need a session
	// lookup, which keeps this proportional to the conflicts found rather than
	// to the size of the brain.
	needed := make(map[string]struct{})
	for _, r := range rels {
		if r.Type != domain.RelationshipTypeContradicts {
			continue
		}
		for _, id := range []string{r.FromClaimID, r.ToClaimID} {
			if _, isNew := newClaimIDs[id]; !isNew {
				needed[id] = struct{}{}
			}
		}
	}

	sessionOf, err := sessionsForClaims(ctx, events, claims, needed)
	if err != nil {
		return nil, 0, err
	}
	// Everything written by this run belongs to the session being captured.
	for id := range newClaimIDs {
		sessionOf[id] = sessionID
	}

	kept := make([]domain.Relationship, 0, len(rels))
	dropped := 0
	for _, r := range rels {
		if r.Type == domain.RelationshipTypeContradicts {
			a, aok := sessionOf[r.FromClaimID]
			b, bok := sessionOf[r.ToClaimID]
			if aok && bok && a != "" && a == b {
				dropped++
				continue
			}
		}
		kept = append(kept, r)
	}
	return kept, dropped, nil
}

// sessionsForClaims resolves each claim to the session of its source events,
// via the evidence links. A claim with several events takes the first
// non-empty session: evidence for one claim comes from one ingestion, so they
// agree in practice, and disagreement should not silently suppress an edge.
func sessionsForClaims(
	ctx context.Context,
	events ports.EventRepository,
	claims ports.ClaimRepository,
	claimIDs map[string]struct{},
) (map[string]string, error) {
	out := make(map[string]string, len(claimIDs))
	if len(claimIDs) == 0 {
		return out, nil
	}
	ids := make([]string, 0, len(claimIDs))
	for id := range claimIDs {
		ids = append(ids, id)
	}
	links, err := claims.ListEvidenceByClaimIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("mnemos: session scope: list evidence: %w", err)
	}
	if len(links) == 0 {
		return out, nil
	}

	eventIDs := make([]string, 0, len(links))
	seen := make(map[string]struct{}, len(links))
	for _, l := range links {
		if _, dup := seen[l.EventID]; dup {
			continue
		}
		seen[l.EventID] = struct{}{}
		eventIDs = append(eventIDs, l.EventID)
	}
	evs, err := events.ListByIDs(ctx, eventIDs)
	if err != nil {
		return nil, fmt.Errorf("mnemos: session scope: list events: %w", err)
	}
	sessionOfEvent := make(map[string]string, len(evs))
	for _, e := range evs {
		if s := e.Metadata[SessionMetadataKey]; s != "" {
			sessionOfEvent[e.ID] = s
		}
	}
	for _, l := range links {
		if out[l.ClaimID] != "" {
			continue
		}
		if s := sessionOfEvent[l.EventID]; s != "" {
			out[l.ClaimID] = s
		}
	}
	return out, nil
}
