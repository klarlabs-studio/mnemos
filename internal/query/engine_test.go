package query

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

type fakeEventRepo struct {
	events []domain.Event
}

func (f fakeEventRepo) Append(_ context.Context, _ domain.Event) error { return nil }
func (f fakeEventRepo) GetByID(_ context.Context, _ string) (domain.Event, error) {
	return domain.Event{}, nil
}
func (f fakeEventRepo) ListByIDs(_ context.Context, _ []string) ([]domain.Event, error) {
	return nil, nil
}
func (f fakeEventRepo) ListAll(_ context.Context) ([]domain.Event, error) { return f.events, nil }
func (f fakeEventRepo) CountAll(_ context.Context) (int64, error) {
	return int64(len(f.events)), nil
}
func (f fakeEventRepo) DeleteByID(_ context.Context, _ string) error { return nil }
func (f fakeEventRepo) DeleteAll(_ context.Context) error            { return nil }
func (f fakeEventRepo) ListByRunID(_ context.Context, runID string) ([]domain.Event, error) {
	filtered := make([]domain.Event, 0)
	for _, event := range f.events {
		if event.RunID == runID {
			filtered = append(filtered, event)
		}
	}
	return filtered, nil
}

type fakeClaimRepo struct {
	claims   []domain.Claim
	evidence []domain.ClaimEvidence
	// verifiedRecorder, when non-nil, records each MarkVerified'd claim id so a test
	// can assert the reconsolidation freshness touch fired on the recalled set.
	verifiedRecorder *[]string
	// creditWrites, when non-nil, records the components map written by each
	// ApplyBeliefCredit call (keyed by claim id) so a test can assert the
	// competitive-inhibition write-back. Also makes the fake a ports.BeliefCreditWriter.
	creditWrites *map[string]map[string]float64
}

func (f fakeClaimRepo) ApplyBeliefCredit(_ context.Context, claimID string, components map[string]float64, _ float64) error {
	if f.creditWrites != nil {
		cp := make(map[string]float64, len(components))
		for k, v := range components {
			cp[k] = v
		}
		(*f.creditWrites)[claimID] = cp
	}
	return nil
}

func (f fakeClaimRepo) Upsert(_ context.Context, _ []domain.Claim) error { return nil }
func (f fakeClaimRepo) UpsertWithReason(_ context.Context, _ []domain.Claim, _ string) error {
	return nil
}
func (f fakeClaimRepo) UpsertWithReasonAs(_ context.Context, _ []domain.Claim, _, _ string) error {
	return nil
}
func (f fakeClaimRepo) UpsertEvidence(_ context.Context, _ []domain.ClaimEvidence) error {
	return nil
}
func (f fakeClaimRepo) ListAll(_ context.Context) ([]domain.Claim, error) { return f.claims, nil }
func (f fakeClaimRepo) ListByTestRequirementRef(_ context.Context, ref string) ([]domain.Claim, error) {
	if ref == "" {
		return nil, nil
	}
	out := make([]domain.Claim, 0)
	for _, c := range f.claims {
		if c.Type == domain.ClaimTypeTestResult && c.TestRequirementRef == ref {
			out = append(out, c)
		}
	}
	return out, nil
}
func (f fakeClaimRepo) SetValidity(_ context.Context, _ string, _ time.Time) error {
	return nil
}
func (f fakeClaimRepo) SetLifecycle(_ context.Context, _ string, _ domain.ClaimLifecycle) error {
	return nil
}
func (f fakeClaimRepo) MarkVerified(_ context.Context, claimID string, _ time.Time, _ float64) error {
	if f.verifiedRecorder != nil {
		*f.verifiedRecorder = append(*f.verifiedRecorder, claimID)
	}
	return nil
}
func (f fakeClaimRepo) RepointEvidence(_ context.Context, _, _ string) error { return nil }
func (f fakeClaimRepo) DeleteCascade(_ context.Context, _ string) error      { return nil }
func (f fakeClaimRepo) ListByEventIDs(_ context.Context, _ []string) ([]domain.Claim, error) {
	return f.claims, nil
}
func (f fakeClaimRepo) ListEvidenceByClaimIDs(_ context.Context, claimIDs []string) ([]domain.ClaimEvidence, error) {
	wanted := map[string]struct{}{}
	for _, id := range claimIDs {
		wanted[id] = struct{}{}
	}
	out := make([]domain.ClaimEvidence, 0, len(f.evidence))
	for _, e := range f.evidence {
		if _, ok := wanted[e.ClaimID]; ok {
			out = append(out, e)
		}
	}
	return out, nil
}
func (f fakeClaimRepo) ListStatusHistoryByClaimID(_ context.Context, _ string) ([]domain.ClaimStatusTransition, error) {
	return nil, nil
}
func (f fakeClaimRepo) ListByIDs(_ context.Context, claimIDs []string) ([]domain.Claim, error) {
	wanted := map[string]struct{}{}
	for _, id := range claimIDs {
		wanted[id] = struct{}{}
	}
	out := make([]domain.Claim, 0, len(claimIDs))
	for _, c := range f.claims {
		if _, ok := wanted[c.ID]; ok {
			out = append(out, c)
		}
	}
	return out, nil
}
func (f fakeClaimRepo) CountAll(_ context.Context) (int64, error) {
	return int64(len(f.claims)), nil
}
func (f fakeClaimRepo) ListAllEvidence(_ context.Context) ([]domain.ClaimEvidence, error) {
	out := make([]domain.ClaimEvidence, len(f.evidence))
	copy(out, f.evidence)
	return out, nil
}
func (f fakeClaimRepo) ListAllStatusHistory(_ context.Context) ([]domain.ClaimStatusTransition, error) {
	return nil, nil
}
func (f fakeClaimRepo) DeleteAll(_ context.Context) error { return nil }
func (f fakeClaimRepo) ListIDsMissingEmbedding(_ context.Context) ([]string, error) {
	return nil, nil
}

type fakeRelationshipRepo struct {
	rels map[string][]domain.Relationship
	// strengthenedWith, when non-nil, records each StrengthenAssociations call's id
	// set so a test can assert the Hebbian write-back fired with the co-retrieved set.
	strengthenedWith *[][]string
}

func (f fakeRelationshipRepo) Upsert(_ context.Context, _ []domain.Relationship) error { return nil }

// StrengthenAssociations implements ports.RelationshipStrengthener faithfully over the
// fake's dual-indexed edge map (an edge is stored under both endpoints, so both copies
// are incremented). Records the call set when a recorder is wired.
func (f fakeRelationshipRepo) StrengthenAssociations(_ context.Context, claimIDs []string, delta, maxStrength float64) (int, error) {
	if delta <= 0 || len(claimIDs) < 2 {
		return 0, nil
	}
	if f.strengthenedWith != nil {
		*f.strengthenedWith = append(*f.strengthenedWith, append([]string(nil), claimIDs...))
	}
	set := make(map[string]bool, len(claimIDs))
	for _, id := range claimIDs {
		set[id] = true
	}
	matched := map[string]bool{}
	for _, id := range claimIDs {
		for _, r := range f.rels[id] {
			if set[r.FromClaimID] && set[r.ToClaimID] {
				matched[r.ID] = true
			}
		}
	}
	for key := range f.rels {
		list := f.rels[key]
		for i := range list {
			if !matched[list[i].ID] {
				continue
			}
			s := list[i].Strength
			if s <= 0 {
				s = 1
			}
			s += delta
			if s > maxStrength {
				s = maxStrength
			}
			list[i].Strength = s
		}
	}
	return len(matched), nil
}
func (f fakeRelationshipRepo) DecayAssociations(_ context.Context, retain float64) (int, error) {
	if retain < 0 || retain >= 1 {
		return 0, nil
	}
	n := 0
	for key := range f.rels {
		list := f.rels[key]
		for i := range list {
			if list[i].Strength <= 1 {
				continue
			}
			list[i].Strength = 1 + (list[i].Strength-1)*retain
			n++
		}
	}
	return n, nil
}
func (f fakeRelationshipRepo) RepointEndpoint(_ context.Context, _, _ string) error { return nil }
func (f fakeRelationshipRepo) DeleteByClaim(_ context.Context, _ string) error      { return nil }
func (f fakeRelationshipRepo) ListByClaim(_ context.Context, claimID string) ([]domain.Relationship, error) {
	return f.rels[claimID], nil
}
func (f fakeRelationshipRepo) ListByClaimIDs(_ context.Context, claimIDs []string) ([]domain.Relationship, error) {
	seen := map[string]struct{}{}
	out := make([]domain.Relationship, 0)
	for _, id := range claimIDs {
		for _, rel := range f.rels[id] {
			if _, dup := seen[rel.ID]; dup {
				continue
			}
			seen[rel.ID] = struct{}{}
			out = append(out, rel)
		}
	}
	return out, nil
}
func (f fakeRelationshipRepo) CountAll(_ context.Context) (int64, error) {
	var n int64
	seen := map[string]struct{}{}
	for _, list := range f.rels {
		for _, rel := range list {
			if _, dup := seen[rel.ID]; dup {
				continue
			}
			seen[rel.ID] = struct{}{}
			n++
		}
	}
	return n, nil
}
func (f fakeRelationshipRepo) CountByType(_ context.Context, relType string) (int64, error) {
	var n int64
	seen := map[string]struct{}{}
	for _, list := range f.rels {
		for _, rel := range list {
			if _, dup := seen[rel.ID]; dup {
				continue
			}
			seen[rel.ID] = struct{}{}
			if string(rel.Type) == relType {
				n++
			}
		}
	}
	return n, nil
}
func (f fakeRelationshipRepo) DeleteAll(_ context.Context) error { return nil }
func (f fakeRelationshipRepo) ListAll(_ context.Context) ([]domain.Relationship, error) {
	seen := map[string]struct{}{}
	out := make([]domain.Relationship, 0)
	for _, list := range f.rels {
		for _, rel := range list {
			if _, dup := seen[rel.ID]; dup {
				continue
			}
			seen[rel.ID] = struct{}{}
			out = append(out, rel)
		}
	}
	return out, nil
}

