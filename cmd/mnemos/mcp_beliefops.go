package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	mcp "go.klarlabs.de/mcp"
	mnemos "go.klarlabs.de/mnemos"
	"go.klarlabs.de/mnemos/internal/beliefops"
	"go.klarlabs.de/mnemos/internal/browse"
)

// MCP bindings for capabilities that REST, gRPC and the library already had:
// episode and association browsing, pre-built belief writes, valid-time
// scans, lifecycle transitions, per-belief provenance and history, and entity
// associations. Each tool is a thin adapter over the implementation the
// other transports call — internal/browse for the listings, internal/
// beliefops for provenance, history and entity edges, the Memory facade for
// remember_belief, scan_beliefs and set_belief_lifecycle.

// memResolver returns the request's Memory (getMem in handleMCP).
type memResolver func(context.Context) (mnemos.Memory, error)

// brainMemResolver returns the Memory for the brain a belief id resolved to
// (getMemForBrain in handleMCP).
type brainMemResolver func(context.Context, claimBrain) (mnemos.Memory, error)

// registerBeliefOpsTools adds the tools below to srv.
func registerBeliefOpsTools(srv *mcp.Server, actor string, getMem memResolver, getMemForBrain brainMemResolver) {
	srv.Tool("list_episodes").
		Description("List stored episodes newest first, optionally for one run. Mirrors GET /v1/episodes.").
		OutputSchema(mcpListEpisodesOutput{}).
		Handler(func(ctx context.Context, in mcpListEpisodesInput) (mcpListEpisodesOutput, error) {
			return mcpRunListEpisodes(ctx, in)
		})

	srv.Tool("list_associations").
		Description("List belief-to-belief associations (supports, contradicts), optionally of one type. Mirrors GET /v1/associations; list_dissonances returns contradictions with both beliefs' text.").
		OutputSchema(mcpListAssociationsOutput{}).
		Handler(func(ctx context.Context, in mcpListAssociationsInput) (mcpListAssociationsOutput, error) {
			return mcpRunListAssociations(ctx, in)
		})

	srv.Tool("list_entity_associations").
		Description("List entity-to-entity associations (action_of, outcome_of, validates, ...): all, one kind, or those touching one entity (entity_id with entity_type). Mirrors GET /v1/entity-associations.").
		OutputSchema(mcpListEntityAssociationsOutput{}).
		Handler(func(ctx context.Context, in mcpListEntityAssociationsInput) (mcpListEntityAssociationsOutput, error) {
			return mcpRunListEntityAssociations(ctx, in)
		})

	srv.Tool("remember_belief").
		Description("Write a pre-built belief linked to episodes that already exist (evidence is required). Use remember to store a fact together with its own evidence. Mirrors POST /v1/beliefs.").
		OutputSchema(mcpRememberBeliefOutput{}).
		Handler(func(ctx context.Context, in mcpRememberBeliefInput) (mcpRememberBeliefOutput, error) {
			mem, err := getMem(ctx)
			if err != nil {
				return mcpRememberBeliefOutput{}, err
			}
			return mcpRunRememberBelief(ctx, mem, mcpActorFor(ctx, actor), in)
		})

	srv.Tool("scan_beliefs").
		Description("Return beliefs whose valid-time interval overlaps a window, ordered by valid_from. Mirrors GET /v1/scan.").
		OutputSchema(mcpScanBeliefsOutput{}).
		Handler(func(ctx context.Context, in mcpScanBeliefsInput) (mcpScanBeliefsOutput, error) {
			mem, err := getMem(ctx)
			if err != nil {
				return mcpScanBeliefsOutput{}, err
			}
			return mcpRunScanBeliefs(ctx, mem, in)
		})

	srv.Tool("set_belief_lifecycle").
		Description("Set a belief's curation lifecycle: candidate, promoted or superseded. Mirrors POST /v1/beliefs/<id>/lifecycle.").
		OutputSchema(mcpSetBeliefLifecycleOutput{}).
		Handler(func(ctx context.Context, in mcpSetBeliefLifecycleInput) (mcpSetBeliefLifecycleOutput, error) {
			return mcpRunSetBeliefLifecycle(ctx, getMemForBrain, in)
		})

	srv.Tool("belief_provenance").
		Description("Explain where a belief's trust score came from: the per-signal breakdown and a plain-English rationale. Mirrors GET /v1/beliefs/<id>/provenance.").
		OutputSchema(mcpBeliefProvenanceOutput{}).
		Handler(func(ctx context.Context, in mcpBeliefIDInput) (mcpBeliefProvenanceOutput, error) {
			return mcpRunBeliefProvenance(ctx, in)
		})

	srv.Tool("belief_history").
		Description("Return a belief's version chain newest first (text, confidence, status, who wrote it, when). Mirrors GET /v1/beliefs/<id>/history.").
		OutputSchema(claimHistoryResponse{}).
		Handler(func(ctx context.Context, in mcpBeliefIDInput) (claimHistoryResponse, error) {
			return mcpRunBeliefHistory(ctx, in)
		})
}

