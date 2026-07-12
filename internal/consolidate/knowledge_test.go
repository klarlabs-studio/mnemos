package consolidate

import (
	"context"
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
)

// classClaim builds an active class-level claim (eligible for the knowledge
// path). Individual/unknown variants are built inline where a test needs them.
func classClaim(id, text string, conf float64) domain.Claim {
	return domain.Claim{
		ID:           id,
		Text:         text,
		Confidence:   conf,
		Status:       domain.ClaimStatusActive,
		SubjectClass: domain.SubjectClassClass,
	}
}

func schemaWithStatement(schemas []domain.Schema, substr string) (domain.Schema, bool) {
	for _, s := range schemas {
		if contains(s.Statement, substr) {
			return s, true
		}
	}
	return domain.Schema{}, false
}

// TestSynthesizeKnowledgeSchemas_ClustersClassClaims verifies that class-level
// claims stating the same fact cluster into ONE knowledge schema, marked
// class-level and knowledge-sourced, carrying the backing claim ids as evidence.
func TestSynthesizeKnowledgeSchemas_ClustersClassClaims(t *testing.T) {
	claims := []domain.Claim{
		classClaim("c1", "Golden Retrievers are predisposed to diabetes", 0.8),
		classClaim("c2", "Golden Retrievers are predisposed to diabetes", 0.9),
	}
	schemas := SynthesizeKnowledgeSchemas(claims)
	if len(schemas) != 1 {
		t.Fatalf("expected 1 clustered knowledge schema, got %d: %+v", len(schemas), schemas)
	}
	s := schemas[0]
	if s.SubjectClass != domain.SubjectClassClass {
		t.Fatalf("knowledge schema subject class = %q, want class", s.SubjectClass)
	}
	if s.Source != domain.SchemaSourceKnowledge {
		t.Fatalf("knowledge schema source = %q, want %q", s.Source, domain.SchemaSourceKnowledge)
	}
	if s.Confidence != 0.9 {
		t.Fatalf("knowledge schema confidence = %v, want 0.9 (aggregate max)", s.Confidence)
	}
	if len(s.Evidence) != 2 {
		t.Fatalf("knowledge schema evidence = %v, want the 2 backing claim ids", s.Evidence)
	}
	for _, e := range s.Evidence {
		if e != "c1" && e != "c2" {
			t.Fatalf("evidence entry %q is not a backing claim id", e)
		}
	}
}

// TestSynthesizeKnowledgeSchemas_ExcludesIndividualAndUnknown is the privacy
// invariant at the synthesis layer: individual and unknown claims are skipped
// fail-closed and never become knowledge schemas.
func TestSynthesizeKnowledgeSchemas_ExcludesIndividualAndUnknown(t *testing.T) {
	claims := []domain.Claim{
		{ID: "ind", Text: "Rex the Golden Retriever has diabetes", Confidence: 0.9, Status: domain.ClaimStatusActive, SubjectClass: domain.SubjectClassIndividual},
		{ID: "unk", Text: "some unclassified assertion about a thing", Confidence: 0.9, Status: domain.ClaimStatusActive, SubjectClass: domain.SubjectClassUnknown},
		classClaim("cls", "Golden Retrievers are predisposed to diabetes", 0.8),
	}
	schemas := SynthesizeKnowledgeSchemas(claims)
	if len(schemas) != 1 {
		t.Fatalf("only the class claim should synthesize; got %d schemas: %+v", len(schemas), schemas)
	}
	if _, ok := schemaWithStatement(schemas, "Rex"); ok {
		t.Fatalf("individual claim leaked into a knowledge schema")
	}
	if _, ok := schemaWithStatement(schemas, "unclassified"); ok {
		t.Fatalf("unknown claim leaked into a knowledge schema")
	}
}

// TestSynthesizeKnowledgeSchemas_DistinctNounsDoNotMerge verifies the Jaccard
// threshold keeps genuinely different facts in separate schemas even when they
// share a common subject noun.
func TestSynthesizeKnowledgeSchemas_DistinctNounsDoNotMerge(t *testing.T) {
	claims := []domain.Claim{
		classClaim("d1", "Golden Retrievers are predisposed to diabetes", 0.8),
		classClaim("d2", "Dachshunds are predisposed to intervertebral disc disease", 0.8),
	}
	schemas := SynthesizeKnowledgeSchemas(claims)
	if len(schemas) != 2 {
		t.Fatalf("distinct-noun claims must not over-merge; got %d schemas: %+v", len(schemas), schemas)
	}
}

// TestSynthesizeKnowledgeSchemas_EmptyInputs verifies the no-op / no-token paths.
func TestSynthesizeKnowledgeSchemas_EmptyInputs(t *testing.T) {
	if got := SynthesizeKnowledgeSchemas(nil); got != nil {
		t.Fatalf("nil input should yield nil, got %+v", got)
	}
	// A class claim whose statement is all stop words has no content tokens.
	only := SynthesizeKnowledgeSchemas([]domain.Claim{classClaim("s", "the a an of", 0.9)})
	if len(only) != 0 {
		t.Fatalf("a class claim with no content tokens must not synthesize, got %+v", only)
	}
}