func TestAnswerIncludesClaimsAndContradictions(t *testing.T) {
	now := time.Date(2026, 4, 12, 17, 0, 0, 0, time.UTC)

	events := fakeEventRepo{events: []domain.Event{
		{ID: "ev_1", RunID: "run_1", Content: "Revenue decreased after launch", Timestamp: now},
		{ID: "ev_2", RunID: "run_2", Content: "Churn increased in Q2", Timestamp: now.Add(time.Minute)},
	}}

	claims := []domain.Claim{
		{ID: "cl_1", Text: "Revenue decreased after launch", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive, Confidence: 0.8, CreatedAt: now},
		{ID: "cl_2", Text: "Revenue did not decrease after launch", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive, Confidence: 0.8, CreatedAt: now},
	}

	rels := fakeRelationshipRepo{rels: map[string][]domain.Relationship{
		"cl_1": {{ID: "rl_1", Type: domain.RelationshipTypeContradicts, FromClaimID: "cl_1", ToClaimID: "cl_2", CreatedAt: now}},
		"cl_2": {{ID: "rl_1", Type: domain.RelationshipTypeContradicts, FromClaimID: "cl_1", ToClaimID: "cl_2", CreatedAt: now}},
	}}

	engine := NewEngine(events, fakeClaimRepo{claims: claims}, rels)
	answer, err := engine.Answer("what happened to revenue after launch")
	if err != nil {
		t.Fatalf("Answer() error = %v", err)
	}

	if len(answer.Claims) != 2 {
		t.Fatalf("Claims len = %d, want 2", len(answer.Claims))
	}
	if len(answer.Contradictions) != 1 {
		t.Fatalf("Contradictions len = %d, want 1", len(answer.Contradictions))
	}
	if len(answer.TimelineEventIDs) == 0 {
		t.Fatal("TimelineEventIDs should not be empty")
	}
	if !strings.Contains(answer.AnswerText, "strongest signal") {
		t.Fatalf("AnswerText = %q, expected strongest signal narrative", answer.AnswerText)
	}
	if !strings.Contains(answer.AnswerText, "contested") {
		t.Fatalf("AnswerText = %q, expected contradiction context", answer.AnswerText)
	}
}

func TestAnswerForRunScopesEvents(t *testing.T) {
	now := time.Date(2026, 4, 12, 18, 0, 0, 0, time.UTC)
	events := fakeEventRepo{events: []domain.Event{
		{ID: "ev_run_a", RunID: "run_a", Content: "Revenue decreased after launch", Timestamp: now},
		{ID: "ev_run_b", RunID: "run_b", Content: "Churn increased in Q2", Timestamp: now.Add(time.Minute)},
	}}
	claims := []domain.Claim{
		{ID: "cl_1", Text: "Revenue decreased after launch", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive, Confidence: 0.8, CreatedAt: now},
	}
	engine := NewEngine(events, fakeClaimRepo{claims: claims}, fakeRelationshipRepo{rels: map[string][]domain.Relationship{}})

	answer, err := engine.AnswerForRun("what happened to revenue", "run_a")
	if err != nil {
		t.Fatalf("AnswerForRun() error = %v", err)
	}
	if len(answer.TimelineEventIDs) != 1 {
		t.Fatalf("TimelineEventIDs len = %d, want 1", len(answer.TimelineEventIDs))
	}
	if answer.TimelineEventIDs[0] != "ev_run_a" {
		t.Fatalf("TimelineEventIDs[0] = %q, want ev_run_a", answer.TimelineEventIDs[0])
	}
}

func TestAnswer_HopExpansionWalksRelationshipGraph(t *testing.T) {
	now := time.Date(2026, 4, 18, 9, 0, 0, 0, time.UTC)

	// Three claims chained through relationships:
	//   cl_seed --(supports)--> cl_one --(contradicts)--> cl_two
	// A query that finds only cl_seed via the events should, with hops=2,
	// expand to include cl_one (1 hop) and cl_two (2 hops).
	events := fakeEventRepo{events: []domain.Event{
		{ID: "ev_seed", RunID: "r", Content: "Seed event about cache eviction policy", Timestamp: now},
	}}
	allClaims := []domain.Claim{
		{ID: "cl_seed", Text: "Cache eviction is LRU", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive, Confidence: 0.9, CreatedAt: now},
		{ID: "cl_one", Text: "LRU outperforms FIFO under our workload", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive, Confidence: 0.85, CreatedAt: now},
		{ID: "cl_two", Text: "FIFO is simpler to reason about", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive, Confidence: 0.7, CreatedAt: now},
	}
	repo := fakeClaimRepo{
		claims:   allClaims,
		evidence: []domain.ClaimEvidence{{ClaimID: "cl_seed", EventID: "ev_seed"}},
	}
	rels := fakeRelationshipRepo{rels: map[string][]domain.Relationship{
		"cl_seed": {{ID: "r1", Type: domain.RelationshipTypeSupports, FromClaimID: "cl_seed", ToClaimID: "cl_one", CreatedAt: now}},
		"cl_one": {
			{ID: "r1", Type: domain.RelationshipTypeSupports, FromClaimID: "cl_seed", ToClaimID: "cl_one", CreatedAt: now},
			{ID: "r2", Type: domain.RelationshipTypeContradicts, FromClaimID: "cl_one", ToClaimID: "cl_two", CreatedAt: now.Add(time.Minute)},
		},
		"cl_two": {{ID: "r2", Type: domain.RelationshipTypeContradicts, FromClaimID: "cl_one", ToClaimID: "cl_two", CreatedAt: now.Add(time.Minute)}},
	}}

	// ListByEventIDs returns only the seed claim (the others have no
	// evidence link), so without hops the answer would have one claim.
	repo.claims = []domain.Claim{allClaims[0]}
	// But ListByIDs (used during expansion) needs to find the others too —
	// stash them via a wrapper that knows both sets.
	wrapper := hopFakeClaimRepo{fakeClaimRepo: repo, all: allClaims}

	engine := NewEngine(events, wrapper, rels)

	// Hops = 0 → just the seed.
	noHops, err := engine.Answer("cache eviction policy")
	if err != nil {
		t.Fatalf("Answer(0 hops): %v", err)
	}
	if len(noHops.Claims) != 1 {
		t.Fatalf("0-hop claim count = %d, want 1", len(noHops.Claims))
	}

	// Hops = 2 → seed + 1-hop neighbor + 2-hop neighbor.
	withHops, err := engine.AnswerWithOptions("cache eviction policy", AnswerOptions{Hops: 2})
	if err != nil {
		t.Fatalf("Answer(2 hops): %v", err)
	}
	if len(withHops.Claims) != 3 {
		t.Fatalf("2-hop claim count = %d, want 3 (got: %+v)", len(withHops.Claims), withHops.Claims)
	}
	if withHops.ClaimHopDistance["cl_seed"] != 0 {
		t.Errorf("cl_seed hop distance = %d, want 0", withHops.ClaimHopDistance["cl_seed"])
	}
	if withHops.ClaimHopDistance["cl_one"] != 1 {
		t.Errorf("cl_one hop distance = %d, want 1", withHops.ClaimHopDistance["cl_one"])
	}
	if withHops.ClaimHopDistance["cl_two"] != 2 {
		t.Errorf("cl_two hop distance = %d, want 2", withHops.ClaimHopDistance["cl_two"])
	}
	if !strings.Contains(withHops.AnswerText, "Expanded 2 additional claim(s)") {
		t.Errorf("AnswerText missing expansion summary: %q", withHops.AnswerText)
	}

	// Hops = 1 → seed + only the 1-hop neighbor (cl_two should not appear).
	oneHop, err := engine.AnswerWithOptions("cache eviction policy", AnswerOptions{Hops: 1})
	if err != nil {
		t.Fatalf("Answer(1 hop): %v", err)
	}
	if len(oneHop.Claims) != 2 {
		t.Fatalf("1-hop claim count = %d, want 2", len(oneHop.Claims))
	}
	if _, expanded := oneHop.ClaimHopDistance["cl_two"]; expanded {
		t.Errorf("cl_two should not appear at hops=1")
	}
}

