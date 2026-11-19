package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/pipeline"
	"go.klarlabs.de/mnemos/internal/store"
)

// relStore runs the workloads against a relational backend: through the
// production repositories (the "production" path) and through one SQL
// statement per workload (the "single-query" path).
type relStore struct {
	name    string // sqlite | postgres
	conn    *store.Conn
	db      *sql.DB
	dialect string // sqlite | postgres
	indexed bool   // dissonanceIndexed has created its index
}

func openRel(ctx context.Context, name, dsn string) (*relStore, error) {
	conn, err := store.Open(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", name, err)
	}
	db, ok := conn.Raw.(*sql.DB)
	if !ok {
		_ = conn.Close()
		return nil, fmt.Errorf("%s: raw handle is %T, want *sql.DB", name, conn.Raw)
	}
	return &relStore{name: name, conn: conn, db: db, dialect: name}, nil
}

func (s *relStore) close() { _ = s.conn.Close() }

// load writes the corpus through the storage ports, in the order
// scalebench.Load uses, plus each batch's dependencies.
func (s *relStore) load(ctx context.Context, c *Corpus) error {
	for _, b := range c.Batches {
		if err := s.writeRaw(ctx, b); err != nil {
			return err
		}
	}
	return nil
}

// writeRaw is the storage-port write of one batch with nothing else: the
// like-for-like comparison with a graph store's write.
func (s *relStore) writeRaw(ctx context.Context, b Batch) error {
	for _, ev := range b.Events {
		if err := s.conn.Events.Append(ctx, ev); err != nil {
			return fmt.Errorf("append episode %s: %w", ev.ID, err)
		}
	}
	if len(b.Dependencies) > 0 {
		if err := s.conn.Dependencies.Append(ctx, b.Dependencies); err != nil {
			return fmt.Errorf("append dependencies: %w", err)
		}
	}
	if err := s.conn.Claims.Upsert(ctx, b.Claims); err != nil {
		return fmt.Errorf("upsert beliefs: %w", err)
	}
	if err := s.conn.Claims.UpsertEvidence(ctx, b.Evidence); err != nil {
		return fmt.Errorf("upsert evidence: %w", err)
	}
	if len(b.Relationships) > 0 {
		if err := s.conn.Relationships.Upsert(ctx, b.Relationships); err != nil {
			return fmt.Errorf("upsert associations: %w", err)
		}
	}
	return nil
}

// ---- production paths -------------------------------------------------------

// expandProd is query.Engine.expandClaimsByHops (internal/query/engine.go),
// line for line, with no kind filter (the recall default): one
// ListByClaimIDs and one ListByIDs per hop.
func (s *relStore) expandProd(ctx context.Context, in ExpandInput) ([]string, error) {
	hopDistance := map[string]int{}
	frontier := make([]string, 0, len(in.Seeds))
	for _, id := range in.Seeds {
		hopDistance[id] = 0
		frontier = append(frontier, id)
	}
	var out []string
	for hop := 1; hop <= in.Hops && len(frontier) > 0; hop++ {
		rels, err := s.conn.Relationships.ListByClaimIDs(ctx, frontier)
		if err != nil {
			return nil, fmt.Errorf("list relationships for hop %d: %w", hop, err)
		}
		nextIDs := map[string]struct{}{}
		for _, rel := range rels {
			for _, neighbor := range []string{rel.FromClaimID, rel.ToClaimID} {
				if _, seen := hopDistance[neighbor]; seen {
					continue
				}
				nextIDs[neighbor] = struct{}{}
			}
		}
		if len(nextIDs) == 0 {
			break
		}
		ids := make([]string, 0, len(nextIDs))
		for id := range nextIDs {
			ids = append(ids, id)
		}
		newClaims, err := s.conn.Claims.ListByIDs(ctx, ids)
		if err != nil {
			return nil, fmt.Errorf("load hop-%d claims: %w", hop, err)
		}
		for _, c := range newClaims {
			hopDistance[c.ID] = hop
			out = append(out, expandLine(c.ID, hop))
		}
		frontier = ids
	}
	return out, nil
}

