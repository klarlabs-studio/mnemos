package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/extract"
	"go.klarlabs.de/mnemos/internal/store"
)

// A belief the brain already holds is not a new claim. Restating it — in a
// later session, a replayed transcript, a document ingested twice — links the
// new evidence to the claim that holds it instead of adding a row.
//
// Measured on a real 111k-claim brain: 62.8% of live claims were exact
// duplicates of another claim's text, 427 texts had 100 or more copies, and a
// two-day replay added 56k claims by storing the same texts again. Each copy
// recalled separately, related separately, and sat in the contradiction
// detector's candidate pool.
//
// "Holds" means a LIVE claim (valid time open, not deprecated) with the same
// normalised text (domain.ClaimTextKey), scope and audience, in the same
// tenant — the connection is already tenant-scoped. A deprecated claim never
// absorbs a restatement: the belief comes back as a new claim, as it would have
// before.
//
// Folding must not turn repetition into corroboration (#443, #390). Trust
// counts evidence links, so a claim gains at most ONE link per origin — the
// capture session that produced the evidence, or for sessionless input the
// content itself. Replaying a session, or ingesting the same document again,
// therefore leaves the claim's evidence and trust exactly where they were. A
// restatement from a new session is a new origin and is linked, which counts no
// more than the duplicate row it replaces would have.

// KnownClaims is a write batch folded onto the beliefs the brain already holds.
type KnownClaims struct {
	// Claims are the claims still to write. None restates a live claim of the
	// same scope and audience, or another claim in the batch.
	Claims []domain.Claim
	// Links point each piece of evidence at the claim that holds its text, less
	// any link that would give a claim a second piece of evidence from the same
	// origin.
	Links []domain.ClaimEvidence
	// Remap maps each folded claim id to the id of the claim holding its belief.
	// Callers that embed or tag entities after persisting use it to address the
	// claim that exists.
	Remap map[string]string
}

// CollapseKnownClaims folds a batch onto the live claims that already state
// its beliefs. PersistArtifacts applies it to every write; callers that do
// further per-claim work after persisting (embedding, entity tagging, relate)
// call it first, so that work lands on the claim that is stored.
func CollapseKnownClaims(ctx context.Context, conn *store.Conn, events []domain.Event, claims []domain.Claim, links []domain.ClaimEvidence) (KnownClaims, error) {
	held, err := heldBeliefs(ctx, conn, claims)
	if err != nil {
		return KnownClaims{}, err
	}
	kept, remap, err := foldOntoHeld(ctx, conn, claims, held)
	if err != nil {
		return KnownClaims{}, err
	}
	links, err = dropRepeatedOrigins(ctx, conn, events, rewriteLinks(links, remap))
	if err != nil {
		return KnownClaims{}, err
	}
	return KnownClaims{Claims: kept, Links: links, Remap: remap}, nil
}

// beliefIdentity is what makes two claims the same belief.
type beliefIdentity struct {
	key        string
	scope      domain.Scope
	visibility domain.Visibility
}

func identityOf(c domain.Claim) beliefIdentity {
	vis := c.Visibility
	if vis == "" {
		vis = domain.DefaultVisibility
	}
	return beliefIdentity{key: domain.ClaimTextKey(c.Text), scope: c.Scope, visibility: vis}
}

// heldBeliefs returns, per identity, the id of the oldest live claim already
// holding a belief the batch states.
func heldBeliefs(ctx context.Context, conn *store.Conn, claims []domain.Claim) (map[beliefIdentity]string, error) {
	keys := make([]string, 0, len(claims))
	seen := make(map[string]struct{}, len(claims))
	for _, c := range claims {
		k := domain.ClaimTextKey(c.Text)
		if _, dup := seen[k]; !dup && domain.NormalizeClaimText(c.Text) != "" {
			seen[k] = struct{}{}
			keys = append(keys, k)
		}
	}
	held := map[beliefIdentity]string{}
	if len(keys) == 0 {
		return held, nil
	}
	live, err := conn.Claims.ListLiveByTextKeys(ctx, keys)
	if err != nil {
		return nil, fmt.Errorf("look up held beliefs: %w", err)
	}
	for _, c := range live { // oldest first, so the first claim to say it holds it
		if _, ok := held[identityOf(c)]; !ok {
			held[identityOf(c)] = c.ID
		}
	}
	return held, nil
}