// hopFakeClaimRepo extends fakeClaimRepo so ListByIDs (used for
// hop-expansion) can find claims that aren't in the seed set. The
// embedded fakeClaimRepo promotes every other ports.ClaimRepository
// method, so adding new methods to that interface only requires
// touching the override list here.
type hopFakeClaimRepo struct {
	fakeClaimRepo
	all []domain.Claim
}

func (r hopFakeClaimRepo) ListByIDs(_ context.Context, ids []string) ([]domain.Claim, error) {
	wanted := map[string]struct{}{}
	for _, id := range ids {
		wanted[id] = struct{}{}
	}
	out := make([]domain.Claim, 0, len(ids))
	for _, c := range r.all {
		if _, ok := wanted[c.ID]; ok {
			out = append(out, c)
		}
	}
	return out, nil
}

func TestAnswer_NarrativeSurfacesStatusTransitions(t *testing.T) {
	now := time.Date(2026, 4, 12, 10, 0, 0, 0, time.UTC)

	events := fakeEventRepo{events: []domain.Event{
		{ID: "ev1", RunID: "r", Content: "Cache eviction policy.", Timestamp: now},
	}}
	claim := domain.Claim{
		ID: "cl_evo", Text: "Cache eviction is LRU",
		Type: domain.ClaimTypeDecision, Status: domain.ClaimStatusResolved,
		Confidence: 0.9, CreatedAt: now,
	}
	repo := narrativeFakeClaimRepo{
		fakeClaimRepo: fakeClaimRepo{
			claims:   []domain.Claim{claim},
			evidence: []domain.ClaimEvidence{{ClaimID: "cl_evo", EventID: "ev1"}},
		},
		history: map[string][]domain.ClaimStatusTransition{
			"cl_evo": {
				{ClaimID: "cl_evo", FromStatus: "", ToStatus: domain.ClaimStatusActive, ChangedAt: now, Reason: ""},
				{ClaimID: "cl_evo", FromStatus: domain.ClaimStatusActive, ToStatus: domain.ClaimStatusContested, ChangedAt: now.Add(72 * time.Hour), Reason: "auto: conflict with cl_fifo"},
				{ClaimID: "cl_evo", FromStatus: domain.ClaimStatusContested, ToStatus: domain.ClaimStatusResolved, ChangedAt: now.Add(144 * time.Hour), Reason: "evidence review"},
			},
		},
	}

	engine := NewEngine(events, repo, fakeRelationshipRepo{rels: map[string][]domain.Relationship{}})
	answer, err := engine.Answer("cache eviction policy")
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if !strings.Contains(answer.AnswerText, "Evolution:") {
		t.Fatalf("missing Evolution section: %q", answer.AnswerText)
	}
	if !strings.Contains(answer.AnswerText, "First recorded as active") {
		t.Errorf("missing initial state in narrative")
	}
	if !strings.Contains(answer.AnswerText, "became contested") {
		t.Errorf("missing contested transition")
	}
	if !strings.Contains(answer.AnswerText, "became resolved") {
		t.Errorf("missing resolved transition")
	}
	if !strings.Contains(answer.AnswerText, "evidence review") {
		t.Errorf("missing reason text")
	}
}

type narrativeFakeClaimRepo struct {
	fakeClaimRepo
	history map[string][]domain.ClaimStatusTransition
}

func (r narrativeFakeClaimRepo) ListStatusHistoryByClaimID(_ context.Context, id string) ([]domain.ClaimStatusTransition, error) {
	return r.history[id], nil
}

func TestAnswer_AttributesProvenanceFromPulledEvent(t *testing.T) {
	now := time.Date(2026, 4, 18, 9, 0, 0, 0, time.UTC)

	events := fakeEventRepo{events: []domain.Event{
		{ID: "ev_local", RunID: "r", Content: "Local fact about cache eviction policy", Timestamp: now,
			Metadata: map[string]string{}},
		{ID: "ev_remote", RunID: "r", Content: "Remote claim about cache eviction policy", Timestamp: now.Add(time.Minute),
			Metadata: map[string]string{"pulled_from_registry": "https://reg.example.com"}},
	}}
	claims := []domain.Claim{
		{ID: "cl_local", Text: "We use LRU for cache eviction policy", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive, Confidence: 0.8, CreatedAt: now},
		{ID: "cl_remote", Text: "Cache eviction policy is FIFO", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive, Confidence: 0.8, CreatedAt: now.Add(time.Minute)},
	}
	repo := fakeClaimRepo{
		claims: claims,
		evidence: []domain.ClaimEvidence{
			{ClaimID: "cl_local", EventID: "ev_local"},
			{ClaimID: "cl_remote", EventID: "ev_remote"},
		},
	}

	engine := NewEngine(events, repo, fakeRelationshipRepo{rels: map[string][]domain.Relationship{}})
	answer, err := engine.Answer("cache eviction policy")
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if got := answer.ClaimProvenance["cl_local"]; got != "local" {
		t.Errorf("cl_local provenance = %q, want 'local'", got)
	}
	if got := answer.ClaimProvenance["cl_remote"]; got != "https://reg.example.com" {
		t.Errorf("cl_remote provenance = %q, want registry URL", got)
	}
	if !strings.Contains(answer.AnswerText, "from https://reg.example.com") &&
		!strings.Contains(answer.AnswerText, "from a connected registry") {
		t.Errorf("AnswerText does not surface provenance: %q", answer.AnswerText)
	}
}

// --- Dual-mode resolution tests ---

func makeContradictingSetup(now time.Time, highTrust, lowTrust float64) (fakeEventRepo, fakeClaimRepo, fakeRelationshipRepo) {
	events := fakeEventRepo{events: []domain.Event{
		{ID: "ev1", RunID: "r", Content: "deployment strategy", Timestamp: now},
	}}
	claims := []domain.Claim{
		{ID: "cl_high", Text: "We deploy with blue-green", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive, Confidence: highTrust, TrustScore: highTrust, CreatedAt: now},
		{ID: "cl_low", Text: "We deploy with canary", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive, Confidence: lowTrust, TrustScore: lowTrust, CreatedAt: now},
	}
	claimRepo := fakeClaimRepo{claims: claims}
	relRepo := fakeRelationshipRepo{rels: map[string][]domain.Relationship{
		"cl_high": {{ID: "rel1", Type: domain.RelationshipTypeContradicts, FromClaimID: "cl_high", ToClaimID: "cl_low", CreatedAt: now}},
		"cl_low":  {{ID: "rel1", Type: domain.RelationshipTypeContradicts, FromClaimID: "cl_high", ToClaimID: "cl_low", CreatedAt: now}},
	}}
	return events, claimRepo, relRepo
}

func TestDualMode_AgentAutoResolvesHighMarginContradiction(t *testing.T) {
	now := time.Now().UTC()
	events, claimRepo, relRepo := makeContradictingSetup(now, 0.92, 0.71)

	engine := NewEngine(events, claimRepo, relRepo)
	answer, err := engine.AnswerWithOptions("deployment strategy", AnswerOptions{
		Consumer: domain.ConsumerAgent,
	})
	if err != nil {
		t.Fatalf("AnswerWithOptions: %v", err)
	}
	if !answer.AutoResolved {
		t.Error("expected AutoResolved=true for agent consumer with high-margin contradiction")
	}
	// Losing claim (cl_low) should be demoted.
	for _, c := range answer.Claims {
		if c.ID == "cl_low" {
			t.Errorf("demoted claim cl_low still present in answer.Claims")
		}
	}
	if answer.ContradictionExplanation != "" {
		t.Errorf("agent consumer should not produce ContradictionExplanation, got: %q", answer.ContradictionExplanation)
	}
}

func TestDualMode_AgentKeepsBothOnSlimMargin(t *testing.T) {
	now := time.Now().UTC()
	// Confidence margin = 0.10 (< 0.20), TrustScore delta = 0.01 (< trustTiebreak=0.05).
	// Neither threshold is met → both claims retained, AutoResolved=false.
	ev := fakeEventRepo{events: []domain.Event{
		{ID: "ev1", Content: "deployment strategy", Timestamp: now},
	}}
	highClaim := domain.Claim{
		ID: "cl_high", Text: "We deploy with blue-green", Type: domain.ClaimTypeFact,
		Status: domain.ClaimStatusActive, Confidence: 0.82, TrustScore: 0.81, CreatedAt: now,
	}
	lowClaim := domain.Claim{
		ID: "cl_low", Text: "We deploy with canary", Type: domain.ClaimTypeFact,
		Status: domain.ClaimStatusActive, Confidence: 0.72, TrustScore: 0.80, CreatedAt: now,
	}
	claimRepo := fakeClaimRepo{
		claims: []domain.Claim{highClaim, lowClaim},
		evidence: []domain.ClaimEvidence{
			{ClaimID: "cl_high", EventID: "ev1"},
			{ClaimID: "cl_low", EventID: "ev1"},
		},
	}
	relRepo := fakeRelationshipRepo{rels: map[string][]domain.Relationship{
		"cl_high": {{ID: "rel1", Type: domain.RelationshipTypeContradicts, FromClaimID: "cl_high", ToClaimID: "cl_low", CreatedAt: now}},
		"cl_low":  {{ID: "rel1", Type: domain.RelationshipTypeContradicts, FromClaimID: "cl_high", ToClaimID: "cl_low", CreatedAt: now}},
	}}
	engine := NewEngine(ev, claimRepo, relRepo)
	answer, err := engine.AnswerWithOptions("deployment strategy", AnswerOptions{
		Consumer: domain.ConsumerAgent,
	})
	if err != nil {
		t.Fatalf("AnswerWithOptions: %v", err)
	}
	if answer.AutoResolved {
		t.Error("expected AutoResolved=false when both confidence margin and trust delta are too slim")
	}
	ids := map[string]bool{}
	for _, c := range answer.Claims {
		ids[c.ID] = true
	}
	if !ids["cl_high"] || !ids["cl_low"] {
		t.Error("both claims should be retained when margin is too slim to auto-resolve")
	}
}