// retractProd is the read side of govwrite.retractEvent (internal/govwrite
// retract.go + dependence.go): the direct beliefs, the breadth-first
// dependency closure (closureOf), the beliefs reached through it
// (reachedThroughClosure) and the live independent evidence of each
// (liveIndependentEvidence, with the per-episode retraction lookups and the
// memoised dependency walk). It stops before any write.
func (s *relStore) retractProd(ctx context.Context, in RetractInput) ([]string, error) {
	conn, root := s.conn, in.Root
	direct, err := conn.Claims.ListByEventIDs(ctx, []string{root})
	if err != nil {
		return nil, err
	}
	level := map[string]int{}
	var order []string
	frontier := []string{root}
	for lvl := 1; len(frontier) > 0; lvl++ {
		deps, err := conn.Dependencies.ListDependents(ctx, frontier)
		if err != nil {
			return nil, err
		}
		sort.Slice(deps, func(i, j int) bool {
			if deps[i].EventID != deps[j].EventID {
				return deps[i].EventID < deps[j].EventID
			}
			return deps[i].DependsOn < deps[j].DependsOn
		})
		var next []string
		for _, d := range deps {
			if d.EventID == root {
				continue
			}
			if _, seen := level[d.EventID]; seen {
				continue
			}
			level[d.EventID] = lvl
			order = append(order, d.EventID)
			next = append(next, d.EventID)
		}
		frontier = next
	}
	dependents := append([]domain.Claim{}, direct...)
	isDirect := map[string]bool{}
	for _, c := range direct {
		isDirect[c.ID] = true
	}
	if len(order) > 0 {
		reached, err := conn.Claims.ListByEventIDs(ctx, order)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, c := range direct {
			seen[c.ID] = true
		}
		for _, c := range reached {
			if !seen[c.ID] {
				seen[c.ID] = true
				dependents = append(dependents, c)
			}
		}
		// reachedThroughClosure also reads the reached beliefs' evidence to
		// pick the episode each was reached through.
		ids := make([]string, 0, len(dependents))
		for _, c := range dependents {
			if !isDirect[c.ID] {
				ids = append(ids, c.ID)
			}
		}
		if _, err := conn.Claims.ListEvidenceByClaimIDs(ctx, ids); err != nil {
			return nil, err
		}
	}
	live, err := s.liveIndependent(ctx, dependents, root)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, id := range order {
		out = append(out, closureLine(id, level[id]))
	}
	for _, c := range dependents {
		out = append(out, outcomeLine(c.ID, retractOutcome(isDirect[c.ID], live[c.ID], c.Status)))
	}
	return out, nil
}

// liveIndependent mirrors govwrite's independence check with root overridden
// as retracted: per evidence episode, the journal lookup (LookupRetraction)
// and the memoised walk of its dependencies (dependsOnRetracted).
func (s *relStore) liveIndependent(ctx context.Context, claims []domain.Claim, root string) (map[string]int, error) {
	out := map[string]int{}
	if len(claims) == 0 {
		return out, nil
	}
	ids := make([]string, len(claims))
	for i, c := range claims {
		ids[i] = c.ID
	}
	links, err := s.conn.Claims.ListEvidenceByClaimIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	x := &indep{conn: s.conn, retracted: map[string]bool{root: true}, tainted: map[string]bool{}, visiting: map[string]bool{}}
	for _, l := range links {
		dead, err := x.isRetracted(ctx, l.EventID)
		if err != nil {
			return nil, err
		}
		if dead {
			continue
		}
		tainted, err := x.dependsOnRetracted(ctx, l.EventID)
		if err != nil {
			return nil, err
		}
		if !tainted {
			out[l.ClaimID]++
		}
	}
	return out, nil
}

