package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/query"
)

// Recall on every surface is one pipeline:
//
//	surface input → recallParams → query.RecallRequest → query.Engine.Recall → recallOutput
//
// The CLI `query` command, the query_knowledge and recall_at_time MCP tools
// and REST /v1/search each translate their own spelling into recallParams and
// nothing else. Before this, each built AnswerOptions by hand: the CLI accepted
// YYYY-MM-DD where MCP demanded RFC3339, the CLI rejected hops above five while
// MCP clamped them, query_knowledge took no time bounds or trust floor at all,
// and the CLI ranked by keywords alone unless --embed was passed while MCP used
// the embeddings it found configured. The same question could get a different
// answer depending on where it was asked. TestRecallParity_EverySurfaceAgrees
// pins the shared path.

// recallParams is the surface-neutral recall input. Surfaces parse times with
// parseInstant, the one parser, before filling it.
type recallParams struct {
	Question       string
	RunID          string
	AsOf           time.Time // valid time
	RecordedAsOf   time.Time // transaction time (ADR 0036)
	Hops           int
	HopKinds       []domain.RelationshipType
	MinTrust       float64
	IncludeHistory bool
	Scope          domain.Scope
	Visibility     string
	Lifecycle      string
	// AllowedClaimIDs restricts the answer to these claims (CLI --entity).
	AllowedClaimIDs map[string]struct{}
	// Consumer is the one per-surface default: an agent caller gets
	// contradictions resolved for it, a person gets them explained.
	Consumer domain.Consumer
	// SkipAnswerText is set by surfaces that return beliefs only.
	SkipAnswerText bool
	// Behaviours a caller forces on; the shared cognitive defaults supply the
	// rest inside query.Engine.Recall.
	Prime, Salient, Hebbian, Reconsolidate, Inhibit bool
}

// request validates p and builds the engine request. Every error wraps
// query.ErrInvalidRecall, so a surface maps it to its user-error form once.
func (p recallParams) request() (query.RecallRequest, error) {
	visibility := domain.Visibility(strings.TrimSpace(p.Visibility))
	switch visibility {
	case "", domain.VisibilityPersonal, domain.VisibilityTeam, domain.VisibilityOrg:
	default:
		return query.RecallRequest{}, fmt.Errorf("%w: visibility must be personal, team or org, got %q", query.ErrInvalidRecall, p.Visibility)
	}
	return query.RecallRequest{
		Question: p.Question,
		RunID:    p.RunID,
		Options: query.AnswerOptions{
			Hops:            p.Hops,
			HopKinds:        p.HopKinds,
			MinTrust:        p.MinTrust,
			AsOf:            p.AsOf.UTC(),
			RecordedAsOf:    p.RecordedAsOf.UTC(),
			IncludeHistory:  p.IncludeHistory,
			Scope:           p.Scope,
			Visibility:      visibility,
			Lifecycle:       domain.ClaimLifecycle(strings.TrimSpace(p.Lifecycle)),
			AllowedClaimIDs: p.AllowedClaimIDs,
			Consumer:        p.Consumer,
			SkipAnswerText:  p.SkipAnswerText,
			Prime:           p.Prime,
			Salient:         p.Salient,
			Hebbian:         p.Hebbian,
			Reconsolidate:   p.Reconsolidate,
			Inhibit:         p.Inhibit,
		},
	}, nil
}

// parseInstant is the one timestamp parser for recall on every surface:
// empty is "not set"; otherwise YYYY-MM-DD (midnight UTC), RFC 3339 or RFC 3339
// with fractional seconds. The result is UTC.
func parseInstant(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized timestamp %q (want YYYY-MM-DD or RFC3339)", s)
}

// recallWith runs p on engine. It is the only place a surface calls the
// engine's recall.
func recallWith(ctx context.Context, engine query.Engine, p recallParams) (domain.Answer, error) {
	req, err := p.request()
	if err != nil {
		return domain.Answer{}, err
	}
	return engine.Recall(ctx, req)
}

// isInvalidRecall reports whether err is the caller's mistake.
func isInvalidRecall(err error) bool {
	return errors.Is(err, query.ErrInvalidRecall)
}

// recallOutput is the one serialised recall result, shared by MCP and the
// CLI's JSON output. Field names are the brain-native wire vocabulary.
type recallOutput struct {
	Answer         string                `json:"answer"`
	Claims         []domain.Claim        `json:"beliefs"`
	Contradictions []domain.Relationship `json:"dissonances"`
	Timeline       []string              `json:"timeline"`
	Confidence     float64               `json:"confidence"`
	// ClaimProvenance maps claim ID to "local" or a registry URL so the
	// agent can show which claims came from a federated registry.
	ClaimProvenance map[string]string `json:"belief_provenance,omitempty"`
	// ClaimHopDistance maps claim ID to the BFS hop count from the
	// directly-retrieved set (0 = direct, N>0 = expanded via N hops of
	// supports/contradicts). Empty when hops=0.
	ClaimHopDistance map[string]int `json:"belief_hop_distance,omitempty"`
}

func newRecallOutput(a domain.Answer) recallOutput {
	return recallOutput{
		Answer:           a.AnswerText,
		Claims:           a.Claims,
		Contradictions:   a.Contradictions,
		Timeline:         a.TimelineEventIDs,
		Confidence:       a.Confidence,
		ClaimProvenance:  a.ClaimProvenance,
		ClaimHopDistance: a.ClaimHopDistance,
	}
}