func TestDualMode_UserGetExplanation(t *testing.T) {
	now := time.Now().UTC()
	events, claimRepo, relRepo := makeContradictingSetup(now, 0.92, 0.71)

	engine := NewEngine(events, claimRepo, relRepo)
	answer, err := engine.AnswerWithOptions("deployment strategy", AnswerOptions{
		Consumer: domain.ConsumerUser,
	})
	if err != nil {
		t.Fatalf("AnswerWithOptions: %v", err)
	}
	if answer.AutoResolved {
		t.Error("user consumer should never auto-resolve")
	}
	if answer.ContradictionExplanation == "" {
		t.Error("expected non-empty ContradictionExplanation for user consumer with contradictions")
	}
	if !strings.Contains(answer.ContradictionExplanation, "contradiction") {
		t.Errorf("explanation does not mention 'contradiction': %q", answer.ContradictionExplanation)
	}
	// Both claims should remain in the answer.
	ids := map[string]bool{}
	for _, c := range answer.Claims {
		ids[c.ID] = true
	}
	if !ids["cl_high"] || !ids["cl_low"] {
		t.Error("user consumer should keep both contradicting claims")
	}
}

func TestDualMode_DefaultConsumerBehavesLikeUser(t *testing.T) {
	now := time.Now().UTC()
	events, claimRepo, relRepo := makeContradictingSetup(now, 0.92, 0.71)

	engine := NewEngine(events, claimRepo, relRepo)
	// Zero-value AnswerOptions — Consumer is empty string, treated as ConsumerUser.
	answer, err := engine.AnswerWithOptions("deployment strategy", AnswerOptions{})
	if err != nil {
		t.Fatalf("AnswerWithOptions: %v", err)
	}
	if answer.AutoResolved {
		t.Error("zero-value Consumer should not auto-resolve")
	}
}

func TestVerdict_AgentHighMarginProducesTrustVerdict(t *testing.T) {
	now := time.Now().UTC()
	// margin = 0.92 - 0.71 = 0.21 — above escalation threshold
	events, claimRepo, relRepo := makeContradictingSetup(now, 0.92, 0.71)

	engine := NewEngine(events, claimRepo, relRepo)
	answer, err := engine.AnswerWithOptions("deployment strategy", AnswerOptions{
		Consumer: domain.ConsumerAgent,
	})
	if err != nil {
		t.Fatalf("AnswerWithOptions: %v", err)
	}
	if len(answer.Verdicts) == 0 {
		t.Fatal("expected at least one Verdict for agent consumer with contradiction")
	}
	v := answer.Verdicts[0]
	if v.Action != domain.VerdictActionTrust && v.Action != domain.VerdictActionUpdate {
		t.Errorf("expected trust or update action, got %q", v.Action)
	}
	if v.WinnerClaimID != "cl_high" {
		t.Errorf("expected winner=cl_high, got %q", v.WinnerClaimID)
	}
	if v.LoserClaimID != "cl_low" {
		t.Errorf("expected loser=cl_low, got %q", v.LoserClaimID)
	}
	if v.Confidence == 0 {
		t.Error("expected non-zero Confidence in verdict")
	}
	if v.Rationale == "" {
		t.Error("expected non-empty Rationale in verdict")
	}
	if v.EscalationReason != "" {
		t.Errorf("expected empty EscalationReason for resolved verdict, got %q", v.EscalationReason)
	}
}

func TestVerdict_AgentSlimMarginProducesEscalateVerdict(t *testing.T) {
	now := time.Now().UTC()
	// Confidence margin = 0.10 (< 0.20), TrustScore delta = 0.01 (< trustTiebreak=0.05).
	// Both margins too slim → Escalate verdict emitted.
	ev := fakeEventRepo{events: []domain.Event{
		{ID: "ev1", Content: "deployment strategy", Timestamp: now},
	}}
	highClaim := domain.Claim{
		ID: "cl_high", Text: "We deploy with blue-green", Type: domain.ClaimTypeFact,
		Status: domain.ClaimStatusActive, Confidence: 0.82, TrustScore: 0.81, CreatedAt: now,
	}
	lowClaim := domain.Claim{
		ID: "cl_low", Text: "We deploy with canary", Type: domain.ClaimTypeFact,
		Status: domain.ClaimStatusActive, Confidence: 0.72, TrustScore: 0.80, CreatedAt: now,
	}
	claimRepo := fakeClaimRepo{
		claims: []domain.Claim{highClaim, lowClaim},
		evidence: []domain.ClaimEvidence{
			{ClaimID: "cl_high", EventID: "ev1"},
			{ClaimID: "cl_low", EventID: "ev1"},
		},
	}
	relRepo := fakeRelationshipRepo{rels: map[string][]domain.Relationship{
		"cl_high": {{ID: "rel1", Type: domain.RelationshipTypeContradicts, FromClaimID: "cl_high", ToClaimID: "cl_low", CreatedAt: now}},
		"cl_low":  {{ID: "rel1", Type: domain.RelationshipTypeContradicts, FromClaimID: "cl_high", ToClaimID: "cl_low", CreatedAt: now}},
	}}
	engine := NewEngine(ev, claimRepo, relRepo)
	answer, err := engine.AnswerWithOptions("deployment strategy", AnswerOptions{
		Consumer: domain.ConsumerAgent,
	})
	if err != nil {
		t.Fatalf("AnswerWithOptions: %v", err)
	}
	if len(answer.Verdicts) == 0 {
		t.Fatal("expected at least one Verdict even when escalating")
	}
	v := answer.Verdicts[0]
	if v.Action != domain.VerdictActionEscalate {
		t.Errorf("expected escalate action for slim-margin contradiction, got %q", v.Action)
	}
	if v.EscalationReason == "" {
		t.Error("expected non-empty EscalationReason for escalation verdict")
	}
	if v.WinnerClaimID != "" || v.LoserClaimID != "" {
		t.Errorf("escalation verdict should have empty winner/loser IDs, got winner=%q loser=%q",
			v.WinnerClaimID, v.LoserClaimID)
	}
}

func TestVerdict_UserConsumerProducesNoVerdicts(t *testing.T) {
	now := time.Now().UTC()
	events, claimRepo, relRepo := makeContradictingSetup(now, 0.92, 0.71)

	engine := NewEngine(events, claimRepo, relRepo)
	answer, err := engine.AnswerWithOptions("deployment strategy", AnswerOptions{
		Consumer: domain.ConsumerUser,
	})
	if err != nil {
		t.Fatalf("AnswerWithOptions: %v", err)
	}
	if len(answer.Verdicts) != 0 {
		t.Errorf("user consumer should produce no Verdicts, got %d", len(answer.Verdicts))
	}
}

// fakeDecisionRepo is a minimal in-memory DecisionRepository for tests.
type fakeDecisionRepo struct {
	decisions []domain.Decision
}

func (r fakeDecisionRepo) Append(_ context.Context, _ domain.Decision) error { return nil }
func (r fakeDecisionRepo) GetByID(_ context.Context, id string) (domain.Decision, error) {
	for _, d := range r.decisions {
		if d.ID == id {
			return d, nil
		}
	}
	return domain.Decision{}, nil
}
func (r fakeDecisionRepo) ListAll(_ context.Context) ([]domain.Decision, error) {
	return r.decisions, nil
}
func (r fakeDecisionRepo) ListByRiskLevel(_ context.Context, level string) ([]domain.Decision, error) {
	var out []domain.Decision
	for _, d := range r.decisions {
		if string(d.RiskLevel) == level {
			out = append(out, d)
		}
	}
	return out, nil
}
func (r fakeDecisionRepo) AttachOutcome(_ context.Context, _, _ string) error { return nil }
func (r fakeDecisionRepo) CountAll(_ context.Context) (int64, error) {
	return int64(len(r.decisions)), nil
}
func (r fakeDecisionRepo) DeleteAll(_ context.Context) error { return nil }
func (r fakeDecisionRepo) AppendBeliefs(_ context.Context, _ string, _ []string) error {
	return nil
}
func (r fakeDecisionRepo) ListBeliefs(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}

