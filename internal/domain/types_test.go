package domain

import (
	"encoding/json"
	"testing"
	"time"
)

func TestClaimValidate(t *testing.T) {
	tests := []struct {
		name      string
		claim     Claim
		wantError bool
	}{
		{
			name: "valid claim",
			claim: Claim{
				ID:         "c-1",
				Text:       "Revenue dropped after feature launch",
				Type:       ClaimTypeFact,
				Status:     ClaimStatusActive,
				Confidence: 0.9,
			},
		},
		{
			name: "missing id",
			claim: Claim{
				Text:       "x",
				Type:       ClaimTypeFact,
				Status:     ClaimStatusActive,
				Confidence: 0.7,
			},
			wantError: true,
		},
		{
			name: "confidence out of range",
			claim: Claim{
				ID:         "c-2",
				Text:       "x",
				Type:       ClaimTypeFact,
				Status:     ClaimStatusActive,
				Confidence: 1.1,
			},
			wantError: true,
		},
		{
			name: "invalid type",
			claim: Claim{
				ID:         "c-3",
				Text:       "x",
				Type:       "unknown",
				Status:     ClaimStatusActive,
				Confidence: 0.5,
			},
			wantError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.claim.Validate()
			if (err != nil) != tc.wantError {
				t.Fatalf("Validate() error = %v, wantError %v", err, tc.wantError)
			}
		})
	}
}

func TestClaimEvidenceValidate(t *testing.T) {
	valid := ClaimEvidence{ClaimID: "c-1", EventID: "e-1"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() unexpected error: %v", err)
	}

	missingEvent := ClaimEvidence{ClaimID: "c-1"}
	if err := missingEvent.Validate(); err == nil {
		t.Fatal("Validate() expected error for missing event_id")
	}
}

