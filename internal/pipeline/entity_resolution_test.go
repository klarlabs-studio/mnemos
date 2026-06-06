package pipeline

import (
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
)

func TestMatchByContainment_FindsExistingPersonByPrefix(t *testing.T) {
	pool := []domain.Entity{
		{ID: "e_alice", Name: "Alice", NormalizedName: "alice", Type: domain.EntityTypePerson},
	}
	got, ok := matchByContainment(pool, "Alice Smith")
	if !ok {
		t.Fatalf("expected to fold 'Alice Smith' onto existing 'Alice'")
	}
	if got.ID != "e_alice" {
		t.Errorf("matched %q, want e_alice", got.ID)
	}
}

func TestMatchByContainment_FindsExistingPersonAsContainer(t *testing.T) {
	// Reverse direction: an existing "Alice Smith" should accept a
	// shorter "Alice" surface form.
	pool := []domain.Entity{
		{ID: "e_alice_full", Name: "Alice Smith", NormalizedName: "alice smith", Type: domain.EntityTypePerson},
	}
	got, ok := matchByContainment(pool, "Alice")
	if !ok {
		t.Fatalf("expected 'Alice' to fold onto 'Alice Smith'")
	}
	if got.ID != "e_alice_full" {
		t.Errorf("matched %q, want e_alice_full", got.ID)
	}
}

func TestMatchByContainment_RejectsDistinctNames(t *testing.T) {
	pool := []domain.Entity{
		{ID: "e_alice", Name: "Alice", NormalizedName: "alice"},
		{ID: "e_bob", Name: "Bob", NormalizedName: "bob"},
	}
	if _, ok := matchByContainment(pool, "Carol"); ok {
		t.Errorf("Carol should not match Alice or Bob")
	}
}

func TestMatchByContainment_HandlesMissingNormalizedName(t *testing.T) {
	// Falls back to normalizing Name when the repo didn't populate
	// NormalizedName.
	pool := []domain.Entity{
		{ID: "e_alice", Name: "  Alice  "},
	}
	got, ok := matchByContainment(pool, "alice")
	if !ok {
		t.Fatalf("expected match via on-the-fly normalization")
	}
	if got.ID != "e_alice" {
		t.Errorf("matched %q, want e_alice", got.ID)
	}
}

func TestNormalizeEntityName_LowercaseAndCollapseWhitespace(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"  Alice  Smith ", "alice smith"},
		{"BOB", "bob"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := normalizeEntityName(tc.in); got != tc.want {
			t.Errorf("normalizeEntityName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