func TestAuditTrail_ErrorWithoutDecisionRepository(t *testing.T) {
	engine := NewEngine(fakeEventRepo{}, fakeClaimRepo{}, fakeRelationshipRepo{rels: map[string][]domain.Relationship{}})
	_, err := engine.AuditTrail(context.Background(), AuditTrailOptions{})
	if err == nil {
		t.Fatal("expected error when no DecisionRepository is wired, got nil")
	}
	if !strings.Contains(err.Error(), "WithDecisions") {
		t.Errorf("error should mention WithDecisions, got: %v", err)
	}
}

func TestAuditTrail_ReturnsOnlyRefutedDecisions(t *testing.T) {
	now := time.Now().UTC()
	decisions := []domain.Decision{
		{
			ID:              "d1",
			Statement:       "deploy on Fridays",
			RiskLevel:       domain.RiskLevelHigh,
			ChosenAt:        now,
			FailedOutcomeID: "oc_bad_deploy",
			RefutedBeliefs:  []string{"cl_1", "cl_2"},
		},
		{
			ID:        "d2",
			Statement: "use feature flags",
			RiskLevel: domain.RiskLevelLow,
			ChosenAt:  now,
			// No FailedOutcomeID → not refuted; should be filtered out by default.
		},
	}
	engine := NewEngine(fakeEventRepo{}, fakeClaimRepo{}, fakeRelationshipRepo{rels: map[string][]domain.Relationship{}}).
		WithDecisions(fakeDecisionRepo{decisions: decisions})

	entries, err := engine.AuditTrail(context.Background(), AuditTrailOptions{})
	if err != nil {
		t.Fatalf("AuditTrail: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 refuted entry, got %d", len(entries))
	}
	if entries[0].Decision.ID != "d1" {
		t.Errorf("expected decision d1, got %s", entries[0].Decision.ID)
	}
	if entries[0].FailedOutcomeID != "oc_bad_deploy" {
		t.Errorf("expected FailedOutcomeID=oc_bad_deploy, got %s", entries[0].FailedOutcomeID)
	}
	if len(entries[0].RefutedBeliefs) != 2 {
		t.Errorf("expected 2 RefutedBeliefs, got %d", len(entries[0].RefutedBeliefs))
	}
}