// --- list_episodes -----------------------------------------------------------

type mcpListEpisodesInput struct {
	RunID  string `json:"runId,omitempty" jsonschema:"description=Only this run's episodes. Required when the caller's token carries a run allowlist."`
	Limit  int    `json:"limit,omitempty" jsonschema:"description=Max episodes (default 50, cap 200)"`
	Offset int    `json:"offset,omitempty" jsonschema:"description=Episodes to skip"`
}

type mcpListEpisodesOutput struct {
	Episodes []eventDTO `json:"episodes"`
	Total    int        `json:"total"`
	Limit    int        `json:"limit"`
	Offset   int        `json:"offset"`
}

func mcpRunListEpisodes(ctx context.Context, in mcpListEpisodesInput) (mcpListEpisodesOutput, error) {
	limit, offset := normalizePagination(in.Limit, in.Offset)
	runID := strings.TrimSpace(in.RunID)
	if err := enforceRunScope(ctx, runID); err != nil {
		return mcpListEpisodesOutput{}, err
	}
	conn, err := openConn(ctx)
	if err != nil {
		return mcpListEpisodesOutput{}, err
	}
	defer closeConn(conn)
	pg, err := browse.Events(ctx, conn, runID, nil, limit, offset)
	if err != nil {
		return mcpListEpisodesOutput{}, fmt.Errorf("list episodes: %w", err)
	}
	out := mcpListEpisodesOutput{Episodes: make([]eventDTO, 0, len(pg.Items)), Total: pg.Total, Limit: limit, Offset: offset}
	for _, e := range pg.Items {
		out.Episodes = append(out.Episodes, eventDTO{
			ID: e.ID, RunID: e.RunID, SchemaVersion: e.SchemaVersion, Content: e.Content, SourceInputID: e.SourceInputID,
			Timestamp: e.Timestamp.UTC().Format(time.RFC3339), Metadata: e.Metadata, IngestedAt: e.IngestedAt.UTC().Format(time.RFC3339),
		})
	}
	return out, nil
}

// --- list_associations -------------------------------------------------------

type mcpListAssociationsInput struct {
	Type   string `json:"type,omitempty" jsonschema:"description=supports or contradicts; empty lists both"`
	Limit  int    `json:"limit,omitempty" jsonschema:"description=Max associations (default 50, cap 200)"`
	Offset int    `json:"offset,omitempty" jsonschema:"description=Associations to skip"`
}

type mcpListAssociationsOutput struct {
	Associations []relationshipDTO `json:"associations"`
	Total        int               `json:"total"`
	Limit        int               `json:"limit"`
	Offset       int               `json:"offset"`
}

