package recorded

import (
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

func TestRewindClaim(t *testing.T) {
	t0 := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC) // Monday
	tue := t0.Add(24 * time.Hour)
	thu := t0.Add(72 * time.Hour)
	base := domain.Claim{
		ID: "c", CreatedAt: t0, Status: domain.ClaimStatusDeprecated,
		ValidTo: thu, ArchivedAt: thu,
	}
	insert := domain.ClaimStatusTransition{ToStatus: domain.ClaimStatusActive, ChangedAt: t0}
	retract := domain.ClaimStatusTransition{FromStatus: domain.ClaimStatusActive, ToStatus: domain.ClaimStatusDeprecated, ChangedAt: thu}
	ended := domain.ValidityChange{Field: domain.ValidityFieldValidTo, To: thu, ChangedAt: thu}
	archived := domain.ValidityChange{Field: domain.ValidityFieldArchivedAt, To: thu, ChangedAt: thu}
	merged := domain.Claim{ID: "c", CreatedAt: t0, Status: domain.ClaimStatusDeprecated, MergedInto: "w"}
	mergedThu := domain.ClaimStatusTransition{
		FromStatus: domain.ClaimStatusActive, ToStatus: domain.ClaimStatusDeprecated,
		ChangedAt: thu, Reason: domain.MergeRedirectReason,
	}

	cases := []struct {
		name       string
		claim      domain.Claim
		status     []domain.ClaimStatusTransition
		validity   []domain.ValidityChange
		at         time.Time
		wantExists bool
		want       domain.Claim
	}{
		{
			name: "before the claim was recorded it does not exist", claim: base, at: t0.Add(-time.Hour),
		},
		{
			name: "every change after T is undone", claim: base, at: tue, wantExists: true,
			status:   []domain.ClaimStatusTransition{insert, retract},
			validity: []domain.ValidityChange{ended, archived},
			want:     domain.Claim{ID: "c", CreatedAt: t0, Status: domain.ClaimStatusActive},
		},
		{
			name: "changes at or before T are kept", claim: base, at: thu, wantExists: true,
			status:   []domain.ClaimStatusTransition{insert, retract},
			validity: []domain.ValidityChange{ended, archived},
			want:     base,
		},
		{
			name: "only later transitions: the first one's from-state held at T", claim: base, at: tue, wantExists: true,
			status: []domain.ClaimStatusTransition{retract},
			want:   domain.Claim{ID: "c", CreatedAt: t0, Status: domain.ClaimStatusActive, ValidTo: thu, ArchivedAt: thu},
		},
		{
			name: "no history: the stored value is the best evidence", claim: base, at: tue, wantExists: true,
			want: base,
		},
		{
			name: "a merge after T is undone: the claim was held separately", at: tue, wantExists: true,
			claim:  merged,
			status: []domain.ClaimStatusTransition{insert, mergedThu},
			want:   domain.Claim{ID: "c", CreatedAt: t0, Status: domain.ClaimStatusActive},
		},
		{
			name: "a merge at or before T stands", at: thu, wantExists: true,
			claim:  merged,
			status: []domain.ClaimStatusTransition{insert, mergedThu},
			want:   merged,
		},
		{
			name: "an undated merge stands: no history, the stored value", at: tue, wantExists: true,
			claim:  merged,
			status: []domain.ClaimStatusTransition{insert, {FromStatus: domain.ClaimStatusActive, ToStatus: domain.ClaimStatusDeprecated, ChangedAt: thu, Reason: "retracted"}},
			want:   domain.Claim{ID: "c", CreatedAt: t0, Status: domain.ClaimStatusActive, MergedInto: "w"},
		},
		{
			name: "an empty from-status never blanks a claim", claim: base, at: tue, wantExists: true,
			status: []domain.ClaimStatusTransition{{ToStatus: domain.ClaimStatusDeprecated, ChangedAt: thu}},
			want:   base,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Rewind(tc.claim, tc.status, tc.validity, tc.at)
			if ok != tc.wantExists {
				t.Fatalf("exists = %v, want %v", ok, tc.wantExists)
			}
			if !ok {
				return
			}
			if got.Status != tc.want.Status || !got.ValidTo.Equal(tc.want.ValidTo) || !got.ArchivedAt.Equal(tc.want.ArchivedAt) || got.MergedInto != tc.want.MergedInto {
				t.Errorf("got status=%q valid_to=%v archived_at=%v merged_into=%q, want status=%q valid_to=%v archived_at=%v merged_into=%q",
					got.Status, got.ValidTo, got.ArchivedAt, got.MergedInto, tc.want.Status, tc.want.ValidTo, tc.want.ArchivedAt, tc.want.MergedInto)
			}
		})
	}
}