func TestAuditTrail_IncludeSuccessfulReturnsAll(t *testing.T) {
	now := time.Now().UTC()
	decisions := []domain.Decision{
		{ID: "d1", Statement: "s1", RiskLevel: domain.RiskLevelHigh, ChosenAt: now, FailedOutcomeID: "oc_x"},
		{ID: "d2", Statement: "s2", RiskLevel: domain.RiskLevelLow, ChosenAt: now},
	}
	engine := NewEngine(fakeEventRepo{}, fakeClaimRepo{}, fakeRelationshipRepo{rels: map[string][]domain.Relationship{}}).
		WithDecisions(fakeDecisionRepo{decisions: decisions})

	entries, err := engine.AuditTrail(context.Background(), AuditTrailOptions{IncludeSuccessful: true})
	if err != nil {
		t.Fatalf("AuditTrail: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("expected 2 entries with IncludeSuccessful=true, got %d", len(entries))
	}
}

func TestAuditTrail_FilterByService(t *testing.T) {
	now := time.Now().UTC()
	decisions := []domain.Decision{
		{
			ID: "d1", Statement: "bad payments decision",
			RiskLevel: domain.RiskLevelHigh, ChosenAt: now,
			Scope:           domain.Scope{Service: "payments"},
			FailedOutcomeID: "oc_payments_fail",
		},
		{
			ID: "d2", Statement: "bad auth decision",
			RiskLevel: domain.RiskLevelMedium, ChosenAt: now,
			Scope:           domain.Scope{Service: "auth"},
			FailedOutcomeID: "oc_auth_fail",
		},
	}
	engine := NewEngine(fakeEventRepo{}, fakeClaimRepo{}, fakeRelationshipRepo{rels: map[string][]domain.Relationship{}}).
		WithDecisions(fakeDecisionRepo{decisions: decisions})

	entries, err := engine.AuditTrail(context.Background(), AuditTrailOptions{Service: "payments"})
	if err != nil {
		t.Fatalf("AuditTrail: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry for service=payments, got %d", len(entries))
	}
	if entries[0].Decision.Scope.Service != "payments" {
		t.Errorf("expected service payments, got %s", entries[0].Decision.Scope.Service)
	}
}

func TestAuditTrail_FilterByRiskLevel(t *testing.T) {
	now := time.Now().UTC()
	decisions := []domain.Decision{
		{ID: "d1", Statement: "high risk", RiskLevel: domain.RiskLevelHigh, ChosenAt: now, FailedOutcomeID: "oc_1"},
		{ID: "d2", Statement: "low risk", RiskLevel: domain.RiskLevelLow, ChosenAt: now, FailedOutcomeID: "oc_2"},
	}
	engine := NewEngine(fakeEventRepo{}, fakeClaimRepo{}, fakeRelationshipRepo{rels: map[string][]domain.Relationship{}}).
		WithDecisions(fakeDecisionRepo{decisions: decisions})

	entries, err := engine.AuditTrail(context.Background(), AuditTrailOptions{RiskLevel: "high"})
	if err != nil {
		t.Fatalf("AuditTrail: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 high-risk entry, got %d", len(entries))
	}
	if entries[0].Decision.RiskLevel != domain.RiskLevelHigh {
		t.Errorf("expected RiskLevelHigh, got %s", entries[0].Decision.RiskLevel)
	}
}

// ---------------------------------------------------------------------------
// Workspace visibility isolation tests
// ---------------------------------------------------------------------------

func TestVisibility_PersonalOnlySeesPersonal(t *testing.T) {
	now := time.Now().UTC()
	events := fakeEventRepo{events: []domain.Event{
		{ID: "ev_1", Content: "some event", Timestamp: now},
	}}
	claims := []domain.Claim{
		{ID: "cl_personal", Text: "personal note", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive, Confidence: 0.9, CreatedAt: now, Visibility: domain.VisibilityPersonal},
		{ID: "cl_team", Text: "team claim", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive, Confidence: 0.9, CreatedAt: now, Visibility: domain.VisibilityTeam},
		{ID: "cl_org", Text: "org claim", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive, Confidence: 0.9, CreatedAt: now, Visibility: domain.VisibilityOrg},
	}
	evidence := []domain.ClaimEvidence{
		{ClaimID: "cl_personal", EventID: "ev_1"},
		{ClaimID: "cl_team", EventID: "ev_1"},
		{ClaimID: "cl_org", EventID: "ev_1"},
	}
	repo := fakeClaimRepo{claims: claims, evidence: evidence}
	engine := NewEngine(events, repo, fakeRelationshipRepo{rels: map[string][]domain.Relationship{}})

	answer, err := engine.AnswerWithOptions("personal note", AnswerOptions{Visibility: domain.VisibilityPersonal})
	if err != nil {
		t.Fatalf("AnswerWithOptions: %v", err)
	}
	for _, c := range answer.Claims {
		if c.Visibility != domain.VisibilityPersonal {
			t.Errorf("personal query returned claim with visibility %q (id=%s)", c.Visibility, c.ID)
		}
	}
}

func TestVisibility_TeamSeesPersonalAndTeam(t *testing.T) {
	now := time.Now().UTC()
	events := fakeEventRepo{events: []domain.Event{
		{ID: "ev_1", Content: "some event", Timestamp: now},
	}}
	claims := []domain.Claim{
		{ID: "cl_personal", Text: "personal note", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive, Confidence: 0.9, CreatedAt: now, Visibility: domain.VisibilityPersonal},
		{ID: "cl_team", Text: "team claim", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive, Confidence: 0.9, CreatedAt: now, Visibility: domain.VisibilityTeam},
		{ID: "cl_org", Text: "org only claim", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive, Confidence: 0.9, CreatedAt: now, Visibility: domain.VisibilityOrg},
	}
	evidence := []domain.ClaimEvidence{
		{ClaimID: "cl_personal", EventID: "ev_1"},
		{ClaimID: "cl_team", EventID: "ev_1"},
		{ClaimID: "cl_org", EventID: "ev_1"},
	}
	repo := fakeClaimRepo{claims: claims, evidence: evidence}
	engine := NewEngine(events, repo, fakeRelationshipRepo{rels: map[string][]domain.Relationship{}})

	answer, err := engine.AnswerWithOptions("some claim", AnswerOptions{Visibility: domain.VisibilityTeam})
	if err != nil {
		t.Fatalf("AnswerWithOptions: %v", err)
	}
	for _, c := range answer.Claims {
		if c.Visibility == domain.VisibilityOrg {
			t.Errorf("team query returned org-visibility claim (id=%s)", c.ID)
		}
	}
}

func TestVisibility_OrgSeesAll(t *testing.T) {
	now := time.Now().UTC()
	events := fakeEventRepo{events: []domain.Event{
		{ID: "ev_1", Content: "some event", Timestamp: now},
	}}
	claims := []domain.Claim{
		{ID: "cl_personal", Text: "personal note event", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive, Confidence: 0.9, CreatedAt: now, Visibility: domain.VisibilityPersonal},
		{ID: "cl_team", Text: "team claim event", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive, Confidence: 0.9, CreatedAt: now, Visibility: domain.VisibilityTeam},
		{ID: "cl_org", Text: "org claim event", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive, Confidence: 0.9, CreatedAt: now, Visibility: domain.VisibilityOrg},
	}
	evidence := []domain.ClaimEvidence{
		{ClaimID: "cl_personal", EventID: "ev_1"},
		{ClaimID: "cl_team", EventID: "ev_1"},
		{ClaimID: "cl_org", EventID: "ev_1"},
	}
	repo := fakeClaimRepo{claims: claims, evidence: evidence}
	engine := NewEngine(events, repo, fakeRelationshipRepo{rels: map[string][]domain.Relationship{}})

	answer, err := engine.AnswerWithOptions("event", AnswerOptions{Visibility: domain.VisibilityOrg})
	if err != nil {
		t.Fatalf("AnswerWithOptions: %v", err)
	}
	ids := make(map[string]bool)
	for _, c := range answer.Claims {
		ids[c.ID] = true
	}
	for _, want := range []string{"cl_personal", "cl_team", "cl_org"} {
		if !ids[want] {
			t.Errorf("org query missing claim %s", want)
		}
	}
}

func TestVisibility_ZeroValueDefaultsToTeam(t *testing.T) {
	now := time.Now().UTC()
	events := fakeEventRepo{events: []domain.Event{
		{ID: "ev_1", Content: "some event", Timestamp: now},
	}}
	claims := []domain.Claim{
		{ID: "cl_personal", Text: "personal note event", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive, Confidence: 0.9, CreatedAt: now, Visibility: domain.VisibilityPersonal},
		{ID: "cl_team", Text: "team claim event", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive, Confidence: 0.9, CreatedAt: now, Visibility: domain.VisibilityTeam},
		{ID: "cl_org", Text: "org only claim event", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive, Confidence: 0.9, CreatedAt: now, Visibility: domain.VisibilityOrg},
	}
	evidence := []domain.ClaimEvidence{
		{ClaimID: "cl_personal", EventID: "ev_1"},
		{ClaimID: "cl_team", EventID: "ev_1"},
		{ClaimID: "cl_org", EventID: "ev_1"},
	}
	repo := fakeClaimRepo{claims: claims, evidence: evidence}
	engine := NewEngine(events, repo, fakeRelationshipRepo{rels: map[string][]domain.Relationship{}})

	// zero-value Visibility → treated as team: personal+team visible, org not
	answer, err := engine.AnswerWithOptions("event", AnswerOptions{})
	if err != nil {
		t.Fatalf("AnswerWithOptions: %v", err)
	}
	for _, c := range answer.Claims {
		if c.Visibility == domain.VisibilityOrg {
			t.Errorf("default (team) query returned org-visibility claim (id=%s)", c.ID)
		}
	}
}

// TestWhyTrustClaim_NotFound verifies that WhyTrustClaim returns an error when
// the given claim ID does not exist in the repository.
func TestWhyTrustClaim_NotFound(t *testing.T) {
	eng := NewEngine(
		fakeEventRepo{},
		fakeClaimRepo{claims: nil},
		fakeRelationshipRepo{rels: map[string][]domain.Relationship{}},
	)
	_, err := eng.WhyTrustClaim(context.Background(), "nonexistent-id")
	if err == nil {
		t.Fatal("expected error for unknown claim ID, got nil")
	}
}

// TestWhyTrustClaim_ReturnsSignals verifies that WhyTrustClaim returns a
// ProvenanceReport with a non-empty signal list and a score in [0,1].
func TestWhyTrustClaim_ReturnsSignals(t *testing.T) {
	now := time.Now().UTC()
	c := domain.Claim{
		ID:             "cl_prov",
		Text:           "cache hit ratio dropped",
		Type:           domain.ClaimTypeFact,
		Status:         domain.ClaimStatusActive,
		Confidence:     0.82,
		TrustScore:     0.75,
		CreatedAt:      now.Add(-24 * time.Hour),
		SourceDocument: "runbook.md",
	}
	eng := NewEngine(
		fakeEventRepo{},
		fakeClaimRepo{claims: []domain.Claim{c}},
		fakeRelationshipRepo{rels: map[string][]domain.Relationship{}},
	)
	report, err := eng.WhyTrustClaim(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("WhyTrustClaim: %v", err)
	}
	if report.ClaimID != c.ID {
		t.Errorf("ClaimID: want %q, got %q", c.ID, report.ClaimID)
	}
	if report.Score < 0 || report.Score > 1 {
		t.Errorf("Score out of range: %v", report.Score)
	}
	if len(report.Signals) == 0 {
		t.Error("expected at least one provenance signal, got none")
	}
	if report.Rationale == "" {
		t.Error("expected non-empty rationale")
	}
}

// TestTrustTiebreak_HigherTrustWins verifies that when two claims have a slim
// confidence margin (< escalationMargin), the one with a higher TrustScore
// wins and the other is dropped from the resolved set.
func TestTrustTiebreak_HigherTrustWins(t *testing.T) {
	now := time.Now().UTC()
	// Confidence margin is 0.05 (< escalationMargin=0.2), so TrustScore breaks the tie.
	// TrustScore diff is 0.25 (> trustTiebreak=0.05), so resolution should succeed.
	fromClaim := domain.Claim{
		ID: "cl_from", Text: "service latency is high", Type: domain.ClaimTypeFact,
		Status: domain.ClaimStatusActive, Confidence: 0.75, TrustScore: 0.80, CreatedAt: now,
		Visibility: domain.VisibilityTeam,
	}
	toClaim := domain.Claim{
		ID: "cl_to", Text: "service latency is normal", Type: domain.ClaimTypeFact,
		Status: domain.ClaimStatusActive, Confidence: 0.70, TrustScore: 0.55, CreatedAt: now,
		Visibility: domain.VisibilityTeam,
	}
	contradiction := domain.Relationship{
		FromClaimID: fromClaim.ID,
		ToClaimID:   toClaim.ID,
		Type:        domain.RelationshipTypeContradicts,
	}

	resolved, verdicts, autoResolved := resolveContradictionsForAgent(
		[]domain.Claim{fromClaim, toClaim},
		[]domain.Relationship{contradiction},
		now,
	)

	if !autoResolved {
		t.Fatal("expected auto-resolved=true for trust-tiebreak case")
	}
	if len(verdicts) == 0 {
		t.Fatal("expected at least one verdict")
	}
	// The winner should be fromClaim (higher TrustScore).
	for _, c := range resolved {
		if c.ID == toClaim.ID {
			t.Errorf("loser claim (id=%s, lower TrustScore) should have been removed from resolved set", toClaim.ID)
		}
	}
}

// TestTrustTiebreak_TieEscalates verifies that when both claims have a slim
// confidence margin AND a TrustScore delta below trustTiebreak, the engine
// escalates (autoResolved=false) rather than picking a winner arbitrarily.
func TestTrustTiebreak_TieEscalates(t *testing.T) {
	now := time.Now().UTC()
	// Confidence margin = 0.05 (< 0.2), TrustScore diff = 0.02 (< trustTiebreak=0.05)
	fromClaim := domain.Claim{
		ID: "cl_from2", Text: "queue depth is critical", Type: domain.ClaimTypeFact,
		Status: domain.ClaimStatusActive, Confidence: 0.75, TrustScore: 0.71, CreatedAt: now,
		Visibility: domain.VisibilityTeam,
	}
	toClaim := domain.Claim{
		ID: "cl_to2", Text: "queue depth is normal", Type: domain.ClaimTypeFact,
		Status: domain.ClaimStatusActive, Confidence: 0.70, TrustScore: 0.73, CreatedAt: now,
		Visibility: domain.VisibilityTeam,
	}
	contradiction := domain.Relationship{
		FromClaimID: fromClaim.ID,
		ToClaimID:   toClaim.ID,
		Type:        domain.RelationshipTypeContradicts,
	}

	_, verdicts, autoResolved := resolveContradictionsForAgent(
		[]domain.Claim{fromClaim, toClaim},
		[]domain.Relationship{contradiction},
		now,
	)

	if autoResolved {
		t.Fatal("expected auto-resolved=false when trust scores too close to break tie")
	}
	// Should emit an Escalate verdict.
	hasEscalate := false
	for _, v := range verdicts {
		if v.Action == domain.VerdictActionEscalate {
			hasEscalate = true
			break
		}
	}
	if !hasEscalate {
		t.Errorf("expected VerdictActionEscalate verdict, got: %+v", verdicts)
	}
}

// TestTestConflict_RecencyWins covers the test-aware tiebreak: when two
// test_result claims under the same TestRequirementRef contradict each other
// (one passing, one failing), the more recent run wins regardless of
// confidence parity.
func TestTestConflict_RecencyWins(t *testing.T) {
	now := time.Now().UTC()
	stale := domain.Claim{
		ID: "cl_stale", Text: "test_login passed", Type: domain.ClaimTypeTestResult,
		Status: domain.ClaimStatusActive, Confidence: 0.85, TrustScore: 0.7,
		TestID: "t1", TestRequirementRef: "REQ-LOGIN", TestPassCount: 1, TestFailCount: 0,
		TestLastRunAt: now.Add(-30 * 24 * time.Hour),
		CreatedAt:     now, Visibility: domain.VisibilityTeam,
	}
	fresh := domain.Claim{
		ID: "cl_fresh", Text: "test_login failed", Type: domain.ClaimTypeTestResult,
		Status: domain.ClaimStatusActive, Confidence: 0.85, TrustScore: 0.7,
		TestID: "t2", TestRequirementRef: "REQ-LOGIN", TestPassCount: 0, TestFailCount: 1,
		TestLastRunAt: now.Add(-1 * time.Hour),
		CreatedAt:     now, Visibility: domain.VisibilityTeam,
	}
	contradiction := domain.Relationship{
		FromClaimID: stale.ID, ToClaimID: fresh.ID,
		Type: domain.RelationshipTypeContradicts,
	}

	resolved, verdicts, autoResolved := resolveContradictionsForAgent(
		[]domain.Claim{stale, fresh},
		[]domain.Relationship{contradiction},
		now,
	)

	if !autoResolved {
		t.Fatal("expected auto-resolved=true for fresh-vs-stale test conflict")
	}
	if len(verdicts) == 0 || verdicts[0].WinnerClaimID != fresh.ID {
		t.Fatalf("expected fresh test (cl_fresh) to win, got verdicts: %+v", verdicts)
	}
	if !strings.Contains(verdicts[0].Rationale, "test recency") {
		t.Errorf("expected rationale to cite test recency, got: %q", verdicts[0].Rationale)
	}
	for _, c := range resolved {
		if c.ID == stale.ID {
			t.Errorf("stale claim should be demoted from resolved set")
		}
	}
}

// TestTestConflict_PassRatioWins covers the secondary test-aware tiebreak:
// when recency is tied, the test with the higher pass-ratio wins.
func TestTestConflict_PassRatioWins(t *testing.T) {
	now := time.Now().UTC()
	flaky := domain.Claim{
		ID: "cl_flaky", Text: "test_payment failed once", Type: domain.ClaimTypeTestResult,
		Status: domain.ClaimStatusActive, Confidence: 0.85, TrustScore: 0.7,
		TestID: "t_flaky", TestRequirementRef: "REQ-PAY",
		TestPassCount: 5, TestFailCount: 5, // 0/10 decisiveness
		TestLastRunAt: now.Add(-2 * time.Hour),
		CreatedAt:     now, Visibility: domain.VisibilityTeam,
	}
	stable := domain.Claim{
		ID: "cl_stable", Text: "test_payment passed", Type: domain.ClaimTypeTestResult,
		Status: domain.ClaimStatusActive, Confidence: 0.85, TrustScore: 0.7,
		TestID: "t_stable", TestRequirementRef: "REQ-PAY",
		TestPassCount: 10, TestFailCount: 0, // 10/10 decisiveness
		TestLastRunAt: now.Add(-2 * time.Hour),
		CreatedAt:     now, Visibility: domain.VisibilityTeam,
	}
	contradiction := domain.Relationship{
		FromClaimID: flaky.ID, ToClaimID: stable.ID,
		Type: domain.RelationshipTypeContradicts,
	}

	_, verdicts, autoResolved := resolveContradictionsForAgent(
		[]domain.Claim{flaky, stable},
		[]domain.Relationship{contradiction},
		now,
	)

	if !autoResolved {
		t.Fatal("expected auto-resolved=true for flaky-vs-stable")
	}
	if verdicts[0].WinnerClaimID != stable.ID {
		t.Fatalf("expected stable test to win on pass-ratio, got winner=%s", verdicts[0].WinnerClaimID)
	}
	if !strings.Contains(verdicts[0].Rationale, "pass-ratio") {
		t.Errorf("expected rationale to cite pass-ratio, got: %q", verdicts[0].Rationale)
	}
}

// ---------------------------------------------------------------------------
// fakeIncidentRepo — minimal in-memory IncidentRepository for engine tests.
// ---------------------------------------------------------------------------

type fakeIncidentRepo struct {
	incidents map[string]domain.Incident
}

func newFakeIncidentRepo(incs ...domain.Incident) fakeIncidentRepo {
	m := make(map[string]domain.Incident, len(incs))
	for _, i := range incs {
		m[i.ID] = i
	}
	return fakeIncidentRepo{incidents: m}
}

func (r fakeIncidentRepo) Upsert(_ context.Context, inc domain.Incident) error {
	r.incidents[inc.ID] = inc
	return nil
}
func (r fakeIncidentRepo) GetByID(_ context.Context, id string) (domain.Incident, bool, error) {
	inc, ok := r.incidents[id]
	return inc, ok, nil
}
func (r fakeIncidentRepo) ListAll(_ context.Context) ([]domain.Incident, error) {
	out := make([]domain.Incident, 0, len(r.incidents))
	for _, i := range r.incidents {
		out = append(out, i)
	}
	return out, nil
}
func (r fakeIncidentRepo) ListBySeverity(_ context.Context, sev domain.IncidentSeverity) ([]domain.Incident, error) {
	var out []domain.Incident
	for _, i := range r.incidents {
		if i.Severity == sev {
			out = append(out, i)
		}
	}
	return out, nil
}
func (r fakeIncidentRepo) ListByStatus(_ context.Context, s domain.IncidentStatus) ([]domain.Incident, error) {
	var out []domain.Incident
	for _, i := range r.incidents {
		if i.Status == s {
			out = append(out, i)
		}
	}
	return out, nil
}
func (r fakeIncidentRepo) Resolve(_ context.Context, id string, resolvedAt time.Time) error {
	inc, ok := r.incidents[id]
	if !ok {
		return nil
	}
	inc.Status = domain.IncidentStatusResolved
	inc.ResolvedAt = resolvedAt
	r.incidents[id] = inc
	return nil
}
func (r fakeIncidentRepo) AttachDecision(_ context.Context, incidentID, decisionID string) error {
	inc, ok := r.incidents[incidentID]
	if !ok {
		return nil
	}
	for _, d := range inc.DecisionIDs {
		if d == decisionID {
			return nil
		}
	}
	inc.DecisionIDs = append(inc.DecisionIDs, decisionID)
	r.incidents[incidentID] = inc
	return nil
}
func (r fakeIncidentRepo) AttachOutcome(_ context.Context, incidentID, outcomeID string) error {
	inc, ok := r.incidents[incidentID]
	if !ok {
		return nil
	}
	for _, o := range inc.OutcomeIDs {
		if o == outcomeID {
			return nil
		}
	}
	inc.OutcomeIDs = append(inc.OutcomeIDs, outcomeID)
	r.incidents[incidentID] = inc
	return nil
}
func (r fakeIncidentRepo) SetPlaybook(_ context.Context, incidentID, playbookID string) error {
	inc, ok := r.incidents[incidentID]
	if !ok {
		return nil
	}
	inc.PlaybookID = playbookID
	r.incidents[incidentID] = inc
	return nil
}
func (r fakeIncidentRepo) CountAll(_ context.Context) (int64, error) {
	return int64(len(r.incidents)), nil
}
func (r fakeIncidentRepo) DeleteAll(_ context.Context) error {
	for k := range r.incidents {
		delete(r.incidents, k)
	}
	return nil
}

// ---------------------------------------------------------------------------
// WhyWereWeWrong tests
// ---------------------------------------------------------------------------

func TestWhyWereWeWrong_ErrorWithoutIncidentRepository(t *testing.T) {
	eng := NewEngine(fakeEventRepo{}, fakeClaimRepo{}, fakeRelationshipRepo{rels: map[string][]domain.Relationship{}})
	_, err := eng.WhyWereWeWrong(context.Background(), "inc-1")
	if err == nil {
		t.Fatal("expected error when no IncidentRepository is wired")
	}
	if !strings.Contains(err.Error(), "WithIncidents") {
		t.Errorf("error should mention WithIncidents, got: %v", err)
	}
}

func TestWhyWereWeWrong_ErrorWithoutDecisionRepository(t *testing.T) {
	eng := NewEngine(fakeEventRepo{}, fakeClaimRepo{}, fakeRelationshipRepo{rels: map[string][]domain.Relationship{}}).
		WithIncidents(newFakeIncidentRepo())
	_, err := eng.WhyWereWeWrong(context.Background(), "inc-1")
	if err == nil {
		t.Fatal("expected error when no DecisionRepository is wired")
	}
	if !strings.Contains(err.Error(), "WithDecisions") {
		t.Errorf("error should mention WithDecisions, got: %v", err)
	}
}

func TestWhyWereWeWrong_ErrorWhenIncidentNotFound(t *testing.T) {
	eng := NewEngine(fakeEventRepo{}, fakeClaimRepo{}, fakeRelationshipRepo{rels: map[string][]domain.Relationship{}}).
		WithIncidents(newFakeIncidentRepo()).
		WithDecisions(fakeDecisionRepo{})
	_, err := eng.WhyWereWeWrong(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error for missing incident")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention not found, got: %v", err)
	}
}

