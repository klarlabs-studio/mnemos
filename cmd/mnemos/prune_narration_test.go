package main

import (
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
)

// The prune's classification correctness is covered by internal/extract (it
// calls extract.IsJunk directly). This guards the command's flag contract: a
// bare `prune` must never guess at a destructive operation, and an unknown flag
// must be rejected rather than silently ignored.
func TestParsePruneArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    string
		wantErr bool
	}{
		{"bare prune is an error, not a default", nil, "", true},
		{"narration target", []string{"--narration"}, "narration", false},
		{"unknown flag rejected", []string{"--bogus"}, "", true},
		{"unknown flag rejected even with a valid one", []string{"--narration", "--bogus"}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsePruneArgs(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("target = %q, want %q", got, tt.want)
			}
		})
	}
}

// selectNarrationClaims is the prune's only novel logic: filter to ACTIVE and
// classify via the shared extraction filter. Classification precision/recall
// is covered in internal/extract; this pins the selection contract.
func TestSelectNarrationClaims(t *testing.T) {
	claims := []domain.Claim{
		{ID: "real", Text: "We chose Postgres because write volume outgrew SQLite", Status: domain.ClaimStatusActive},
		{ID: "leadin", Text: "Now wiring the handler into core:", Status: domain.ClaimStatusActive},
		{ID: "narration", Text: "Let me check the config", Status: domain.ClaimStatusActive},
		// A junk claim that is already deprecated must NOT be re-selected.
		{ID: "already-dep", Text: "Getting the detail:", Status: domain.ClaimStatusDeprecated},
		// A junk claim that is CONTESTED is now included: it was auto-contested
		// at ingest against other junk, not human-flagged, so it is still noise.
		{ID: "contested-junk", Text: "Verifying the impact:", Status: domain.ClaimStatusContested},
		// A REAL contested claim (genuine dispute) must be left alone.
		{ID: "contested-real", Text: "The write path must never bypass the governed kernel", Status: domain.ClaimStatusContested},
	}
	got := selectNarrationClaims(claims)

	gotIDs := map[string]bool{}
	for _, c := range got {
		gotIDs[c.ID] = true
	}
	if !gotIDs["leadin"] || !gotIDs["narration"] {
		t.Errorf("missed active junk claims: got %v", gotIDs)
	}
	if gotIDs["real"] {
		t.Error("selected a real knowledge claim for deprecation")
	}
	if gotIDs["already-dep"] {
		t.Error("re-selected an already-deprecated claim, churning its history")
	}
	if !gotIDs["contested-junk"] {
		t.Error("did not select a contested JUNK claim (it is noise, not under review)")
	}
	if gotIDs["contested-real"] {
		t.Error("selected a contested REAL claim — genuine disputes must be left alone")
	}
}
