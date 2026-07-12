package sqlite

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

func TestClaimRepositoryStatusHistory_RecordsInitialInsertThenTransitions(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)

	ctx := context.Background()
	repo := NewClaimRepository(db)

	now := time.Date(2026, 4, 18, 9, 0, 0, 0, time.UTC)
	claim := domain.Claim{
		ID:         "cl_hist",
		Text:       "Lifecycle test",
		Type:       domain.ClaimTypeFact,
		Confidence: 0.8,
		Status:     domain.ClaimStatusActive,
		CreatedAt:  now,
	}

	// Initial insert → history row with from_status = "".
	if err := repo.Upsert(ctx, []domain.Claim{claim}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	hist, err := repo.ListStatusHistoryByClaimID(ctx, "cl_hist")
	if err != nil {
		t.Fatalf("list hist: %v", err)
	}
	if len(hist) != 1 || hist[0].FromStatus != "" || hist[0].ToStatus != domain.ClaimStatusActive {
		t.Fatalf("after insert: got %+v, want single ('' -> active) row", hist)
	}

	// Same-status upsert → no new row.
	if err := repo.Upsert(ctx, []domain.Claim{claim}); err != nil {
		t.Fatalf("re-upsert same status: %v", err)
	}
	hist, _ = repo.ListStatusHistoryByClaimID(ctx, "cl_hist")
	if len(hist) != 1 {
		t.Fatalf("after no-op upsert: %d rows, want 1", len(hist))
	}

	// Status change → new row with from_status = active.
	claim.Status = domain.ClaimStatusContested
	if err := repo.UpsertWithReason(ctx, []domain.Claim{claim}, "contradicts cl_other"); err != nil {
		t.Fatalf("contested upsert: %v", err)
	}
	hist, _ = repo.ListStatusHistoryByClaimID(ctx, "cl_hist")
	if len(hist) != 2 {
		t.Fatalf("after contest: %d rows, want 2", len(hist))
	}
	if hist[1].FromStatus != domain.ClaimStatusActive || hist[1].ToStatus != domain.ClaimStatusContested {
		t.Fatalf("bad second transition: %+v", hist[1])
	}
	if hist[1].Reason != "contradicts cl_other" {
		t.Fatalf("reason not captured: %q", hist[1].Reason)
	}

	// Resolved → one more row.
	claim.Status = domain.ClaimStatusResolved
	if err := repo.UpsertWithReason(ctx, []domain.Claim{claim}, "operator chose winner"); err != nil {
		t.Fatalf("resolve upsert: %v", err)
	}
	hist, _ = repo.ListStatusHistoryByClaimID(ctx, "cl_hist")
	if len(hist) != 3 {
		t.Fatalf("after resolve: %d rows, want 3", len(hist))
	}
	if hist[2].FromStatus != domain.ClaimStatusContested || hist[2].ToStatus != domain.ClaimStatusResolved {
		t.Fatalf("bad third transition: %+v", hist[2])
	}
}

func TestClaimRepositoryStatusHistory_EmptyForUnknownClaim(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)

	hist, err := NewClaimRepository(db).ListStatusHistoryByClaimID(context.Background(), "cl_nope")
	if err != nil {
		t.Fatalf("list hist: %v", err)
	}
	if len(hist) != 0 {
		t.Fatalf("got %d rows for unknown claim, want 0", len(hist))
	}
}

func TestClaimRepositoryUpsertAndListByEventIDs(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)

	eventRepo := NewEventRepository(db)
	claimRepo := NewClaimRepository(db)

	now := time.Date(2026, 4, 12, 14, 0, 0, 0, time.UTC)
	event := domain.Event{
		ID:            "ev_for_claim",
		SchemaVersion: "v1",
		Content:       "Team decided to postpone launch",
		SourceInputID: "in_1",
		Timestamp:     now,
		IngestedAt:    now,
		Metadata:      map[string]string{"chunk_kind": "text"},
	}
	if err := eventRepo.Append(context.Background(), event); err != nil {
		t.Fatalf("Append() event error = %v", err)
	}

	claim := domain.Claim{
		ID:         "cl_1",
		Text:       "Team decided to postpone launch",
		Type:       domain.ClaimTypeDecision,
		Confidence: 0.9,
		Status:     domain.ClaimStatusActive,
		CreatedAt:  now,
	}
	if err := claimRepo.Upsert(context.Background(), []domain.Claim{claim}); err != nil {
		t.Fatalf("Upsert() claim error = %v", err)
	}

	if err := claimRepo.UpsertEvidence(context.Background(), []domain.ClaimEvidence{{ClaimID: "cl_1", EventID: "ev_for_claim"}}); err != nil {
		t.Fatalf("UpsertEvidence() error = %v", err)
	}

	claims, err := claimRepo.ListByEventIDs(context.Background(), []string{"ev_for_claim"})
	if err != nil {
		t.Fatalf("ListByEventIDs() error = %v", err)
	}
	if len(claims) != 1 {
		t.Fatalf("ListByEventIDs() len = %d, want 1", len(claims))
	}
	if claims[0].ID != "cl_1" {
		t.Fatalf("ListByEventIDs() claim id = %q, want cl_1", claims[0].ID)
	}
}