func TestWhyWereWeWrong_BasicIncidentNoRootClaim(t *testing.T) {
	now := time.Now().UTC()
	inc := domain.Incident{
		ID:       "inc-basic",
		Title:    "Cache stampede",
		Summary:  "Redis was evicting under load",
		Severity: domain.IncidentSeverityHigh,
		Status:   domain.IncidentStatusOpen,
		OpenedAt: now,
	}
	eng := NewEngine(fakeEventRepo{}, fakeClaimRepo{}, fakeRelationshipRepo{rels: map[string][]domain.Relationship{}}).
		WithIncidents(newFakeIncidentRepo(inc)).
		WithDecisions(fakeDecisionRepo{})

	report, err := eng.WhyWereWeWrong(context.Background(), "inc-basic")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Incident.ID != "inc-basic" {
		t.Errorf("incident id: got %q, want inc-basic", report.Incident.ID)
	}
	if report.RootClaim != nil {
		t.Error("expected RootClaim to be nil when no root cause claim id set")
	}
	if len(report.AffectedDecisions) != 0 {
		t.Errorf("expected 0 affected decisions, got %d", len(report.AffectedDecisions))
	}
	if report.Explanation == "" {
		t.Error("expected non-empty Explanation")
	}
	if !strings.Contains(report.Explanation, "Cache stampede") {
		t.Errorf("explanation should mention incident title, got: %q", report.Explanation)
	}
}

