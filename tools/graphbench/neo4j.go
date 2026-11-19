package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// neo4jStore talks to Neo4j over its HTTP transactional Cypher endpoint, so the
// main module gains no driver dependency. HTTP+JSON costs more per round trip
// than Bolt; the harness measures that floor ("RETURN 1") and reports it next
// to the results, so readers can see how much of a latency is transport.
//
// Model: (:Belief {id, text, type, status, confidence, created_at, valid_to,
// merged_into}), (:Episode {id, run_id, ts, retracted}); association edges
// (:Belief)-[:SUPPORTS|CONTRADICTS|... {id, created_at}]->(:Belief),
// (:Belief)-[:EVIDENCED_BY]->(:Episode), (:Episode)-[:DEPENDS_ON]->(:Episode),
// (:Belief)-[:MERGED_INTO]->(:Belief). Unique constraints index both ids.
type neo4jStore struct {
	url    string // e.g. http://127.0.0.1:7474
	db     string
	client *http.Client
}

func newNeo4j(url string) *neo4jStore {
	return &neo4jStore{
		url: strings.TrimRight(url, "/"), db: "neo4j",
		client: &http.Client{Timeout: 10 * time.Minute, Transport: &http.Transport{MaxIdleConnsPerHost: 4}},
	}
}

type cypher struct {
	Statement  string         `json:"statement"`
	Parameters map[string]any `json:"parameters,omitempty"`
}

type cypherResponse struct {
	Results []struct {
		Columns []string `json:"columns"`
		Data    []struct {
			Row []any `json:"row"`
		} `json:"data"`
	} `json:"results"`
	Errors []struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
}

// run executes the statements in one auto-committed transaction (one HTTP
// round trip) and returns each statement's rows.
func (n *neo4jStore) run(ctx context.Context, stmts ...cypher) ([][][]any, error) {
	body, err := json.Marshal(map[string]any{"statements": withRowFormat(stmts)})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.url+"/db/"+n.db+"/tx/commit", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("neo4j: HTTP %d: %.300s", resp.StatusCode, raw)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var cr cypherResponse
	if err := dec.Decode(&cr); err != nil {
		return nil, fmt.Errorf("neo4j: decode: %w", err)
	}
	if len(cr.Errors) > 0 {
		return nil, fmt.Errorf("neo4j: %s: %s", cr.Errors[0].Code, cr.Errors[0].Message)
	}
	out := make([][][]any, len(cr.Results))
	for i, r := range cr.Results {
		for _, d := range r.Data {
			out[i] = append(out[i], d.Row)
		}
	}
	return out, nil
}

func withRowFormat(stmts []cypher) []map[string]any {
	out := make([]map[string]any, len(stmts))
	for i, s := range stmts {
		m := map[string]any{"statement": s.Statement, "resultDataContents": []string{"row"}}
		if s.Parameters != nil {
			m["parameters"] = s.Parameters
		}
		out[i] = m
	}
	return out
}