// mcpRunListAssociations pages associations by id. Associations carry no run,
// so a run-scoped token is refused (fail closed), as for list_dissonances
// without a run.
func mcpRunListAssociations(ctx context.Context, in mcpListAssociationsInput) (mcpListAssociationsOutput, error) {
	limit, offset := normalizePagination(in.Limit, in.Offset)
	if in.Type != "" && in.Type != "supports" && in.Type != "contradicts" {
		return mcpListAssociationsOutput{}, userInputError(fmt.Errorf("invalid type %q (want supports or contradicts)", in.Type))
	}
	if err := enforceRunScope(ctx, ""); err != nil {
		return mcpListAssociationsOutput{}, err
	}
	conn, err := openConn(ctx)
	if err != nil {
		return mcpListAssociationsOutput{}, err
	}
	defer closeConn(conn)
	pg, err := browse.Relationships(ctx, conn, in.Type, "", limit, offset)
	if err != nil {
		return mcpListAssociationsOutput{}, fmt.Errorf("list associations: %w", err)
	}
	out := mcpListAssociationsOutput{Associations: make([]relationshipDTO, 0, len(pg.Items)), Total: pg.Total, Limit: limit, Offset: offset}
	for _, r := range pg.Items {
		out.Associations = append(out.Associations, relationshipDTO{
			ID: r.ID, Type: string(r.Type), FromClaimID: r.FromClaimID, ToClaimID: r.ToClaimID,
			CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return out, nil
}

// --- list_entity_associations ------------------------------------------------

type mcpListEntityAssociationsInput struct {
	EntityID   string `json:"entity_id,omitempty" jsonschema:"description=With entity_type: only edges touching this entity"`
	EntityType string `json:"entity_type,omitempty" jsonschema:"description=Type of entity_id (action, outcome, claim, ...)"`
	Kind       string `json:"kind,omitempty" jsonschema:"description=Only edges of this kind"`
	Limit      int    `json:"limit,omitempty" jsonschema:"description=Max edges (default 50, cap 200)"`
	Offset     int    `json:"offset,omitempty" jsonschema:"description=Edges to skip"`
}

type mcpListEntityAssociationsOutput = entityAssociationsResponse

func mcpRunListEntityAssociations(ctx context.Context, in mcpListEntityAssociationsInput) (mcpListEntityAssociationsOutput, error) {
	limit, offset := normalizePagination(in.Limit, in.Offset)
	if err := enforceRunScope(ctx, ""); err != nil {
		return mcpListEntityAssociationsOutput{}, err
	}
	conn, err := openConn(ctx)
	if err != nil {
		return mcpListEntityAssociationsOutput{}, err
	}
	defer closeConn(conn)
	edges, err := beliefops.ListEntityRelationships(ctx, conn, beliefops.EntityFilter{
		EntityID: in.EntityID, EntityType: in.EntityType, Kind: in.Kind,
	})
	if err != nil {
		return mcpListEntityAssociationsOutput{}, mcpBeliefOpError(err)
	}
	out := mcpListEntityAssociationsOutput{Total: len(edges), Limit: limit, Offset: offset}
	page := paginate(edges, limit, offset)
	out.Edges = make([]entityAssociationDTO, 0, len(page))
	for _, e := range page {
		out.Edges = append(out.Edges, entityAssociationDTO{
			ID: e.ID, Kind: string(e.Kind), FromID: e.FromID, FromType: e.FromType, ToID: e.ToID, ToType: e.ToType,
			CreatedAt: e.CreatedAt.UTC().Format(time.RFC3339), CreatedBy: e.CreatedBy,
		})
	}
	return out, nil
}

// --- remember_belief ---------------------------------------------------------

type mcpRememberBeliefInput struct {
	Text       string   `json:"text" jsonschema:"required,description=The belief statement"`
	Type       string   `json:"type,omitempty" jsonschema:"description=fact (default), hypothesis or decision"`
	Confidence float64  `json:"confidence,omitempty" jsonschema:"description=Confidence in [0,1]"`
	EpisodeIDs []string `json:"episode_ids" jsonschema:"required,description=Ids of existing episodes that are this belief's evidence"`
	ValidFrom  string   `json:"valid_from,omitempty" jsonschema:"description=When the belief became true (RFC3339 or YYYY-MM-DD); default now"`
	ValidUntil string   `json:"valid_until,omitempty" jsonschema:"description=When it stopped being true (RFC3339 or YYYY-MM-DD)"`
}

type mcpRememberBeliefOutput struct {
	BeliefID string `json:"belief_id"`
	// Known is true when the brain already held the belief: the episodes were
	// linked to it and no belief was written.
	Known bool `json:"known"`
}

// mcpRunRememberBelief writes through Memory.RememberClaim, the
// implementation the Go library and the lite adapters' remember_claim use.
// Its episodes may belong to any run, so a run-scoped token is refused.
func mcpRunRememberBelief(ctx context.Context, mem mnemos.Memory, actor string, in mcpRememberBeliefInput) (mcpRememberBeliefOutput, error) {
	if err := enforceRunScope(ctx, ""); err != nil {
		return mcpRememberBeliefOutput{}, err
	}
	if strings.TrimSpace(in.Text) == "" {
		return mcpRememberBeliefOutput{}, userInputError(fmt.Errorf("text is required"))
	}
	if !hasNonBlank(in.EpisodeIDs) {
		return mcpRememberBeliefOutput{}, userInputError(fmt.Errorf("episode_ids must name at least one existing episode (beliefs require evidence)"))
	}
	if in.Confidence < 0 || in.Confidence > 1 {
		return mcpRememberBeliefOutput{}, userInputError(fmt.Errorf("confidence must be in [0, 1]"))
	}
	if in.Type != "" && !validClaimType(in.Type) {
		return mcpRememberBeliefOutput{}, userInputError(fmt.Errorf("invalid type %q (want fact, hypothesis, or decision)", in.Type))
	}
	from, err := parseInstant(in.ValidFrom)
	if err != nil {
		return mcpRememberBeliefOutput{}, userInputError(fmt.Errorf("valid_from: %w", err))
	}
	until, err := parseInstant(in.ValidUntil)
	if err != nil {
		return mcpRememberBeliefOutput{}, userInputError(fmt.Errorf("valid_until: %w", err))
	}
	got, err := mem.RememberClaimDetailed(ctx, mnemos.ClaimItem{
		Text: in.Text, Type: in.Type, Confidence: in.Confidence, EventIDs: in.EpisodeIDs,
		ValidFrom: from, ValidUntil: until, CreatedBy: actor,
	})
	if err != nil {
		return mcpRememberBeliefOutput{}, err
	}
	return mcpRememberBeliefOutput{BeliefID: got.ID, Known: got.Known}, nil
}

// --- scan_beliefs ------------------------------------------------------------

type mcpScanBeliefsInput struct {
	ValidFrom  string `json:"valid_from,omitempty" jsonschema:"description=Window start (RFC3339 or YYYY-MM-DD); empty is open"`
	ValidUntil string `json:"valid_until,omitempty" jsonschema:"description=Window end, exclusive; empty is open"`
	Limit      int    `json:"limit,omitempty" jsonschema:"description=Max beliefs (0 = the server cap of 1000)"`
}

type mcpScanBeliefsOutput = scanResponse

// mcpRunScanBeliefs runs Memory.Scan. A scan spans every run, so a
// run-scoped token is refused.
func mcpRunScanBeliefs(ctx context.Context, mem mnemos.Memory, in mcpScanBeliefsInput) (mcpScanBeliefsOutput, error) {
	if err := enforceRunScope(ctx, ""); err != nil {
		return mcpScanBeliefsOutput{}, err
	}
	from, err := parseInstant(in.ValidFrom)
	if err != nil {
		return mcpScanBeliefsOutput{}, userInputError(fmt.Errorf("valid_from: %w", err))
	}
	until, err := parseInstant(in.ValidUntil)
	if err != nil {
		return mcpScanBeliefsOutput{}, userInputError(fmt.Errorf("valid_until: %w", err))
	}
	claims, err := mem.Scan(ctx, mnemos.ScanQuery{ValidFrom: from, ValidUntil: until, Limit: in.Limit})
	if err != nil {
		return mcpScanBeliefsOutput{}, err
	}
	out := mcpScanBeliefsOutput{Beliefs: make([]fullClaimDTO, 0, len(claims))}
	for _, c := range claims {
		out.Beliefs = append(out.Beliefs, publicClaimToDTO(c))
	}
	return out, nil
}

// --- set_belief_lifecycle ----------------------------------------------------

type mcpSetBeliefLifecycleInput struct {
	ClaimID   string `json:"belief_id" jsonschema:"required,description=The belief to transition"`
	Lifecycle string `json:"lifecycle" jsonschema:"required,description=candidate, promoted or superseded"`
}

type mcpSetBeliefLifecycleOutput struct {
	ClaimID   string `json:"belief_id"`
	Lifecycle string `json:"lifecycle"`
}

// mcpRunSetBeliefLifecycle runs Memory.SetClaimLifecycle on the brain that
// holds the belief (#341), as REST and gRPC do on theirs.
func mcpRunSetBeliefLifecycle(ctx context.Context, memFor brainMemResolver, in mcpSetBeliefLifecycleInput) (mcpSetBeliefLifecycleOutput, error) {
	lc := mnemos.ClaimLifecycle(in.Lifecycle)
	switch lc {
	case mnemos.ClaimLifecycleCandidate, mnemos.ClaimLifecyclePromoted, mnemos.ClaimLifecycleSuperseded:
	default:
		return mcpSetBeliefLifecycleOutput{}, userInputError(fmt.Errorf("lifecycle must be candidate, promoted, or superseded"))
	}
	ctx, brain, err := mcpScopeToClaimBrain(ctx, in.ClaimID)
	if err != nil {
		return mcpSetBeliefLifecycleOutput{}, err
	}
	mem, err := memFor(ctx, brain)
	if err != nil {
		return mcpSetBeliefLifecycleOutput{}, err
	}
	if err := mem.SetClaimLifecycle(ctx, in.ClaimID, lc); err != nil {
		return mcpSetBeliefLifecycleOutput{}, err
	}
	return mcpSetBeliefLifecycleOutput(in), nil
}

// --- belief_provenance / belief_history --------------------------------------

type mcpBeliefIDInput struct {
	ClaimID string `json:"belief_id" jsonschema:"required,description=The belief id"`
}

type mcpBeliefProvenanceOutput struct {
	ClaimID        string                   `json:"claim_id"`
	ClaimText      string                   `json:"claim_text"`
	Score          float64                  `json:"score"`
	Signals        []mcpProvenanceSignalDTO `json:"signals"`
	Rationale      string                   `json:"rationale"`
	ProseRationale string                   `json:"prose_rationale,omitempty"`
	SourceDocument string                   `json:"source_document,omitempty"`
	Liveness       string                   `json:"liveness,omitempty"`
}

type mcpProvenanceSignalDTO struct {
	Name         string  `json:"name"`
	Value        float64 `json:"value"`
	Weight       float64 `json:"weight"`
	Contribution float64 `json:"contribution"`
	Detail       string  `json:"detail,omitempty"`
}

// mcpRunBeliefProvenance reads beliefops.Provenance from the brain that holds
// the belief; the output carries the fields REST's provenance document does.
func mcpRunBeliefProvenance(ctx context.Context, in mcpBeliefIDInput) (mcpBeliefProvenanceOutput, error) {
	ctx, _, err := mcpScopeToClaimBrain(ctx, in.ClaimID)
	if err != nil {
		return mcpBeliefProvenanceOutput{}, err
	}
	conn, err := openConn(ctx)
	if err != nil {
		return mcpBeliefProvenanceOutput{}, err
	}
	defer closeConn(conn)
	rep, err := beliefops.Provenance(ctx, conn, in.ClaimID)
	if err != nil {
		return mcpBeliefProvenanceOutput{}, mcpBeliefOpError(err)
	}
	out := mcpBeliefProvenanceOutput{
		ClaimID: rep.ClaimID, ClaimText: rep.ClaimText, Score: rep.Score, Rationale: rep.Rationale,
		ProseRationale: rep.ProseRationale, SourceDocument: rep.SourceDocument, Liveness: string(rep.Liveness),
		Signals: make([]mcpProvenanceSignalDTO, 0, len(rep.Signals)),
	}
	for _, s := range rep.Signals {
		out.Signals = append(out.Signals, mcpProvenanceSignalDTO(s))
	}
	return out, nil
}

// mcpRunBeliefHistory reads beliefops.History from the brain that holds the
// belief and returns REST's history document.
func mcpRunBeliefHistory(ctx context.Context, in mcpBeliefIDInput) (claimHistoryResponse, error) {
	ctx, _, err := mcpScopeToClaimBrain(ctx, in.ClaimID)
	if err != nil {
		return claimHistoryResponse{}, err
	}
	conn, err := openConn(ctx)
	if err != nil {
		return claimHistoryResponse{}, err
	}
	defer closeConn(conn)
	versions, err := beliefops.History(ctx, conn, in.ClaimID)
	if err != nil {
		return claimHistoryResponse{}, mcpBeliefOpError(err)
	}
	return claimHistoryToDTO(in.ClaimID, versions), nil
}
