package relate

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/llm"
)

// DetectCausalLLM augments DetectCausal with an LLM disambiguation
// pass. The deterministic heuristic stays the canonical baseline;
// this path runs when the heuristic returns no edge but the pair
// still satisfies a "shared entity, plausible ordering" precondition
// — borderline cases the heuristic deliberately drops to keep
// precision high. The LLM either confirms a cause/effect relation
// (emit `causes`) or rejects it (emit nothing).
//
// Falls back to heuristic-only output when the LLM client is nil so
// callers can swap LLM on/off without restructuring the pipeline.
func (e Engine) DetectCausalLLM(ctx context.Context, claims []domain.Claim, client llm.Client) ([]domain.Relationship, error) {
	if client == nil {
		return e.DetectCausal(claims)
	}
	heuristic, err := e.DetectCausal(claims)
	if err != nil {
		return nil, err
	}
	heuristicEdges := edgeSet(heuristic)

	type cand struct {
		i, j int
	}
	var borderline []cand

	type analyzed struct {
		raw      map[string]struct{}
		stem     map[string]struct{}
		isAction bool
		isState  bool
		ts       time.Time
	}
	cache := make([]analyzed, len(claims))
	for i, c := range claims {
		stems, _ := contentTokensAndPolarity(c.Text)
		raw := rawContentTokens(c.Text)
		cache[i] = analyzed{
			raw:      raw,
			stem:     stems,
			isAction: hasAnyPrefix(raw, actionPrefixes),
			isState:  hasAnyPrefix(raw, stateChangePrefixes),
			ts:       claimEffectiveTime(c),
		}
	}

	for i := 0; i < len(claims); i++ {
		for j := 0; j < len(claims); j++ {
			if i == j {
				continue
			}
			if !cache[i].isAction || !cache[j].isState {
				continue
			}
			if _, already := heuristicEdges[edgeKey{claims[i].ID, claims[j].ID}]; already {
				continue
			}
			// Borderline: heuristic dropped this pair because it
			// lacked the shared-entity signal. Ordering must still
			// be plausible — asking the LLM about pairs that are
			// out of temporal order would just waste tokens.
			if !plausibleCausalOrdering(cache[i].ts, cache[j].ts) {
				continue
			}
			borderline = append(borderline, cand{i, j})
		}
	}

	now := e.now().UTC()
	out := append([]domain.Relationship(nil), heuristic...)
	for _, c := range borderline {
		yes, err := askLLMCausal(ctx, client, claims[c.i], claims[c.j])
		if err != nil {
			// LLM failure is non-fatal — heuristic answer survives.
			continue
		}
		if !yes {
			continue
		}
		id, err := e.nextID()
		if err != nil {
			return nil, err
		}
		out = append(out, domain.Relationship{
			ID:          id,
			Type:        domain.RelationshipTypeCauses,
			FromClaimID: claims[c.i].ID,
			ToClaimID:   claims[c.j].ID,
			CreatedAt:   now,
		})
	}
	return out, nil
}

type edgeKey struct {
	from, to string
}

func edgeSet(rels []domain.Relationship) map[edgeKey]struct{} {
	out := make(map[edgeKey]struct{}, len(rels))
	for _, r := range rels {
		out[edgeKey{r.FromClaimID, r.ToClaimID}] = struct{}{}
	}
	return out
}

const causalSystemPrompt = `You are a precise reasoner inspecting two operational claims. Answer ONLY in JSON of the form {"causes": true|false}. The first claim is an action; the second is an observed state change. Return causes=true ONLY when the action plausibly produced the state change in the same operational system. Return causes=false for coincidence, unrelated systems, or unclear pairs.`

func askLLMCausal(ctx context.Context, client llm.Client, action, effect domain.Claim) (bool, error) {
	user := fmt.Sprintf(`Action: %s
State change: %s
Action time: %s
State change time: %s`,
		strings.TrimSpace(action.Text),
		strings.TrimSpace(effect.Text),
		formatTimeOrUnknown(claimEffectiveTime(action)),
		formatTimeOrUnknown(claimEffectiveTime(effect)),
	)
	resp, err := client.Complete(ctx, []llm.Message{
		{Role: llm.RoleSystem, Content: causalSystemPrompt},
		{Role: llm.RoleUser, Content: user},
	})
	if err != nil {
		return false, err
	}
	return parseCausesYesNo(resp.Content), nil
}

func parseCausesYesNo(content string) bool {
	c := strings.TrimSpace(content)
	if c == "" {
		return false
	}
	// Try strict JSON first; fall back to substring match for
	// providers that wrap the JSON in prose.
	var payload struct {
		Causes bool `json:"causes"`
	}
	if err := json.Unmarshal([]byte(c), &payload); err == nil {
		return payload.Causes
	}
	if start := strings.Index(c, "{"); start >= 0 {
		if end := strings.LastIndex(c, "}"); end > start {
			if err := json.Unmarshal([]byte(c[start:end+1]), &payload); err == nil {
				return payload.Causes
			}
		}
	}
	lc := strings.ToLower(c)
	return strings.Contains(lc, `"causes": true`) || strings.Contains(lc, `"causes":true`)
}

func formatTimeOrUnknown(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.UTC().Format(time.RFC3339)
}