type indep struct {
	conn                         *store.Conn
	retracted, tainted, visiting map[string]bool
}

func (x *indep) isRetracted(ctx context.Context, id string) (bool, error) {
	if v, ok := x.retracted[id]; ok {
		return v, nil
	}
	_, v, err := govwrite.LookupRetraction(ctx, x.conn, id)
	if err != nil {
		return false, err
	}
	x.retracted[id] = v
	return v, nil
}

func (x *indep) dependsOnRetracted(ctx context.Context, id string) (bool, error) {
	if v, ok := x.tainted[id]; ok {
		return v, nil
	}
	if x.visiting[id] {
		return false, nil
	}
	x.visiting[id] = true
	defer delete(x.visiting, id)
	deps, err := x.conn.Dependencies.ListByEventIDs(ctx, []string{id})
	if err != nil {
		return false, err
	}
	result := false
	for _, d := range deps {
		dead, err := x.isRetracted(ctx, d.DependsOn)
		if err != nil {
			return false, err
		}
		if !dead {
			if dead, err = x.dependsOnRetracted(ctx, d.DependsOn); err != nil {
				return false, err
			}
		}
		if dead {
			result = true
			break
		}
	}
	x.tainted[id] = result
	return result, nil
}

// dissonanceProd is cmd/mnemos listContradictionPairs (the list_dissonances
// tool) without a run filter: ListAll, keep contradicts, newest first, page,
// then resolve the page's belief texts.
func (s *relStore) dissonanceProd(ctx context.Context, in PageInput) ([]string, error) {
	all, err := s.conn.Relationships.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	var cs []domain.Relationship
	for _, r := range all {
		if r.Type == domain.RelationshipTypeContradicts {
			cs = append(cs, r)
		}
	}
	for i, j := 0, len(cs)-1; i < j; i, j = i+1, j-1 {
		cs[i], cs[j] = cs[j], cs[i]
	}
	out := []string{totalLine(len(cs))}
	var page []domain.Relationship
	if in.Offset < len(cs) {
		page = cs[in.Offset:min(len(cs), in.Offset+in.Limit)]
	}
	ids := map[string]struct{}{}
	for _, r := range page {
		ids[r.FromClaimID], ids[r.ToClaimID] = struct{}{}, struct{}{}
		out = append(out, relLine(r.ID))
	}
	list := make([]string, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	if len(list) > 0 {
		if _, err := s.conn.Claims.ListByIDs(ctx, list); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// neighbourhoodProd is query.collectContradictions: one ListByClaimIDs, keep
// contradicts, dedupe by id. It is already a single round trip.
func (s *relStore) neighbourhoodProd(ctx context.Context, in IDsInput) ([]string, error) {
	rels, err := s.conn.Relationships.ListByClaimIDs(ctx, dedupe(in.IDs))
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, r := range rels {
		if r.Type == domain.RelationshipTypeContradicts && !seen[r.ID] {
			seen[r.ID] = true
			out = append(out, relLine(r.ID))
		}
	}
	return out, nil
}

// redirectsProd calls the production resolver, pipeline.StoredRedirects.
func (s *relStore) redirectsProd(ctx context.Context, in IDsInput) ([]string, error) {
	m, err := pipeline.StoredRedirects(ctx, s.conn, in.IDs)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(m))
	for id, t := range m {
		out = append(out, redirectLine(id, t))
	}
	return out, nil
}

// provenanceProd walks belief -> evidence episodes -> the episodes they
// depend on, one level per round trip, with the production reads
// (ListEvidenceByClaimIDs, Dependencies.ListByEventIDs). No single production
// function returns this path; it is what govwrite's dependency walk reads.
func (s *relStore) provenanceProd(ctx context.Context, in IDsInput) ([]string, error) {
	links, err := s.conn.Claims.ListEvidenceByClaimIDs(ctx, dedupe(in.IDs))
	if err != nil {
		return nil, err
	}
	depth := map[string]int{}
	var frontier []string
	for _, l := range links {
		if _, ok := depth[l.EventID]; !ok {
			depth[l.EventID] = 0
			frontier = append(frontier, l.EventID)
		}
	}
	for d := 1; len(frontier) > 0; d++ {
		deps, err := s.conn.Dependencies.ListByEventIDs(ctx, frontier)
		if err != nil {
			return nil, err
		}
		var next []string
		for _, dep := range deps {
			if _, ok := depth[dep.DependsOn]; !ok {
				depth[dep.DependsOn] = d
				next = append(next, dep.DependsOn)
			}
		}
		frontier = next
	}
	out := make([]string, 0, len(depth))
	for e, d := range depth {
		out = append(out, episodeLine(e, d))
	}
	return out, nil
}

// writeProd is the production ingest write, pipeline.PersistArtifacts
// (known-belief collapse, episodes, beliefs, evidence, associations, scoped
// trust rescore), plus the batch's dependencies.
func (s *relStore) writeProd(ctx context.Context, in WriteInput) ([]string, error) {
	b := in.Batch
	if err := pipeline.PersistArtifacts(ctx, s.conn, b.Events, b.Claims, b.Evidence, b.Relationships); err != nil {
		return nil, err
	}
	if len(b.Dependencies) > 0 {
		if err := s.conn.Dependencies.Append(ctx, b.Dependencies); err != nil {
			return nil, err
		}
	}
	return []string{writeLine(len(b.Claims), len(b.Relationships))}, nil
}

func (s *relStore) writeRawVariant(ctx context.Context, in WriteInput) ([]string, error) {
	if err := s.writeRaw(ctx, in.Batch); err != nil {
		return nil, err
	}
	return []string{writeLine(len(in.Batch.Claims), len(in.Batch.Relationships))}, nil
}

// ---- single-query paths ------------------------------------------------------

// idList binds a list of ids: a JSON array for SQLite's json_each, a text[]
// literal for Postgres (the ids are corpus ids, no quoting needed).
func (s *relStore) idList(ids []string) any {
	if s.dialect == "postgres" {
		return "{" + strings.Join(ids, ",") + "}"
	}
	b, _ := json.Marshal(ids)
	return string(b)
}

func (s *relStore) q(sqlite, postgres string) string {
	if s.dialect == "postgres" {
		return postgres
	}
	return sqlite
}

// recursionBound caps the level of the recursive walks over dependencies, which
// a cyclic dependency graph would otherwise extend forever (UNION dedupes on
// (id, level), not id). The production walk cuts cycles instead.
const recursionBound = 64

func (s *relStore) expandCTE(ctx context.Context, in ExpandInput) ([]string, error) {
	query := s.q(`
WITH RECURSIVE
seeds(id) AS (SELECT value FROM json_each(?1)),
walk(id, hop) AS (
  SELECT id, 0 FROM seeds
  UNION
  SELECT r.to_claim_id, w.hop + 1 FROM walk w JOIN relationships r ON r.from_claim_id = w.id WHERE w.hop < ?2
  UNION
  SELECT r.from_claim_id, w.hop + 1 FROM walk w JOIN relationships r ON r.to_claim_id = w.id WHERE w.hop < ?2
)
SELECT c.id, c.text, c.status, MIN(w.hop) FROM walk w JOIN claims c ON c.id = w.id
WHERE w.id NOT IN (SELECT id FROM seeds)
GROUP BY c.id, c.text, c.status`, `
WITH RECURSIVE
seeds AS (SELECT unnest($1::text[]) AS id),
walk(id, hop) AS (
  SELECT id, 0 FROM seeds
  UNION
  SELECT n.id, w.hop + 1 FROM walk w
  CROSS JOIN LATERAL (
    SELECT r.to_claim_id AS id FROM relationships r WHERE r.from_claim_id = w.id
    UNION ALL
    SELECT r.from_claim_id FROM relationships r WHERE r.to_claim_id = w.id) n
  WHERE w.hop < $2
)
SELECT c.id, c.text, c.status, MIN(w.hop) FROM walk w JOIN claims c ON c.id = w.id
WHERE w.id NOT IN (SELECT id FROM seeds)
GROUP BY c.id, c.text, c.status`)
	rows, err := s.db.QueryContext(ctx, query, s.idList(in.Seeds), in.Hops)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id, text, status string
		var hop int
		if err := rows.Scan(&id, &text, &status, &hop); err != nil {
			return nil, err
		}
		out = append(out, expandLine(id, hop))
	}
	return out, rows.Err()
}

func (s *relStore) retractCTE(ctx context.Context, in RetractInput) ([]string, error) {
	query := s.q(`
WITH RECURSIVE
walk(id, lvl) AS (
  SELECT ?1, 0
  UNION
  SELECT d.event_id, w.lvl + 1 FROM walk w
  JOIN event_dependencies d ON d.depends_on = w.id AND d.removed_at IS NULL
  WHERE w.lvl < ?2 AND d.event_id <> ?1
),
clos AS (SELECT id, MIN(lvl) AS lvl FROM walk GROUP BY id),
dep AS (SELECT DISTINCT ce.claim_id FROM claim_evidence ce JOIN clos ON clos.id = ce.event_id)
SELECT 'c', id, lvl, '', 0 FROM clos WHERE id <> ?1
UNION ALL
SELECT 'b', c.id,
  (SELECT COUNT(*) FROM claim_evidence x WHERE x.claim_id = c.id AND x.event_id NOT IN (SELECT id FROM clos)),
  c.status,
  EXISTS (SELECT 1 FROM claim_evidence x WHERE x.claim_id = c.id AND x.event_id = ?1)
FROM dep JOIN claims c ON c.id = dep.claim_id`, `
WITH RECURSIVE
walk(id, lvl) AS (
  SELECT $1::text, 0
  UNION
  SELECT d.event_id, w.lvl + 1 FROM walk w
  JOIN event_dependencies d ON d.depends_on = w.id AND d.removed_at IS NULL
  WHERE w.lvl < $2 AND d.event_id <> $1
),
clos AS (SELECT id, MIN(lvl) AS lvl FROM walk GROUP BY id),
dep AS (SELECT DISTINCT ce.claim_id FROM claim_evidence ce JOIN clos ON clos.id = ce.event_id)
SELECT 'c', id, lvl, '', false FROM clos WHERE id <> $1
UNION ALL
SELECT 'b', c.id,
  (SELECT COUNT(*) FROM claim_evidence x WHERE x.claim_id = c.id AND x.event_id NOT IN (SELECT id FROM clos)),
  c.status,
  EXISTS (SELECT 1 FROM claim_evidence x WHERE x.claim_id = c.id AND x.event_id = $1)
FROM dep JOIN claims c ON c.id = dep.claim_id`)
	rows, err := s.db.QueryContext(ctx, query, in.Root, recursionBound)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var kind, id, status string
		var n int
		var direct bool
		if err := rows.Scan(&kind, &id, &n, &status, &direct); err != nil {
			return nil, err
		}
		if kind == "c" {
			out = append(out, closureLine(id, n))
			continue
		}
		out = append(out, outcomeLine(id, retractOutcome(direct, n, domain.ClaimStatus(status))))
	}
	return out, rows.Err()
}