// waitReady polls until the server answers a query.
func (n *neo4jStore) waitReady(ctx context.Context, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	for {
		_, err := n.run(ctx, cypher{Statement: "RETURN 1"})
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("neo4j not ready after %s: %w", limit, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// relTypes maps every association type to its Neo4j relationship type. A
// relationship type cannot be a Cypher parameter, so statements are built
// from this fixed allowlist only.
var relTypes = map[domain.RelationshipType]string{
	domain.RelationshipTypeSupports: "SUPPORTS", domain.RelationshipTypeContradicts: "CONTRADICTS",
	domain.RelationshipTypeCites: "CITES", domain.RelationshipTypeCauses: "CAUSES",
	domain.RelationshipTypeCausedBy: "CAUSED_BY", domain.RelationshipTypeActionOf: "ACTION_OF",
	domain.RelationshipTypeOutcomeOf: "OUTCOME_OF", domain.RelationshipTypeValidates: "VALIDATES",
	domain.RelationshipTypeRefutes: "REFUTES", domain.RelationshipTypeDerivedFrom: "DERIVED_FROM",
}

// assocPattern is the alternation of every association type: expansion
// follows associations, never EVIDENCED_BY / DEPENDS_ON / MERGED_INTO.
func assocPattern() string {
	var ts []string
	for _, t := range relTypes {
		ts = append(ts, t)
	}
	slices.Sort(ts)
	return strings.Join(ts, "|")
}

func (n *neo4jStore) setup(ctx context.Context) error {
	_, err := n.run(ctx,
		cypher{Statement: "CREATE CONSTRAINT belief_id IF NOT EXISTS FOR (b:Belief) REQUIRE b.id IS UNIQUE"},
		cypher{Statement: "CREATE CONSTRAINT episode_id IF NOT EXISTS FOR (e:Episode) REQUIRE e.id IS UNIQUE"},
	)
	if err != nil {
		return err
	}
	// A relationship property index lets the dissonance page order by
	// recency without a full scan, like the relational created_at sort.
	_, err = n.run(ctx, cypher{Statement: "CREATE INDEX contradicts_created IF NOT EXISTS FOR ()-[r:CONTRADICTS]-() ON (r.created_at)"})
	if err != nil {
		return err
	}
	_, err = n.run(ctx, cypher{Statement: "CALL db.awaitIndexes(300)"})
	return err
}

func beliefProps(c domain.Claim) map[string]any {
	validTo := ""
	if !c.ValidTo.IsZero() {
		validTo = c.ValidTo.UTC().Format(time.RFC3339Nano)
	}
	return map[string]any{
		"id": c.ID, "text": c.Text, "type": string(c.Type), "status": string(c.Status),
		"confidence": c.Confidence, "created_at": c.CreatedAt.UnixNano(), "valid_to": validTo,
		"merged_into": c.MergedInto,
	}
}

// batchStatements renders one batch as Cypher. create=true uses CREATE (bulk
// load into an empty graph); false uses MERGE, the upsert the production write
// path performs.
func batchStatements(b Batch, create bool) []cypher {
	verb := "MERGE"
	if create {
		verb = "CREATE"
	}
	var eps, beliefs, ev, deps []map[string]any
	for _, e := range b.Events {
		eps = append(eps, map[string]any{"id": e.ID, "run_id": e.RunID, "ts": e.Timestamp.UnixNano()})
	}
	for _, c := range b.Claims {
		beliefs = append(beliefs, beliefProps(c))
	}
	for _, l := range b.Evidence {
		ev = append(ev, map[string]any{"c": l.ClaimID, "e": l.EventID})
	}
	for _, d := range b.Dependencies {
		deps = append(deps, map[string]any{"e": d.EventID, "on": d.DependsOn, "kind": string(d.Kind)})
	}
	var out []cypher
	if len(eps) > 0 {
		out = append(out, cypher{Statement: "UNWIND $rows AS r " + verb + " (e:Episode {id: r.id}) SET e.run_id = r.run_id, e.ts = r.ts, e.retracted = false",
			Parameters: map[string]any{"rows": eps}})
	}
	if len(beliefs) > 0 {
		out = append(out, cypher{Statement: "UNWIND $rows AS r " + verb + " (b:Belief {id: r.id}) SET b += r",
			Parameters: map[string]any{"rows": beliefs}})
	}
	if len(ev) > 0 {
		out = append(out, cypher{Statement: "UNWIND $rows AS r MATCH (b:Belief {id: r.c}) MATCH (e:Episode {id: r.e}) " + verb + " (b)-[:EVIDENCED_BY]->(e)",
			Parameters: map[string]any{"rows": ev}})
	}
	byType := map[string][]map[string]any{}
	for _, r := range b.Relationships {
		t, ok := relTypes[r.Type]
		if !ok {
			continue
		}
		byType[t] = append(byType[t], map[string]any{"id": r.ID, "f": r.FromClaimID, "t": r.ToClaimID, "at": r.CreatedAt.UnixNano()})
	}
	types := make([]string, 0, len(byType))
	for t := range byType {
		types = append(types, t)
	}
	slices.Sort(types)
	for _, t := range types {
		out = append(out, cypher{
			Statement:  "UNWIND $rows AS r MATCH (a:Belief {id: r.f}) MATCH (b:Belief {id: r.t}) " + verb + " (a)-[x:" + t + " {id: r.id}]->(b) SET x.created_at = r.at",
			Parameters: map[string]any{"rows": byType[t]},
		})
	}
	if len(deps) > 0 {
		out = append(out, cypher{Statement: "UNWIND $rows AS r MATCH (a:Episode {id: r.e}) MATCH (b:Episode {id: r.on}) " + verb + " (a)-[d:DEPENDS_ON]->(b) SET d.kind = r.kind",
			Parameters: map[string]any{"rows": deps}})
	}
	return out
}

// load writes the corpus batch by batch, then the merge-redirect edges (a
// redirect may point at a belief from a later batch).
func (n *neo4jStore) load(ctx context.Context, c *Corpus) error {
	if err := n.setup(ctx); err != nil {
		return err
	}
	var merges []map[string]any
	for _, b := range c.Batches {
		if _, err := n.run(ctx, batchStatements(b, true)...); err != nil {
			return err
		}
		for _, cl := range b.Claims {
			if cl.MergedInto != "" {
				merges = append(merges, map[string]any{"f": cl.ID, "t": cl.MergedInto})
			}
		}
	}
	for i := 0; i < len(merges); i += 5000 {
		chunk := merges[i:min(len(merges), i+5000)]
		if _, err := n.run(ctx, cypher{
			Statement:  "UNWIND $rows AS r MATCH (a:Belief {id: r.f}) MATCH (b:Belief {id: r.t}) CREATE (a)-[:MERGED_INTO]->(b)",
			Parameters: map[string]any{"rows": chunk},
		}); err != nil {
			return err
		}
	}
	return nil
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(x)
	}
}

func num(v any) int {
	if x, ok := v.(json.Number); ok {
		i, _ := x.Int64()
		return int(i)
	}
	return 0
}

// expandShortest answers multi-source BFS distance with GQL shortest paths:
// for each (seed, reached) pair the shortest path, then the minimum over
// seeds. One round trip.
func (n *neo4jStore) expandShortest(ctx context.Context, in ExpandInput) ([]string, error) {
	q := fmt.Sprintf(`
MATCH (s:Belief) WHERE s.id IN $seeds
MATCH p = ANY SHORTEST (s)-[:%s]-{1,%d}(m:Belief)
WHERE NOT m.id IN $seeds
WITH m, min(length(p)) AS hop
RETURN m.id, m.text, m.status, hop`, assocPattern(), in.Hops)
	res, err := n.run(ctx, cypher{Statement: q, Parameters: map[string]any{"seeds": in.Seeds}})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(res[0]))
	for _, r := range res[0] {
		out = append(out, expandLine(str(r[0]), num(r[3])))
	}
	return out, nil
}

// expandPerHop mirrors the production loop on the graph: one round trip per
// hop returning the frontier's neighbours with their payload; like
// production, already-seen beliefs are dropped client-side.
func (n *neo4jStore) expandPerHop(ctx context.Context, in ExpandInput) ([]string, error) {
	q := fmt.Sprintf(`
MATCH (a:Belief)-[:%s]-(b:Belief) WHERE a.id IN $frontier
RETURN DISTINCT b.id, b.text, b.status`, assocPattern())
	seenSet := map[string]bool{}
	for _, s := range in.Seeds {
		seenSet[s] = true
	}
	frontier := slices.Clone(in.Seeds)
	var out []string
	for hop := 1; hop <= in.Hops && len(frontier) > 0; hop++ {
		res, err := n.run(ctx, cypher{Statement: q, Parameters: map[string]any{"frontier": frontier}})
		if err != nil {
			return nil, err
		}
		frontier = frontier[:0]
		for _, r := range res[0] {
			id := str(r[0])
			if seenSet[id] {
				continue
			}
			seenSet[id] = true
			frontier = append(frontier, id)
			out = append(out, expandLine(id, hop))
		}
	}
	return out, nil
}

// expandAPOC is the usual graph-database answer: one multi-source
// breadth-first traversal with global node uniqueness (APOC core's
// spanningTree), so each belief is reached once, at its BFS distance.
func (n *neo4jStore) expandAPOC(ctx context.Context, in ExpandInput) ([]string, error) {
	res, err := n.run(ctx, cypher{Statement: `
MATCH (s:Belief) WHERE s.id IN $seeds
WITH collect(s) AS starts
CALL apoc.path.spanningTree(starts, {relationshipFilter: $rels, labelFilter: '+Belief', maxLevel: $hops, bfs: true})
YIELD path
WITH last(nodes(path)) AS m, length(path) AS hop WHERE hop > 0
RETURN m.id, m.text, m.status, hop`, Parameters: map[string]any{"seeds": in.Seeds, "hops": in.Hops, "rels": assocPattern()}})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(res[0]))
	for _, r := range res[0] {
		out = append(out, expandLine(str(r[0]), num(r[3])))
	}
	return out, nil
}