// TestClaimVisibility_DefaultsToTeam verifies that a claim stored without an
// explicit Visibility value is retrieved with Visibility == VisibilityTeam.
func TestClaimVisibility_DefaultsToTeam(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)

	ctx := context.Background()
	repo := NewClaimRepository(db)

	claim := domain.Claim{
		ID:         "cl_vis_default",
		Text:       "default visibility claim",
		Type:       domain.ClaimTypeFact,
		Confidence: 0.8,
		Status:     domain.ClaimStatusActive,
		CreatedAt:  time.Now().UTC(),
		// Visibility intentionally omitted — should default to "team".
	}
	if err := repo.Upsert(ctx, []domain.Claim{claim}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := repo.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(ListAll) = %d, want 1", len(got))
	}
	if got[0].Visibility != domain.VisibilityTeam {
		t.Errorf("Visibility = %q, want %q", got[0].Visibility, domain.VisibilityTeam)
	}
}

// TestClaimVisibility_RoundTrip verifies that each explicit Visibility value
// survives a persist-and-retrieve cycle unchanged.
// TestClaimSubjectClass_RoundTrip pins the ADR 0012 persistence contract:
// a claim saved with a SubjectClass loads back with the same value on every
// read path (ListAll via sqlc, ListByIDs + ListByEventIDs via the raw scanner).
// Without the subject_class column the promotion path would always read back
// SubjectClassUnknown and be inert.
func TestClaimSubjectClass_RoundTrip(t *testing.T) {
	cases := []domain.SubjectClass{
		domain.SubjectClassClass,
		domain.SubjectClassIndividual,
		domain.SubjectClassUnknown,
	}

	for _, sc := range cases {
		name := string(sc)
		if name == "" {
			name = "unknown"
		}
		t.Run(name, func(t *testing.T) {
			db := openTestDB(t)
			defer closeDB(db)

			ctx := context.Background()
			repo := NewClaimRepository(db)

			ev := domain.Event{
				ID:            "ev_sc_" + name,
				RunID:         "run",
				SchemaVersion: "1",
				Content:       "subject-class round-trip",
				SourceInputID: "in",
				Timestamp:     time.Now().UTC(),
				IngestedAt:    time.Now().UTC(),
			}
			if err := NewEventRepository(db).Append(ctx, ev); err != nil {
				t.Fatalf("seed event: %v", err)
			}

			claim := domain.Claim{
				ID:           "cl_sc_" + name,
				Text:         "subject-class round-trip: " + name,
				Type:         domain.ClaimTypeFact,
				Confidence:   0.75,
				Status:       domain.ClaimStatusActive,
				CreatedAt:    time.Now().UTC(),
				SubjectClass: sc,
			}
			if err := repo.Upsert(ctx, []domain.Claim{claim}); err != nil {
				t.Fatalf("Upsert: %v", err)
			}
			if err := repo.UpsertEvidence(ctx, []domain.ClaimEvidence{{ClaimID: claim.ID, EventID: ev.ID}}); err != nil {
				t.Fatalf("UpsertEvidence: %v", err)
			}

			// ListAll — sqlc path.
			all, err := repo.ListAll(ctx)
			if err != nil {
				t.Fatalf("ListAll: %v", err)
			}
			if len(all) != 1 || all[0].SubjectClass != sc {
				t.Fatalf("ListAll SubjectClass = %+v, want %q", all, sc)
			}

			// ListByIDs — raw scanClaim path.
			byIDs, err := repo.ListByIDs(ctx, []string{claim.ID})
			if err != nil {
				t.Fatalf("ListByIDs: %v", err)
			}
			if len(byIDs) != 1 || byIDs[0].SubjectClass != sc {
				t.Fatalf("ListByIDs SubjectClass = %+v, want %q", byIDs, sc)
			}

			// ListByEventIDs — raw scanClaim path (JOIN).
			byEvents, err := repo.ListByEventIDs(ctx, []string{ev.ID})
			if err != nil {
				t.Fatalf("ListByEventIDs: %v", err)
			}
			if len(byEvents) != 1 || byEvents[0].SubjectClass != sc {
				t.Fatalf("ListByEventIDs SubjectClass = %+v, want %q", byEvents, sc)
			}
		})
	}
}