// dissonanceIndexed is the cheapest relational fix for the dissonance page:
// an index on relationships(type, created_at) that neither production
// schema has (created by the harness on its own store, on first call, which
// is a warm-up call), an index-only count, and a page read in index order.
// It is what Neo4j's relationship-property index does for the Cypher path.
func (s *relStore) dissonanceIndexed(ctx context.Context, in PageInput) ([]string, error) {
	if !s.indexed {
		if _, err := s.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS gb_relationships_type_created ON relationships(type, created_at)"); err != nil {
			return nil, err
		}
		if s.dialect == "postgres" {
			if _, err := s.db.ExecContext(ctx, "ANALYZE relationships"); err != nil {
				return nil, err
			}
		}
		s.indexed = true
	}
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM relationships WHERE type = 'contradicts'").Scan(&total); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, s.q(`
SELECT r.id, a.text, b.text FROM relationships r
JOIN claims a ON a.id = r.from_claim_id JOIN claims b ON b.id = r.to_claim_id
WHERE r.type = 'contradicts' ORDER BY r.created_at DESC LIMIT ?1 OFFSET ?2`, `
SELECT r.id, a.text, b.text FROM relationships r
JOIN claims a ON a.id = r.from_claim_id JOIN claims b ON b.id = r.to_claim_id
WHERE r.type = 'contradicts' ORDER BY r.created_at DESC LIMIT $1 OFFSET $2`), in.Limit, in.Offset)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []string{totalLine(total)}
	for rows.Next() {
		var id, a, b string
		if err := rows.Scan(&id, &a, &b); err != nil {
			return nil, err
		}
		out = append(out, relLine(id))
	}
	return out, rows.Err()
}

func (s *relStore) dissonanceSQL(ctx context.Context, in PageInput) ([]string, error) {
	query := s.q(`