func TestEventValidate(t *testing.T) {
	base := Event{
		ID:            "ev_1",
		SourceInputID: "in_1",
		Content:       "a meaningful sentence",
		Timestamp:     time.Unix(1, 0),
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid event rejected: %v", err)
	}

	cases := []struct {
		name  string
		mut   func(e *Event)
		wantB bool
	}{
		{"missing id", func(e *Event) { e.ID = "" }, true},
		{"empty content", func(e *Event) { e.Content = "" }, true},
		{"whitespace content", func(e *Event) { e.Content = "   " }, true},
		{"missing source_input_id", func(e *Event) { e.SourceInputID = "" }, true},
		{"zero timestamp", func(e *Event) { e.Timestamp = time.Time{} }, true},
		{"oversize content", func(e *Event) {
			e.Content = make_string('x', MaxEventContentBytes+1)
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := base
			tc.mut(&e)
			err := e.Validate()
			if tc.wantB && err == nil {
				t.Error("expected error, got nil")
			}
			if !tc.wantB && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// make_string is a tiny helper to build a string of n copies of c
// without pulling in strings.Repeat (which we use elsewhere).
func make_string(c byte, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = c
	}
	return string(b)
}

func TestRelationshipValidate(t *testing.T) {
	base := Relationship{
		ID:          "r_1",
		Type:        RelationshipTypeSupports,
		FromClaimID: "cl_a",
		ToClaimID:   "cl_b",
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid relationship rejected: %v", err)
	}

	cases := []struct {
		name string
		mut  func(r *Relationship)
	}{
		{"missing id", func(r *Relationship) { r.ID = "" }},
		{"missing from", func(r *Relationship) { r.FromClaimID = "" }},
		{"missing to", func(r *Relationship) { r.ToClaimID = "" }},
		{"self reference", func(r *Relationship) { r.ToClaimID = r.FromClaimID }},
		{"bad type", func(r *Relationship) { r.Type = "undermines" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := base
			tc.mut(&r)
			if err := r.Validate(); err == nil {
				t.Errorf("expected error for %s, got nil", tc.name)
			}
		})
	}
}

// TestProvenanceReportRoundTrip pins the JSON shape of ProvenanceReport
// (including the ProseRationale field added in v0.15.0) so a renamed
// or dropped field is caught by the domain test suite directly rather
// than only by integration tests in internal/query.
func TestProvenanceReportRoundTrip(t *testing.T) {
	in := ProvenanceReport{
		ClaimID:   "cl_42",
		ClaimText: "user prefers vegetarian",
		Score:     0.86,
		Signals: []ProvenanceSignal{
			{
				Name:         "base_trust",
				Value:        0.7,
				Weight:       0.5,
				Contribution: 0.35,
				Detail:       "stored trust score 0.70",
			},
			{
				Name:         "recency",
				Value:        0.92,
				Weight:       0.1,
				Contribution: 0.092,
				Detail:       "3 days since last evidence",
			},
		},
		Rationale:      "base=0.70 authority=0.50 citations=2(0.31) recency=0.92 liveness=live",
		ProseRationale: "Most recent evidence 3 days ago (fresh). Live source.",
		SourceDocument: "user-prefs.md",
		Liveness:       LivenessLive,
	}

	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var out ProvenanceReport
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if out.ClaimID != in.ClaimID {
		t.Errorf("claim_id round-trip drift: %q -> %q", in.ClaimID, out.ClaimID)
	}
	if out.Score != in.Score {
		t.Errorf("score round-trip drift: %v -> %v", in.Score, out.Score)
	}
	if out.ProseRationale != in.ProseRationale {
		t.Errorf("prose_rationale round-trip drift: %q -> %q", in.ProseRationale, out.ProseRationale)
	}
	if out.Liveness != LivenessLive {
		t.Errorf("liveness round-trip drift: %q -> %q", in.Liveness, out.Liveness)
	}
	if len(out.Signals) != len(in.Signals) {
		t.Fatalf("signals length mismatch: %d -> %d", len(in.Signals), len(out.Signals))
	}
	if out.Signals[0].Contribution != in.Signals[0].Contribution {
		t.Errorf("signal contribution round-trip drift: %v -> %v",
			in.Signals[0].Contribution, out.Signals[0].Contribution)
	}

	// Pin the wire format: the JSON tag for ProseRationale is
	// "prose_rationale,omitempty" so an unset field disappears from
	// the payload — required for backward-compat with older readers.
	emptier := ProvenanceReport{ClaimID: "cl_x", Score: 0.5}
	b2, _ := json.Marshal(emptier)
	if string(b2) == "" {
		t.Fatal("empty marshal output")
	}
	for _, banned := range []string{"prose_rationale", "source_document", "liveness"} {
		if contains(string(b2), banned) {
			t.Errorf("unset %q must omit from JSON; got %s", banned, b2)
		}
	}
}

// contains is a tiny wrapper that avoids pulling strings into this
// already strings-light test.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// TestScopePredicates pins the Scope helpers used across the
// multi-tenant filter surfaces (CLI --service/--env/--team, query,
// MCP filters). Adding direct domain-package coverage for IsEmpty,
// Equal, Key, and Matches keeps the helpers behaviour-tested without
// relying on integration tests in internal/query.
func TestScopePredicates(t *testing.T) {
	empty := Scope{}
	billingProd := Scope{Service: "billing", Env: "prod"}
	billingProdTeam := Scope{Service: "billing", Env: "prod", Team: "core"}

	if !empty.IsEmpty() {
		t.Error("zero Scope must report IsEmpty=true")
	}
	if billingProd.IsEmpty() {
		t.Error("populated Scope must report IsEmpty=false")
	}

	if !billingProd.Equal(Scope{Service: "billing", Env: "prod"}) {
		t.Error("identical Scopes must compare equal")
	}
	if billingProd.Equal(billingProdTeam) {
		t.Error("Scopes differing only in Team must NOT be equal")
	}

	// Key must include all three fields and be stable.
	wantKey := "billing|prod|core"
	if got := billingProdTeam.Key(); got != wantKey {
		t.Errorf("Key() = %q, want %q", got, wantKey)
	}

	// Matches: empty filter matches anything; partial filter is a
	// wildcard on unset fields; conflicting field rejects.
	if !billingProdTeam.Matches(empty) {
		t.Error("empty filter must match any scope")
	}
	if !billingProdTeam.Matches(Scope{Service: "billing"}) {
		t.Error("partial filter should match on populated fields only")
	}
	if billingProdTeam.Matches(Scope{Env: "staging"}) {
		t.Error("conflicting filter must reject")
	}
	if billingProdTeam.Matches(Scope{Team: "growth"}) {
		t.Error("conflicting team filter must reject")
	}
}