// foldOntoHeld drops each claim whose belief is already held — by a stored
// claim or an earlier claim of the batch — and records where it went. A claim
// whose id is already stored is an update of that claim, never a duplicate.
func foldOntoHeld(ctx context.Context, conn *store.Conn, claims []domain.Claim, held map[beliefIdentity]string) ([]domain.Claim, map[string]string, error) {
	owner := make(map[beliefIdentity]string, len(held)+len(claims))
	for id, claimID := range held {
		owner[id] = claimID
	}
	remap := map[string]string{}
	kept := make([]domain.Claim, 0, len(claims))
	for _, c := range claims {
		id := identityOf(c)
		if to, ok := owner[id]; ok && to != c.ID && domain.NormalizeClaimText(c.Text) != "" {
			remap[c.ID] = to
			continue
		}
		owner[id] = c.ID
		kept = append(kept, c)
	}
	return restoreStoredUpdates(ctx, conn, claims, kept, remap)
}

// restoreStoredUpdates puts back any folded claim whose id is already stored:
// rewriting a stored claim so it reads like another is an edit, and dropping it
// would lose the edit.
func restoreStoredUpdates(ctx context.Context, conn *store.Conn, claims, kept []domain.Claim, remap map[string]string) ([]domain.Claim, map[string]string, error) {
	if len(remap) == 0 {
		return kept, remap, nil
	}
	folded := make([]string, 0, len(remap))
	for id := range remap {
		folded = append(folded, id)
	}
	stored, err := conn.Claims.ListByIDs(ctx, folded)
	if err != nil {
		return nil, nil, fmt.Errorf("look up folded claim ids: %w", err)
	}
	if len(stored) == 0 {
		return kept, remap, nil
	}
	for _, c := range stored {
		delete(remap, c.ID)
	}
	kept = make([]domain.Claim, 0, len(claims))
	for _, c := range claims {
		if _, folded := remap[c.ID]; !folded {
			kept = append(kept, c)
		}
	}
	return kept, remap, nil
}

// rewriteLinks points links at the claims that hold their beliefs, dropping
// the (claim, event) pairs the rewrite makes identical.
func rewriteLinks(links []domain.ClaimEvidence, remap map[string]string) []domain.ClaimEvidence {
	out := make([]domain.ClaimEvidence, 0, len(links))
	seen := make(map[domain.ClaimEvidence]struct{}, len(links))
	for _, l := range links {
		if to, ok := remap[l.ClaimID]; ok {
			l.ClaimID = to
		}
		if _, dup := seen[l]; dup {
			continue
		}
		seen[l] = struct{}{}
		out = append(out, l)
	}
	return out
}

// dropRepeatedOrigins keeps at most one evidence link per (claim, origin),
// counting the links a stored claim already has. This is what stops a replay or
// a re-ingest from raising trust: the trust model counts links, and a repeat
// from the same origin is not independent evidence (#443). It holds for every
// link a write makes, including links a caller pointed at a stored claim
// itself.
func dropRepeatedOrigins(ctx context.Context, conn *store.Conn, events []domain.Event, links []domain.ClaimEvidence) ([]domain.ClaimEvidence, error) {
	origins := make(map[string]string, len(events)) // event id -> origin
	for _, ev := range events {
		origins[ev.ID] = evidenceOrigin(ev)
	}
	seen, err := storedOrigins(ctx, conn, links, origins)
	if err != nil {
		return nil, err
	}
	out := make([]domain.ClaimEvidence, 0, len(links))
	for _, l := range links {
		origin, ok := origins[l.EventID]
		if !ok {
			origin = "event:" + l.EventID // not in this batch: its own origin
		}
		if seen[l.ClaimID] == nil {
			seen[l.ClaimID] = map[string]struct{}{}
		}
		if _, repeat := seen[l.ClaimID][origin]; repeat {
			continue
		}
		seen[l.ClaimID][origin] = struct{}{}
		out = append(out, l)
	}
	return out, nil
}