// TestClaimSubjectClass_UpsertRefresh proves a re-upsert overwrites the
// stored subject_class (ON CONFLICT ... SET subject_class = excluded...).
func TestClaimSubjectClass_UpsertRefresh(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)
	ctx := context.Background()
	repo := NewClaimRepository(db)

	claim := domain.Claim{
		ID:           "cl_sc_refresh",
		Text:         "starts individual",
		Type:         domain.ClaimTypeFact,
		Confidence:   0.6,
		Status:       domain.ClaimStatusActive,
		CreatedAt:    time.Now().UTC(),
		SubjectClass: domain.SubjectClassIndividual,
	}
	if err := repo.Upsert(ctx, []domain.Claim{claim}); err != nil {
		t.Fatalf("Upsert individual: %v", err)
	}
	claim.SubjectClass = domain.SubjectClassClass
	if err := repo.Upsert(ctx, []domain.Claim{claim}); err != nil {
		t.Fatalf("Upsert class: %v", err)
	}
	got, err := repo.ListByIDs(ctx, []string{claim.ID})
	if err != nil {
		t.Fatalf("ListByIDs: %v", err)
	}
	if len(got) != 1 || got[0].SubjectClass != domain.SubjectClassClass {
		t.Fatalf("re-upsert did not refresh SubjectClass: %+v", got)
	}
}

func TestClaimVisibility_RoundTrip(t *testing.T) {
	cases := []domain.Visibility{
		domain.VisibilityPersonal,
		domain.VisibilityTeam,
		domain.VisibilityOrg,
	}

	for _, vis := range cases {
		t.Run(string(vis), func(t *testing.T) {
			db := openTestDB(t)
			defer closeDB(db)

			ctx := context.Background()
			repo := NewClaimRepository(db)

			claim := domain.Claim{
				ID:         "cl_vis_" + string(vis),
				Text:       "visibility round-trip: " + string(vis),
				Type:       domain.ClaimTypeFact,
				Confidence: 0.75,
				Status:     domain.ClaimStatusActive,
				CreatedAt:  time.Now().UTC(),
				Visibility: vis,
			}
			if err := repo.Upsert(ctx, []domain.Claim{claim}); err != nil {
				t.Fatalf("Upsert: %v", err)
			}

			got, err := repo.ListAll(ctx)
			if err != nil {
				t.Fatalf("ListAll: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("len(ListAll) = %d, want 1", len(got))
			}
			if got[0].Visibility != vis {
				t.Errorf("Visibility = %q, want %q", got[0].Visibility, vis)
			}
		})
	}
}

// TestClaimRepository_ApplyBeliefCredit exercises the ADR-0014 credit persistence
// seam: the confidence_components audit map and trust_score are written together
// and round-trip through the real SQLite UPDATE + scan path.
func TestClaimRepository_ApplyBeliefCredit(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)

	ctx := context.Background()
	repo := NewClaimRepository(db)

	// The repository must satisfy the optional capability.
	var _ interface {
		ApplyBeliefCredit(context.Context, string, map[string]float64, float64) error
	} = repo

	claim := domain.Claim{
		ID: "cl_credit", Text: "belief backing a decision", Type: domain.ClaimTypeHypothesis,
		Confidence: 0.8, Status: domain.ClaimStatusActive, CreatedAt: time.Now().UTC(),
	}
	if err := repo.Upsert(ctx, []domain.Claim{claim}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	comps := map[string]float64{"credit:dec_1:cl_credit": -0.1, "other": 0.42}
	if err := repo.ApplyBeliefCredit(ctx, "cl_credit", comps, 0.7); err != nil {
		t.Fatalf("ApplyBeliefCredit: %v", err)
	}

	got, err := repo.ListByIDs(ctx, []string{"cl_credit"})
	if err != nil || len(got) != 1 {
		t.Fatalf("ListByIDs: err=%v n=%d", err, len(got))
	}
	if got[0].TrustScore != 0.7 {
		t.Errorf("TrustScore = %v, want 0.7", got[0].TrustScore)
	}
	if got[0].ConfidenceComponents["credit:dec_1:cl_credit"] != -0.1 {
		t.Errorf("credit component not persisted: %v", got[0].ConfidenceComponents)
	}
	if got[0].ConfidenceComponents["other"] != 0.42 {
		t.Errorf("non-credit component clobbered: %v", got[0].ConfidenceComponents)
	}

	// A missing claim is a sql.ErrNoRows-wrapped error, not a silent no-op.
	if err := repo.ApplyBeliefCredit(ctx, "nope", comps, 0.5); err == nil {
		t.Error("ApplyBeliefCredit on missing claim should error")
	}
}