SELECT r.id, a.text, b.text, COUNT(*) OVER () FROM relationships r
JOIN claims a ON a.id = r.from_claim_id JOIN claims b ON b.id = r.to_claim_id
WHERE r.type = 'contradicts' ORDER BY r.created_at DESC LIMIT ?1 OFFSET ?2`, `
SELECT r.id, a.text, b.text, COUNT(*) OVER () FROM relationships r
JOIN claims a ON a.id = r.from_claim_id JOIN claims b ON b.id = r.to_claim_id
WHERE r.type = 'contradicts' ORDER BY r.created_at DESC LIMIT $1 OFFSET $2`)
	rows, err := s.db.QueryContext(ctx, query, in.Limit, in.Offset)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	total := -1
	for rows.Next() {
		var id, a, b string
		if err := rows.Scan(&id, &a, &b, &total); err != nil {
			return nil, err
		}
		out = append(out, relLine(id))
	}
	if total >= 0 {
		out = append(out, totalLine(total))
	}
	return out, rows.Err()
}

func (s *relStore) redirectsCTE(ctx context.Context, in IDsInput) ([]string, error) {
	live := s.q(`c.status <> 'deprecated' AND (c.valid_to IS NULL OR c.valid_to = '')`, `c.status <> 'deprecated' AND c.valid_to IS NULL`)
	query := s.q(`