// storedOrigins returns, per claim the links point at, the origins of the
// evidence it already has. A new claim has none; a held one has the sessions
// and documents that already stated it. origins is extended with the stored
// events it reads.
func storedOrigins(ctx context.Context, conn *store.Conn, links []domain.ClaimEvidence, origins map[string]string) (map[string]map[string]struct{}, error) {
	seen := map[string]map[string]struct{}{}
	targets := make([]string, 0, len(links))
	for _, l := range links {
		if _, ok := seen[l.ClaimID]; !ok {
			seen[l.ClaimID] = nil
			targets = append(targets, l.ClaimID)
		}
	}
	if len(targets) == 0 {
		return seen, nil
	}
	stored, err := conn.Claims.ListEvidenceByClaimIDs(ctx, targets)
	if err != nil {
		return nil, fmt.Errorf("list stored evidence: %w", err)
	}
	if err := loadOrigins(ctx, conn, stored, origins); err != nil {
		return nil, err
	}
	for _, l := range stored {
		if seen[l.ClaimID] == nil {
			seen[l.ClaimID] = map[string]struct{}{}
		}
		if origin, ok := origins[l.EventID]; ok {
			seen[l.ClaimID][origin] = struct{}{}
		}
	}
	return seen, nil
}

// loadOrigins reads the stored events behind links that origins does not
// cover yet and records their origins.
func loadOrigins(ctx context.Context, conn *store.Conn, links []domain.ClaimEvidence, origins map[string]string) error {
	var missing []string
	for _, l := range links {
		if _, ok := origins[l.EventID]; !ok {
			missing = append(missing, l.EventID)
			origins[l.EventID] = "event:" + l.EventID // until the read says otherwise
		}
	}
	if len(missing) == 0 {
		return nil
	}
	stored, err := conn.Events.ListByIDs(ctx, missing)
	if err != nil {
		return fmt.Errorf("load stored evidence events: %w", err)
	}
	for _, ev := range stored {
		origins[ev.ID] = evidenceOrigin(ev)
	}
	return nil
}

// evidenceOrigin names where a piece of evidence came from, for deciding
// whether two pieces are independent. A captured session is one origin however
// it is chunked or replayed; sessionless input is identified by its content,
// so the same document ingested again is the same origin.
func evidenceOrigin(ev domain.Event) string {
	if s := ev.Metadata[SessionMetadataKey]; s != "" {
		return "session:" + s
	}
	sum := sha256.Sum256([]byte(ev.Content))
	return "content:" + hex.EncodeToString(sum[:16])
}

// remapRelationships points edges at the claims that hold their endpoints,
// dropping the edges the rewrite turns into self-loops or exact repeats.
func remapRelationships(rels []domain.Relationship, remap map[string]string) []domain.Relationship {
	if len(remap) == 0 {
		return rels
	}
	type edge struct {
		kind     domain.RelationshipType
		from, to string
	}
	out := make([]domain.Relationship, 0, len(rels))
	seen := make(map[edge]struct{}, len(rels))
	for _, r := range rels {
		if to, ok := remap[r.FromClaimID]; ok {
			r.FromClaimID = to
		}
		if to, ok := remap[r.ToClaimID]; ok {
			r.ToClaimID = to
		}
		e := edge{r.Type, r.FromClaimID, r.ToClaimID}
		if _, dup := seen[e]; dup || r.FromClaimID == r.ToClaimID {
			continue
		}
		seen[e] = struct{}{}
		out = append(out, r)
	}
	return out
}

// RemapEntities moves the entity tags of folded claims onto the claims that
// hold their beliefs, so materialising them tags a claim that exists.
func RemapEntities(entities map[string][]extract.ExtractedEntity, remap map[string]string) map[string][]extract.ExtractedEntity {
	if len(remap) == 0 || len(entities) == 0 {
		return entities
	}
	out := make(map[string][]extract.ExtractedEntity, len(entities))
	for claimID, tags := range entities {
		if to, ok := remap[claimID]; ok {
			claimID = to
		}
		out[claimID] = append(out[claimID], tags...)
	}
	return out
}

// SessionEventID is the id of a captured session chunk: derived from the
// session and the chunk's content, so capturing the same chunk again names the
// event already stored and appends nothing. Random ids made every replay a new
// episode (#390).
func SessionEventID(sessionID, content string) string {
	sum := sha256.Sum256([]byte(sessionID + "\x00" + content))
	return "ev_" + hex.EncodeToString(sum[:16])
}