// retract answers the retraction read side in one round trip: the dependency
// closure with BFS levels, and every dependent belief with its live
// independent evidence count. Taint is the general rule (an episode depending
// on root or on any retracted episode), not "is in the closure".
func (n *neo4jStore) retract(ctx context.Context, in RetractInput) ([]string, error) {
	closure := `
MATCH (root:Episode {id: $root})
MATCH p = ANY SHORTEST (d:Episode)-[:DEPENDS_ON]->+(root)
WHERE d <> root
RETURN d.id, length(p)`
	beliefs := `
MATCH (root:Episode {id: $root})
CALL (root) {
  MATCH (b:Belief)-[:EVIDENCED_BY]->(root) RETURN b
  UNION
  MATCH (b:Belief)-[:EVIDENCED_BY]->(:Episode)-[:DEPENDS_ON]->+(root) RETURN b
}
WITH DISTINCT root, b
RETURN b.id, b.status, EXISTS { (b)-[:EVIDENCED_BY]->(root) },
  COUNT { (b)-[:EVIDENCED_BY]->(e:Episode)
          WHERE e <> root AND NOT e.retracted
            AND NOT EXISTS { (e)-[:DEPENDS_ON]->+(x:Episode) WHERE x = root OR x.retracted } }`
	params := map[string]any{"root": in.Root}
	res, err := n.run(ctx, cypher{Statement: closure, Parameters: params}, cypher{Statement: beliefs, Parameters: params})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range res[0] {
		out = append(out, closureLine(str(r[0]), num(r[1])))
	}
	for _, r := range res[1] {
		direct, _ := r[2].(bool)
		out = append(out, outcomeLine(str(r[0]), retractOutcome(direct, num(r[3]), domain.ClaimStatus(str(r[1])))))
	}
	return out, nil
}