WITH RECURSIVE
ids(id) AS (SELECT DISTINCT value FROM json_each(?1)),
chain(start, cur, hop) AS (
  SELECT c.id, c.merged_into, 1 FROM claims c JOIN ids ON ids.id = c.id WHERE c.merged_into <> ''
  UNION ALL
  SELECT ch.start, c.merged_into, ch.hop + 1 FROM chain ch JOIN claims c ON c.id = ch.cur
  WHERE ch.hop < ?2 AND c.merged_into <> '' AND NOT (`+live+`)
)
SELECT ch.start, c.id FROM chain ch JOIN claims c ON c.id = ch.cur WHERE `+live+` AND c.id <> ch.start`, `
WITH RECURSIVE
ids AS (SELECT DISTINCT unnest($1::text[]) AS id),
chain(start, cur, hop) AS (
  SELECT c.id, c.merged_into, 1 FROM claims c JOIN ids ON ids.id = c.id WHERE c.merged_into <> ''
  UNION ALL
  SELECT ch.start, c.merged_into, ch.hop + 1 FROM chain ch JOIN claims c ON c.id = ch.cur
  WHERE ch.hop < $2 AND c.merged_into <> '' AND NOT (`+live+`)
)
SELECT ch.start, c.id FROM chain ch JOIN claims c ON c.id = ch.cur WHERE `+live+` AND c.id <> ch.start`)
	rows, err := s.db.QueryContext(ctx, query, s.idList(in.IDs), maxRedirectHops)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id, target string
		if err := rows.Scan(&id, &target); err != nil {
			return nil, err
		}
		out = append(out, redirectLine(id, target))
	}
	return out, rows.Err()
}

func (s *relStore) provenanceCTE(ctx context.Context, in IDsInput) ([]string, error) {
	query := s.q(`
