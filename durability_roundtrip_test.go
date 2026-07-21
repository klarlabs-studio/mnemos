package mnemos_test

import (
	"context"
	"path/filepath"
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"

	_ "go.klarlabs.de/mnemos/internal/store/memory"
	_ "go.klarlabs.de/mnemos/internal/store/sqlite"
)

// Durability must survive a write/read cycle on every backend, or marking a
// belief session-local silently does nothing.
func TestDurability_RoundTripsPerBackend(t *testing.T) {
	dsns := map[string]string{
		"sqlite": "sqlite://" + filepath.Join(t.TempDir(), "dur.db"),
		"memory": "memory://?namespace=durability_rt",
	}
	for name, dsn := range dsns {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			conn, err := store.Open(ctx, dsn)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			t.Cleanup(func() { _ = conn.Close() })

			claims := []domain.Claim{
				{ID: "c-session", Text: "CI passed in 1m59s", Type: domain.ClaimTypeFact,
					Confidence: 0.9, Status: domain.ClaimStatusActive, Durability: domain.DurabilitySessionLocal},
				{ID: "c-durable", Text: "the retry backoff is jittered by design", Type: domain.ClaimTypeFact,
					Confidence: 0.9, Status: domain.ClaimStatusActive, Durability: domain.DurabilityDurable},
				// Everything captured before the column existed looks like this.
				{ID: "c-legacy", Text: "an older belief", Type: domain.ClaimTypeFact,
					Confidence: 0.9, Status: domain.ClaimStatusActive},
			}
			if err := conn.Claims.Upsert(ctx, claims); err != nil {
				t.Fatalf("upsert: %v", err)
			}
			got, err := conn.Claims.ListByIDs(ctx, []string{"c-session", "c-durable", "c-legacy"})
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			byID := map[string]domain.Claim{}
			for _, c := range got {
				byID[c.ID] = c
			}
			if d := byID["c-session"].Durability; d != domain.DurabilitySessionLocal {
				t.Errorf("session-local did not round-trip: %q", d)
			}
			if d := byID["c-durable"].Durability; d != domain.DurabilityDurable {
				t.Errorf("durable did not round-trip: %q", d)
			}
			// The back catalogue must read back as unknown, and unknown must
			// never behave as session-local.
			legacy := byID["c-legacy"].Durability
			if legacy != domain.DurabilityUnknown {
				t.Errorf("legacy claim should be unknown, got %q", legacy)
			}
			if legacy.IsSessionLocal() {
				t.Error("an unclassified belief must not count as session-local")
			}
		})
	}
}