func (n *neo4jStore) dissonance(ctx context.Context, in PageInput) ([]string, error) {
	res, err := n.run(ctx,
		cypher{Statement: "MATCH ()-[r:CONTRADICTS]->() RETURN count(r)"},
		cypher{Statement: `
MATCH (a:Belief)-[r:CONTRADICTS]->(b:Belief)
WITH a, r, b ORDER BY r.created_at DESC SKIP $off LIMIT $lim
RETURN r.id, a.id, a.text, b.id, b.text`, Parameters: map[string]any{"off": in.Offset, "lim": in.Limit}})
	if err != nil {
		return nil, err
	}
	out := []string{totalLine(num(res[0][0][0]))}
	for _, r := range res[1] {
		out = append(out, relLine(str(r[0])))
	}
	return out, nil
}

func (n *neo4jStore) neighbourhood(ctx context.Context, in IDsInput) ([]string, error) {
	res, err := n.run(ctx, cypher{Statement: `
MATCH (a:Belief)-[r:CONTRADICTS]-(:Belief) WHERE a.id IN $ids
RETURN DISTINCT r.id`, Parameters: map[string]any{"ids": dedupe(in.IDs)}})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(res[0]))
	for _, r := range res[0] {
		out = append(out, relLine(str(r[0])))
	}
	return out, nil
}

