package pipeline

import (
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
)

func TestTrustAffectedClaimIDs(t *testing.T) {
	tests := []struct {
		name   string
		claims []domain.Claim
		links  []domain.ClaimEvidence
		want   []string
	}{
		{
			name:   "claims written",
			claims: []domain.Claim{{ID: "c1"}, {ID: "c2"}},
			want:   []string{"c1", "c2"},
		},
		{
			// The one that is easy to miss: a batch routinely links new events
			// to claims that already exist and are not in `claims`. Their
			// evidence count and recency moved, so their trust is now stale.
			name:   "pre-existing claims the batch corroborated",
			claims: []domain.Claim{{ID: "c1"}},
			links:  []domain.ClaimEvidence{{ClaimID: "old1", EventID: "e1"}, {ClaimID: "old2", EventID: "e2"}},
			want:   []string{"c1", "old1", "old2"},
		},
		{
			name:   "deduped across both sources",
			claims: []domain.Claim{{ID: "c1"}, {ID: "c1"}},
			links:  []domain.ClaimEvidence{{ClaimID: "c1", EventID: "e1"}},
			want:   []string{"c1"},
		},
		{
			name:   "blank ids dropped",
			claims: []domain.Claim{{ID: ""}, {ID: "c1"}},
			links:  []domain.ClaimEvidence{{ClaimID: "", EventID: "e1"}},
			want:   []string{"c1"},
		},
		{
			name: "nothing written",
			want: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := trustAffectedClaimIDs(tt.claims, tt.links)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			seen := map[string]bool{}
			for _, id := range got {
				if seen[id] {
					t.Errorf("duplicate id %q — the scoped query would rescore it twice", id)
				}
				seen[id] = true
			}
			for _, w := range tt.want {
				if !seen[w] {
					t.Errorf("missing %q from %v", w, got)
				}
			}
		})
	}
}
