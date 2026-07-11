package domain

import (
	"testing"
	"time"
)

func validGlobalSchema() GlobalSchema {
	return GlobalSchema{
		ID:              "gsch_1",
		Statement:       "rollback restores availability",
		DistinctTenants: 3,
		Confidence:      0.7,
		Status:          GlobalSchemaStatusActive,
		PromotedAt:      time.Now().UTC(),
	}
}

func TestGlobalSchema_Validate(t *testing.T) {
	// A promoted schema is valid with NO evidence ids — the corroboration count
	// is its provenance (this is the whole point of the distinct type).
	if err := validGlobalSchema().Validate(); err != nil {
		t.Fatalf("valid schema (no evidence ids) rejected: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*GlobalSchema)
	}{
		{"missing id", func(g *GlobalSchema) { g.ID = "" }},
		{"missing statement", func(g *GlobalSchema) { g.Statement = "" }},
		{"confidence too high", func(g *GlobalSchema) { g.Confidence = 1.5 }},
		{"confidence negative", func(g *GlobalSchema) { g.Confidence = -0.1 }},
		{"zero corroboration", func(g *GlobalSchema) { g.DistinctTenants = 0 }},
		{"invalid status", func(g *GlobalSchema) { g.Status = "draft" }},
		{"zero promoted_at", func(g *GlobalSchema) { g.PromotedAt = time.Time{} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := validGlobalSchema()
			tc.mutate(&g)
			if err := g.Validate(); err == nil {
				t.Fatalf("expected validation error for %s", tc.name)
			}
		})
	}
}
