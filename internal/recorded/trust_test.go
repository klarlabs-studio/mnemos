package recorded

import (
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/trust"
)

var (
	mon = time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	tue = mon.Add(24 * time.Hour)
	thu = mon.Add(72 * time.Hour)
)

func TestLinkRecordedAt(t *testing.T) {
	tests := []struct {
		name                   string
		link                   domain.ClaimEvidence
		claimCreated, ingested time.Time
		want                   time.Time
	}{
		{"own time wins", domain.ClaimEvidence{CreatedAt: thu}, mon, mon, thu},
		{"legacy: episode ingested after the claim", domain.ClaimEvidence{}, mon, tue, tue},
		{"legacy: claim created after the episode", domain.ClaimEvidence{}, tue, mon, tue},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LinkRecordedAt(tt.link, tt.claimCreated, tt.ingested); !got.Equal(tt.want) {
				t.Errorf("LinkRecordedAt = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCreditAt(t *testing.T) {
	history := []domain.CreditChange{
		{ClaimID: "c", From: 0, To: 0.1, ChangedAt: tue},
		{ClaimID: "c", From: 0.1, To: -0.05, ChangedAt: thu},
	}
	tests := []struct {
		name    string
		history []domain.CreditChange
		at      time.Time
		want    float64
	}{
		{"before every change: the first from-value", history, mon, 0},
		{"at a change: its to-value", history, tue, 0.1},
		{"between changes", history, tue.Add(time.Hour), 0.1},
		{"after the last change", history, thu.Add(time.Hour), -0.05},
		{"no history: the stored value", nil, mon, 0.3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CreditAt(0.3, tt.history, tt.at); got != tt.want {
				t.Errorf("CreditAt = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTrustInputAt(t *testing.T) {
	claim := domain.Claim{
		ID: "c", Confidence: 0.8, CreatedAt: mon, HalfLifeDays: 30, LastConfirmed: thu,
		ConfidenceComponents: map[string]float64{domain.CreditAppliedComponentKey: 0.1},
	}
	events := map[string]domain.Event{
		"e-mon":       {ID: "e-mon", CreatedBy: "alice", Timestamp: mon, IngestedAt: mon},
		"e-mon-bob":   {ID: "e-mon-bob", CreatedBy: "bob", Timestamp: mon.Add(time.Hour), IngestedAt: mon.Add(time.Hour)},
		"e-mon-later": {ID: "e-mon-later", CreatedBy: "carol", Timestamp: mon, IngestedAt: mon},
		"e-thu":       {ID: "e-thu", CreatedBy: "dave", Timestamp: thu, IngestedAt: thu},
	}
	links := []domain.ClaimEvidence{
		{ClaimID: "c", EventID: "e-mon", CreatedAt: mon},
		{ClaimID: "c", EventID: "e-mon", CreatedAt: mon}, // a duplicate counts once
		{ClaimID: "c", EventID: "e-mon-bob"},             // legacy link: dated by its episode
		{ClaimID: "c", EventID: "e-mon-later", CreatedAt: thu},
		{ClaimID: "c", EventID: "e-thu", CreatedAt: thu},
		{ClaimID: "c", EventID: "e-missing", CreatedAt: mon}, // episode not visible
		{ClaimID: "other", EventID: "e-mon", CreatedAt: mon}, // another claim's link
	}
	credit := []domain.CreditChange{{ClaimID: "c", From: 0, To: 0.1, ChangedAt: thu}}
	// On Thursday confidence moved 0.6 -> 0.8 and half-life 0 -> 30.
	inputs := []domain.TrustInputChange{{ClaimID: "c", Confidence: 0.6, ChangedAt: thu}}

	got := TrustInputAt(claim, links, events, nil, credit, inputs, tue)
	want := domain.TrustInput{
		Confidence:     0.6,
		EvidenceCount:  domain.EffectiveEvidenceCount(2, 2),
		LatestEvidence: mon.Add(time.Hour),
	}
	if got != want {
		t.Errorf("TrustInputAt(Tuesday) = %+v, want %+v", got, want)
	}

	now := TrustInputAt(claim, links, events, nil, credit, inputs, thu.Add(time.Hour))
	if now.EvidenceCount != domain.EffectiveEvidenceCount(4, 4) || !now.LatestEvidence.Equal(thu) ||
		!now.LastConfirmed.Equal(thu) || now.Credit != 0.1 || now.Confidence != 0.8 || now.HalfLifeDays != 30 {
		t.Errorf("TrustInputAt(after Thursday) = %+v, want all four sources, Thursday's evidence, confirmation and credit", now)
	}

	if a, b := TrustAt(claim, links, events, nil, credit, inputs, tue), trust.At(want, tue); a != b {
		t.Errorf("TrustAt = %v, want trust.At over the inputs at T = %v", a, b)
	}
}

// StoredInputsAt: the prior values of the first change after t, or the stored
// values when nothing changed after t. A belief confirmed both before and after
// t reads as confirmed at its earlier confirmation, not as unconfirmed.
func TestStoredInputsAt(t *testing.T) {
	wed := mon.Add(48 * time.Hour)
	claim := domain.Claim{ID: "c", Confidence: 0.9, HalfLifeDays: 14, LastConfirmed: thu}
	history := []domain.TrustInputChange{
		// Out of order on purpose, and one row of another claim.
		{ClaimID: "c", Confidence: 0.7, HalfLifeDays: 30, LastConfirmed: mon, ChangedAt: thu},
		{ClaimID: "c", Confidence: 0.5, HalfLifeDays: 30, ChangedAt: mon},
		{ClaimID: "other", Confidence: 0.1, ChangedAt: tue},
		{ClaimID: "c", Confidence: 0.6, HalfLifeDays: 30, LastConfirmed: mon, ChangedAt: wed},
	}
	tests := []struct {
		name      string
		history   []domain.TrustInputChange
		at        time.Time
		conf, hl  float64
		confirmed time.Time
	}{
		{"before every change: the first change's prior values", history, mon.Add(-time.Hour), 0.5, 30, time.Time{}},
		{"confirmed Monday, again Thursday: Tuesday sees Monday's", history, tue, 0.6, 30, mon},
		{"between the last two changes", history, wed.Add(time.Hour), 0.7, 30, mon},
		{"after every change: the stored values", history, thu.Add(time.Hour), 0.9, 14, thu},
		{"a change exactly at t is already in effect", history, thu, 0.9, 14, thu},
		{"no history: the stored values, but no future confirmation", nil, tue, 0.9, 14, time.Time{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conf, hl, confirmed := StoredInputsAt(claim, tt.history, tt.at)
			if conf != tt.conf || hl != tt.hl || !confirmed.Equal(tt.confirmed) {
				t.Errorf("StoredInputsAt = (%v, %v, %v), want (%v, %v, %v)", conf, hl, confirmed, tt.conf, tt.hl, tt.confirmed)
			}
		})
	}
}