// TestKnowledgePromotion_EmergentAcrossTenants is the ADR 0012 Path A end-to-end:
// the SAME class knowledge produced independently in ≥ MinTenants distinct
// tenants promotes emergently through the unchanged promote engine.
func TestKnowledgePromotion_EmergentAcrossTenants(t *testing.T) {
	fact := "Golden Retrievers are predisposed to diabetes"
	var tenants []TenantLessons
	for _, tn := range []string{"vetclinic-a", "vetclinic-b", "vetclinic-c"} {
		schemas := SynthesizeKnowledgeSchemas([]domain.Claim{classClaim("c-"+tn, fact, 0.85)})
		tenants = append(tenants, TenantLessons{Tenant: tn, Lessons: schemas})
	}

	p := NewPromoter(nil, nil)
	res, err := p.Promote(context.Background(), tenants, Options{Gate: GateAuto})
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if !hasStatement(res.Promoted, "Golden Retrievers are predisposed to diabetes") {
		t.Fatalf("class knowledge corroborated in 3 tenants must promote emergently; promoted=%+v skipped=%+v", res.Promoted, res.Skipped)
	}
	for _, c := range res.Promoted {
		if contains(c.Statement, "Golden Retrievers") && c.DistinctTenants != 3 {
			t.Fatalf("emergent knowledge distinct tenants = %d, want 3", c.DistinctTenants)
		}
	}
}

// TestKnowledgePromotion_SingleTenantNeedsCurate verifies the two knowledge
// paths: a single-tenant class fact is BLOCKED on the emergent path (insufficient
// corroboration) and only reaches promotion via the curated single-source path.
func TestKnowledgePromotion_SingleTenantNeedsCurate(t *testing.T) {
	fact := "the newly-described funnel-web spider envenomation responds to antivenom X"
	schemas := SynthesizeKnowledgeSchemas([]domain.Claim{classClaim("spider1", fact, 0.9)})
	tenants := []TenantLessons{{Tenant: "solo-vet", Lessons: schemas}}

	p := NewPromoter(nil, nil)

	// Emergent (default): single source cannot promote.
	emergent, err := p.Promote(context.Background(), tenants, Options{Gate: GateAuto})
	if err != nil {
		t.Fatalf("Promote emergent: %v", err)
	}
	if hasStatement(emergent.Promoted, "funnel-web") || hasStatement(emergent.Pending, "funnel-web") {
		t.Fatalf("single-tenant class fact must NOT promote on the emergent path: %+v", emergent.Promoted)
	}
	if s, ok := skipReason(emergent, "funnel-web"); !ok || s.Reason != ReasonInsufficientCorroboration {
		t.Fatalf("single-tenant class fact must be skipped for insufficient corroboration, got ok=%v reason=%q", ok, s.Reason)
	}

	// Curated: the same single-source class fact promotes.
	curated, err := p.Promote(context.Background(), tenants, Options{Gate: GateAuto, Curated: true})
	if err != nil {
		t.Fatalf("Promote curated: %v", err)
	}
	if !hasStatement(curated.Promoted, "funnel-web") {
		t.Fatalf("single-source class fact must promote on the curated path; promoted=%+v skipped=%+v", curated.Promoted, curated.Skipped)
	}
	for _, c := range curated.Promoted {
		if contains(c.Statement, "funnel-web") && !c.Curated {
			t.Fatalf("curated knowledge candidate must carry Curated=true for audit")
		}
	}
}

// TestKnowledgePromotion_IndividualNeverPromotes verifies individual claims never
// reach promoted/pending — neither via synthesis (which excludes them) nor, as
// defense-in-depth, if an individual schema were somehow fed to Promote directly
// (gate 0 blocks it) — even on the curated path.
func TestKnowledgePromotion_IndividualNeverPromotes(t *testing.T) {
	// Path 1: synthesis excludes individual claims, so nothing is produced.
	indClaims := []domain.Claim{
		{ID: "rex", Text: "Rex has been diagnosed with diabetes this week", Confidence: 0.95, Status: domain.ClaimStatusActive, SubjectClass: domain.SubjectClassIndividual},
	}
	if got := SynthesizeKnowledgeSchemas(indClaims); len(got) != 0 {
		t.Fatalf("individual claims must not synthesize into knowledge schemas, got %+v", got)
	}

	// Path 2 (defense in depth): even a hand-forged individual schema on the
	// curated path is blocked by gate 0 and lands in Skipped only.
	forged := domain.Lesson{ID: "forged", Statement: "Rex has been diagnosed with diabetes this week", Confidence: 0.95, Evidence: []string{"rex"}, SubjectClass: domain.SubjectClassIndividual}
	p := NewPromoter(nil, nil)
	res, err := p.Promote(context.Background(), []TenantLessons{{Tenant: "solo", Lessons: []domain.Lesson{forged}}}, Options{Gate: GateAuto, Curated: true})
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if hasStatement(res.Promoted, "Rex") || hasStatement(res.Pending, "Rex") {
		t.Fatalf("LEAK: individual subject promoted/pending on curated path: %+v %+v", res.Promoted, res.Pending)
	}
	if s, ok := skipReason(res, "Rex"); !ok || s.Reason != ReasonIneligibleSubject {
		t.Fatalf("individual subject must be skipped as ineligible, got ok=%v reason=%q", ok, s.Reason)
	}
}