WITH RECURSIVE
b(id) AS (SELECT value FROM json_each(?1)),
walk(id, d) AS (
  SELECT DISTINCT ce.event_id, 0 FROM claim_evidence ce JOIN b ON b.id = ce.claim_id
  UNION
  SELECT dep.depends_on, w.d + 1 FROM walk w
  JOIN event_dependencies dep ON dep.event_id = w.id AND dep.removed_at IS NULL WHERE w.d < ?2
)
SELECT id, MIN(d) FROM walk GROUP BY id`, `
WITH RECURSIVE
b AS (SELECT unnest($1::text[]) AS id),
walk(id, d) AS (
  SELECT DISTINCT ce.event_id, 0 FROM claim_evidence ce JOIN b ON b.id = ce.claim_id
  UNION
  SELECT dep.depends_on, w.d + 1 FROM walk w
  JOIN event_dependencies dep ON dep.event_id = w.id AND dep.removed_at IS NULL WHERE w.d < $2
)
SELECT id, MIN(d) FROM walk GROUP BY id`)
	rows, err := s.db.QueryContext(ctx, query, s.idList(dedupe(in.IDs)), recursionBound)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		var d int
		if err := rows.Scan(&id, &d); err != nil {
			return nil, err
		}
		out = append(out, episodeLine(id, d))
	}
	return out, rows.Err()
}

// variants lists every way this store answers each workload.
func (s *relStore) variants() map[string][]Variant {
	v := func(path string, fn func(context.Context, any) ([]string, error)) Variant {
		return Variant{Store: s.name, Path: path, run: fn}
	}
	return map[string][]Variant{
		wExpand2:    {v("production", typed(s.expandProd)), v("single-query", typed(s.expandCTE))},
		wExpand5:    {v("production", typed(s.expandProd)), v("single-query", typed(s.expandCTE))},
		wRetract:    {v("production", typed(s.retractProd)), v("single-query", typed(s.retractCTE))},
		wDissonance: {v("production", typed(s.dissonanceProd)), v("single-query", typed(s.dissonanceSQL)), v("indexed", typed(s.dissonanceIndexed))},
		wNeighbour:  {v("production", typed(s.neighbourhoodProd))},
		wRedirects:  {v("production", typed(s.redirectsProd)), v("single-query", typed(s.redirectsCTE))},
		wProvenance: {v("production", typed(s.provenanceProd)), v("single-query", typed(s.provenanceCTE))},
		wWrite:      {v("production", typed(s.writeProd)), v("ports-only", typed(s.writeRawVariant))},
	}
}

// diskBytes reports the store's footprint: the SQLite file (plus WAL), or the
// Postgres namespace's tables, indexes and TOAST.
func (s *relStore) diskBytes(ctx context.Context, sqlitePath, pgNamespace string) (int64, error) {
	if s.dialect == "sqlite" {
		if _, err := s.db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
			return 0, err
		}
		return fileSizes(sqlitePath, sqlitePath+"-wal")
	}
	var n sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
SELECT SUM(pg_total_relation_size(c.oid)) FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relkind IN ('r', 'm')`, pgNamespace).Scan(&n)
	return n.Int64, err
}

func dedupe(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
