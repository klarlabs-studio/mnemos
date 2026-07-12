package consolidate

// Knowledge synthesis — the claim-derived promotion path (ADR 0012 "Path A").
//
// The operational path (synthesize → promote.go) distils Action→Outcome chains
// into Lessons and promotes the safe cross-tenant subset to the global brain.
// That path is empty for a pure KNOWLEDGE domain: pet-medical facts ("Golden
// Retrievers are predisposed to diabetes") are classified claims, not the
// outcome of any recorded operational action, so nothing operational is ever
// synthesized from them and nothing promotes.
//
// SynthesizeKnowledgeSchemas closes that gap. It turns a tenant's CLASS-LEVEL
// claims directly into [domain.Schema] (=Lesson) values that feed the SAME pure
// promotion engine (consolidate.Promote) as operational lessons. The union of
// {operational lessons ∪ knowledge schemas} is passed per tenant into Promote,
// so knowledge promotes emergently (corroborated across ≥N tenants) or curated
// (single-source + promote:global), running every existing gate unchanged.
//
// The privacy invariant is enforced here as well as at Promote's gate 0: ONLY
// class-level claims are considered; individual and unknown (the empty zero
// value) claims are skipped, fail-closed. A statement about a specific pet/owner
// can never become a knowledge schema and therefore can never reach promotion.
//
// These schemas are TRANSIENT inputs to promotion. Their Evidence holds the
// backing CLAIM ids (for corroboration count and provenance), not action ids, so
// they must NEVER be persisted into the lessons table (whose lesson_evidence FKs
// to actions). Only the de-identified GlobalSchemas that clear promotion persist
// — that write path (promote.go applyPromotion) is unchanged.

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/relate"
)

// KnowledgeEquivalenceJaccard is the statement-similarity threshold at which two
// class-level claims are treated as the same knowledge fact and clustered into
// one schema. It mirrors DefaultEquivalenceJaccard (the cross-tenant promotion
// clustering threshold) so the two clustering passes never drift: Jaccard is
// deliberately strict, penalizing tokens unique to EITHER statement, so two
// claims sharing a common verb but differing on the key noun ("retrievers get
// diabetes" vs "retrievers get hip dysplasia") stay in separate clusters.
const KnowledgeEquivalenceJaccard = DefaultEquivalenceJaccard

// knowledgeItem is one class-level claim paired with its precomputed content
// tokens and a canonical sort key, so clustering is deterministic and
// independent of input order.
type knowledgeItem struct {
	claim  domain.Claim
	tokens map[string]struct{}
	key    string // statement\x00id — canonical, stable ordering key
}

// SynthesizeKnowledgeSchemas turns a tenant's classified claims into knowledge
// schemas (ADR 0012 Path A). It:
//
//   - Considers ONLY class-level claims (domain.EligibleForPromotion). Individual
//     and unknown claims are skipped, fail-closed — the privacy invariant.
//   - Clusters the survivors by normalized-statement similarity, REUSING
//     relate.ContentTokens + Jaccard (the same tokenization and threshold as
//     cross-tenant promotion clustering), so distinct-noun claims don't merge.
//   - Emits one class-level [domain.Schema] per cluster: Statement is the
//     representative (highest-confidence) claim's text, Confidence is the
//     aggregate (max) across the cluster, Evidence is the backing claim ids (for
//     count/provenance only), Source is domain.SchemaSourceKnowledge, Polarity is
//     positive, and SubjectClass is domain.SubjectClassClass.
//
// The result is pure and in-memory; the caller unions it with the tenant's
// operational lessons and passes the union into consolidate.Promote. The engine
// is deterministic: the returned schemas are ordered by id.
func SynthesizeKnowledgeSchemas(claims []domain.Claim) []domain.Schema {
	items := make([]knowledgeItem, 0, len(claims))
	for _, c := range claims {
		// Eligibility gate (fail-closed): only positively class-level claims.
		if !domain.EligibleForPromotion(c.SubjectClass) {
			continue
		}
		toks := relate.ContentTokens(c.Text)
		if len(toks) == 0 {
			// A claim with no content tokens cannot corroborate or de-identify;
			// there is nothing to cluster on.
			continue
		}
		items = append(items, knowledgeItem{
			claim:  c,
			tokens: toks,
			key:    c.Text + "\x00" + c.ID,
		})
	}
	if len(items) == 0 {
		return nil
	}

	// Deterministic greedy clustering: sort canonically first so assignment is
	// stable, then a fixpoint merge collapses any two clusters whose anchors are
	// equivalent — making the result invariant under input permutation, mirroring
	// clusterMembers in promote.go.
	sort.Slice(items, func(i, j int) bool { return items[i].key < items[j].key })

	type kcluster struct {
		anchor  map[string]struct{}
		members []knowledgeItem
	}
	var clusters []kcluster
	for _, it := range items {
		idx := -1
		for i := range clusters {
			if jaccard(clusters[i].anchor, it.tokens) >= KnowledgeEquivalenceJaccard {
				idx = i
				break
			}
		}
		if idx == -1 {
			clusters = append(clusters, kcluster{anchor: it.tokens, members: []knowledgeItem{it}})
			continue
		}
		clusters[idx].members = append(clusters[idx].members, it)
	}
	for {
		merged := false
		for i := 0; i < len(clusters) && !merged; i++ {
			for j := i + 1; j < len(clusters); j++ {
				if jaccard(clusters[i].anchor, clusters[j].anchor) >= KnowledgeEquivalenceJaccard {
					clusters[i].members = append(clusters[i].members, clusters[j].members...)
					clusters = append(clusters[:j], clusters[j+1:]...)
					merged = true
					break
				}
			}
		}
		if !merged {
			break
		}
	}

	out := make([]domain.Schema, 0, len(clusters))
	for _, cl := range clusters {
		best := cl.members[0]
		maxConf := best.claim.Confidence
		evidence := make([]string, 0, len(cl.members))
		for _, m := range cl.members {
			evidence = append(evidence, m.claim.ID)
			if m.claim.Confidence > best.claim.Confidence ||
				(m.claim.Confidence == best.claim.Confidence && m.key < best.key) {
				best = m
			}
			if m.claim.Confidence > maxConf {
				maxConf = m.claim.Confidence
			}
		}
		sort.Strings(evidence)
		out = append(out, domain.Schema{
			ID:           knowledgeSchemaID(best.claim.Text),
			Statement:    best.claim.Text,
			Confidence:   maxConf,
			Evidence:     evidence,
			Polarity:     domain.SchemaPolarityPositive,
			SubjectClass: domain.SubjectClassClass,
			Source:       domain.SchemaSourceKnowledge,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// knowledgeSchemaID derives a stable, content-addressed id for a knowledge
// schema from its representative statement, so re-running synthesis over the
// same claims yields the same id (deterministic clustering + promotion). It is
// namespaced ("ksch_") distinctly from operational and global schema ids.
func knowledgeSchemaID(statement string) string {
	h := sha256.Sum256([]byte("knowledge\x00" + statement))
	return "ksch_" + hex.EncodeToString(h[:12])
}
