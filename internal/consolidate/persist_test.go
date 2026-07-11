package consolidate

import (
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

func TestPromotedLesson_ToGlobalSchema_CarriesOnlyDeidentifiedFields(t *testing.T) {
	p := PromotedLesson{
		Statement:       "restarting the worker clears the stuck queue",
		Scope:           domain.Context{Service: "billing", Env: "prod"},
		Polarity:        domain.SchemaPolarityPositive,
		DistinctTenants: 5,
		EvidenceCount:   14,
		Confidence:      0.77,
		Surprise:        3.1,
		HasSurprise:     true,
	}
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)

	gs := p.ToGlobalSchema(domain.GlobalSchemaStatusActive, now, "consolidate")

	if gs.Statement != p.Statement || gs.Scope != p.Scope || gs.Polarity != p.Polarity {
		t.Fatalf("de-identified fields not carried: %+v", gs)
	}
	if gs.DistinctTenants != p.DistinctTenants || gs.EvidenceCount != p.EvidenceCount {
		t.Fatalf("corroboration counts not carried: %+v", gs)
	}
	if gs.Confidence != p.Confidence || gs.Surprise != p.Surprise || !gs.HasSurprise {
		t.Fatalf("ranking signals not carried: %+v", gs)
	}
	if gs.Status != domain.GlobalSchemaStatusActive || !gs.PromotedAt.Equal(now) || gs.CreatedBy != "consolidate" {
		t.Fatalf("write metadata not set: %+v", gs)
	}
	// The produced record must validate — no evidence ids required, unlike Schema.
	if err := gs.Validate(); err != nil {
		t.Fatalf("mapped schema should validate: %v", err)
	}
}

func TestGlobalSchemaID_DeterministicAndContentAddressed(t *testing.T) {
	base := PromotedLesson{
		Statement: "scale up before the nightly batch",
		Scope:     domain.Context{Service: "reports"},
		Polarity:  domain.SchemaPolarityPositive,
	}
	// Same content → same id (so re-runs upsert rather than churn). Compare a
	// fresh copy so this is a genuine recomputation, not a self-comparison.
	again := base
	if GlobalSchemaID(base) != GlobalSchemaID(again) {
		t.Fatal("id not deterministic")
	}
	// Empty polarity normalizes to positive → same id as explicit positive.
	noPol := base
	noPol.Polarity = ""
	if GlobalSchemaID(noPol) != GlobalSchemaID(base) {
		t.Fatal("empty polarity should normalize to positive for id")
	}
	// Different statement → different id.
	diff := base
	diff.Statement = "scale DOWN after the nightly batch"
	if GlobalSchemaID(diff) == GlobalSchemaID(base) {
		t.Fatal("distinct statements must produce distinct ids")
	}
	// Different scope → different id.
	diffScope := base
	diffScope.Scope = domain.Context{Service: "reports", Env: "staging"}
	if GlobalSchemaID(diffScope) == GlobalSchemaID(base) {
		t.Fatal("distinct scopes must produce distinct ids")
	}
}