// redirects follows MERGED_INTO to the first live belief within the hop bound.
func (n *neo4jStore) redirects(ctx context.Context, in IDsInput) ([]string, error) {
	q := fmt.Sprintf(`
UNWIND $ids AS id
MATCH (c:Belief {id: id})
MATCH p = (c)-[:MERGED_INTO*1..%d]->(t:Belief)
WHERE t.status <> 'deprecated' AND t.valid_to = ''
  AND all(x IN nodes(p)[1..-1] WHERE x.status = 'deprecated' OR x.valid_to <> '')
WITH DISTINCT c, t WHERE t.id <> c.id
RETURN c.id, t.id`, maxRedirectHops)
	res, err := n.run(ctx, cypher{Statement: q, Parameters: map[string]any{"ids": dedupe(in.IDs)}})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(res[0]))
	for _, r := range res[0] {
		out = append(out, redirectLine(str(r[0]), str(r[1])))
	}
	return out, nil
}

func (n *neo4jStore) provenance(ctx context.Context, in IDsInput) ([]string, error) {
	res, err := n.run(ctx, cypher{Statement: `
MATCH (b:Belief)-[:EVIDENCED_BY]->(e:Episode) WHERE b.id IN $ids
WITH collect(DISTINCT e) AS evs
CALL (evs) {
  UNWIND evs AS e RETURN e.id AS id, 0 AS d
  UNION ALL
  UNWIND evs AS e
  MATCH p = ANY SHORTEST (e)-[:DEPENDS_ON]->+(x:Episode)
  RETURN x.id AS id, length(p) AS d
}
RETURN id, min(d)`, Parameters: map[string]any{"ids": dedupe(in.IDs)}})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(res[0]))
	for _, r := range res[0] {
		out = append(out, episodeLine(str(r[0]), num(r[1])))
	}
	return out, nil
}

// write upserts one batch in one transaction (one round trip), with MERGE.
func (n *neo4jStore) write(ctx context.Context, in WriteInput) ([]string, error) {
	if _, err := n.run(ctx, batchStatements(in.Batch, false)...); err != nil {
		return nil, err
	}
	return []string{writeLine(len(in.Batch.Claims), len(in.Batch.Relationships))}, nil
}

func (n *neo4jStore) ping(ctx context.Context, _ any) ([]string, error) {
	_, err := n.run(ctx, cypher{Statement: "RETURN 1"})
	return nil, err
}

func (n *neo4jStore) variants() map[string][]Variant {
	v := func(path string, fn func(context.Context, any) ([]string, error)) Variant {
		return Variant{Store: "neo4j", Path: path, run: fn}
	}
	return map[string][]Variant{
		wExpand2:    {v("cypher-apoc-bfs", typed(n.expandAPOC)), v("cypher-per-hop", typed(n.expandPerHop)), v("cypher-shortest", typed(n.expandShortest))},
		wExpand5:    {v("cypher-apoc-bfs", typed(n.expandAPOC)), v("cypher-per-hop", typed(n.expandPerHop)), v("cypher-shortest", typed(n.expandShortest))},
		wRetract:    {v("cypher", typed(n.retract))},
		wDissonance: {v("cypher", typed(n.dissonance))},
		wNeighbour:  {v("cypher", typed(n.neighbourhood))},
		wRedirects:  {v("cypher", typed(n.redirects))},
		wProvenance: {v("cypher", typed(n.provenance))},
		wWrite:      {v("cypher", typed(n.write))},
	}
}
