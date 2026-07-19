package main

import "testing"

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