func TestWhyWereWeWrong_HydratesAffectedDecisions(t *testing.T) {
	now := time.Now().UTC()
	inc := domain.Incident{
		ID:          "inc-decisions",
		Title:       "Bad deploy",
		Severity:    domain.IncidentSeverityCritical,
		Status:      domain.IncidentStatusOpen,
		OpenedAt:    now,
		DecisionIDs: []string{"dc-1", "dc-2"},
	}
	decisions := []domain.Decision{
		{ID: "dc-1", Statement: "Scale horizontally", RiskLevel: domain.RiskLevelLow},
		{ID: "dc-2", Statement: "Disable health checks", RiskLevel: domain.RiskLevelHigh},
		{ID: "dc-3", Statement: "Unrelated decision", RiskLevel: domain.RiskLevelLow},
	}
	eng := NewEngine(fakeEventRepo{}, fakeClaimRepo{}, fakeRelationshipRepo{rels: map[string][]domain.Relationship{}}).
		WithIncidents(newFakeIncidentRepo(inc)).
		WithDecisions(fakeDecisionRepo{decisions: decisions})

	report, err := eng.WhyWereWeWrong(context.Background(), "inc-decisions")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(report.AffectedDecisions) != 2 {
		t.Fatalf("expected 2 affected decisions, got %d: %v", len(report.AffectedDecisions), report.AffectedDecisions)
	}
	ids := map[string]bool{}
	for _, d := range report.AffectedDecisions {
		ids[d.ID] = true
	}
	if !ids["dc-1"] || !ids["dc-2"] {
		t.Errorf("expected dc-1 and dc-2, got: %v", ids)
	}
	if ids["dc-3"] {
		t.Error("dc-3 should not appear in affected decisions")
	}
	if !strings.Contains(report.Explanation, "2 decision(s)") {
		t.Errorf("explanation should mention 2 decisions, got: %q", report.Explanation)
	}
}

func TestWhyWereWeWrong_ExplanationMentionsSeverityAndStatus(t *testing.T) {
	now := time.Now().UTC()
	inc := domain.Incident{
		ID:       "inc-explain",
		Title:    "Auth service down",
		Summary:  "JWT validation was rejecting all tokens",
		Severity: domain.IncidentSeverityCritical,
		Status:   domain.IncidentStatusOpen,
		OpenedAt: now,
	}
	eng := NewEngine(fakeEventRepo{}, fakeClaimRepo{}, fakeRelationshipRepo{rels: map[string][]domain.Relationship{}}).
		WithIncidents(newFakeIncidentRepo(inc)).
		WithDecisions(fakeDecisionRepo{})

	report, err := eng.WhyWereWeWrong(context.Background(), "inc-explain")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{"critical", "open", "Auth service down"} {
		if !strings.Contains(report.Explanation, want) {
			t.Errorf("explanation missing %q:\n%s", want, report.Explanation)
		}
	}
}

func TestWhyWereWeWrong_ForwardReferenceClaimNotFoundIsGraceful(t *testing.T) {
	now := time.Now().UTC()
	inc := domain.Incident{
		ID:               "inc-fwd",
		Title:            "Forward ref",
		Severity:         domain.IncidentSeverityMedium,
		Status:           domain.IncidentStatusOpen,
		OpenedAt:         now,
		RootCauseClaimID: "cl-not-ingested-yet",
	}
	// No claims in the repo — WhyTrustClaim will fail, but WhyWereWeWrong should tolerate it.
	eng := NewEngine(fakeEventRepo{}, fakeClaimRepo{}, fakeRelationshipRepo{rels: map[string][]domain.Relationship{}}).
		WithIncidents(newFakeIncidentRepo(inc)).
		WithDecisions(fakeDecisionRepo{})

	report, err := eng.WhyWereWeWrong(context.Background(), "inc-fwd")
	if err != nil {
		t.Fatalf("unexpected error on forward-reference claim: %v", err)
	}
	if report.RootClaim != nil {
		t.Error("expected RootClaim to be nil when claim not found")
	}
	if !strings.Contains(report.Explanation, "cl-not-ingested-yet") {
		t.Errorf("explanation should mention the forward-ref claim id, got: %q", report.Explanation)
	}
}

// Forgetting a claim has to actually stop it being recalled. `forget` and
// `memory_deprecate` document that "future recall paths exclude it from active
// context", and BuildContextBlock honored that — but the main Answer path did
// not filter by status at all, so a deprecated claim kept coming back and kept
// being injected by the recall hook every turn. Found by forgetting a claim in
// a real brain and watching the next query return it.
func TestAnswerExcludesDeprecatedClaims(t *testing.T) {
	now := time.Now().UTC()
	events := fakeEventRepo{events: []domain.Event{
		{ID: "ev_1", RunID: "run_1", Content: "Cache backend notes", Timestamp: now},
	}}
	claims := []domain.Claim{
		{ID: "cl_keep", Text: "The cache backend is Redis", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive, Confidence: 0.9, CreatedAt: now},
		{ID: "cl_gone", Text: "The cache backend is Memcached", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusDeprecated, Confidence: 0.9, CreatedAt: now},
	}
	engine := NewEngine(events, fakeClaimRepo{claims: claims},
		fakeRelationshipRepo{rels: map[string][]domain.Relationship{}})

	answer, err := engine.Answer("what is the cache backend")
	if err != nil {
		t.Fatalf("Answer() error = %v", err)
	}
	for _, c := range answer.Claims {
		if c.ID == "cl_gone" {
			t.Error("a deprecated claim was recalled; forgetting it changed the record but not what gets retrieved")
		}
		if c.Status == domain.ClaimStatusDeprecated {
			t.Errorf("deprecated claim %q in the answer", c.Text)
		}
	}
	// The surviving claim must still come back — this filters the forgotten
	// one, it does not empty the result.
	var kept bool
	for _, c := range answer.Claims {
		if c.ID == "cl_keep" {
			kept = true
		}
	}
	if !kept {
		t.Error("over-filtered: the active claim was dropped too")
	}
}

// Contested and resolved claims must survive. Contested is how a disagreement
// is represented, and hiding it would silently pick a side; resolved is the
// winner of one. Only deprecated means "do not recall this".
func TestAnswerKeepsContestedAndResolved(t *testing.T) {
	now := time.Now().UTC()
	events := fakeEventRepo{events: []domain.Event{
		{ID: "ev_1", RunID: "run_1", Content: "We deploy on Fridays. We deploy every weekday.", Timestamp: now},
	}}
	claims := []domain.Claim{
		{ID: "cl_c", Text: "We deploy on Fridays", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusContested, Confidence: 0.8, CreatedAt: now},
		{ID: "cl_r", Text: "We deploy every weekday", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusResolved, Confidence: 0.8, CreatedAt: now},
	}
	engine := NewEngine(events, fakeClaimRepo{claims: claims},
		fakeRelationshipRepo{rels: map[string][]domain.Relationship{}})

	answer, err := engine.Answer("when do we deploy")
	if err != nil {
		t.Fatalf("Answer() error = %v", err)
	}
	if len(answer.Claims) != 2 {
		t.Errorf("got %d claims, want 2 (contested and resolved must both survive)", len(answer.Claims))
	}
}
