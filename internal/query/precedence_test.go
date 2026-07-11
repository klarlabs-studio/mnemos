package query

import "testing"

func TestParsePrecedence(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    PrecedencePolicy
		wantErr bool
	}{
		{"empty defaults to tenant-wins", "", PrecedenceTenantWins, false},
		{"whitespace defaults", "   ", PrecedenceTenantWins, false},
		{"tenant-wins", "tenant-wins", PrecedenceTenantWins, false},
		{"global-wins", "global-wins", PrecedenceGlobalWins, false},
		{"surface-dissonance", "surface-dissonance", PrecedenceSurfaceDissonance, false},
		{"trimmed", "  global-wins  ", PrecedenceGlobalWins, false},
		{"unknown rejected", "banana", DefaultPrecedence, true},
		{"case sensitive", "Tenant-Wins", DefaultPrecedence, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParsePrecedence(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ParsePrecedence(%q) err = %v, wantErr %v", tc.in, err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("ParsePrecedence(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestDefaultPrecedenceIsTenantWins(t *testing.T) {
	if DefaultPrecedence != PrecedenceTenantWins {
		t.Fatalf("default precedence = %q, want tenant-wins", DefaultPrecedence)
	}
}

func TestPrecedenceFromEnv(t *testing.T) {
	t.Setenv(EnvPrecedence, "")
	if p, err := PrecedenceFromEnv(); err != nil || p != PrecedenceTenantWins {
		t.Fatalf("unset env → (%q,%v), want tenant-wins/nil", p, err)
	}
	t.Setenv(EnvPrecedence, "global-wins")
	if p, err := PrecedenceFromEnv(); err != nil || p != PrecedenceGlobalWins {
		t.Fatalf("env global-wins → (%q,%v)", p, err)
	}
	t.Setenv(EnvPrecedence, "nonsense")
	if _, err := PrecedenceFromEnv(); err == nil {
		t.Fatal("invalid env value should error")
	}
	// PrecedenceOrDefault never errors; it degrades to the default.
	if p := PrecedenceOrDefault(); p != DefaultPrecedence {
		t.Fatalf("PrecedenceOrDefault on bad value = %q, want default", p)
	}
}

func TestConflict(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"opposing polarity same topic", "the API is stable", "the API is not stable", true},
		{"opposing polarity reordered", "cache is enabled", "enabled is not cache", true},
		{"identical text is a duplicate not a conflict", "uses Kafka", "uses Kafka", false},
		{"different topic", "uses Kafka", "prefers Postgres", false},
		{"same polarity same topic", "the API is stable", "the API is stable", false},
		{"empty never conflicts", "", "not", false},
		{"punctuation ignored", "Deploy on Fridays.", "never deploy on Fridays!", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Conflict(tc.a, tc.b); got != tc.want {
				t.Errorf("Conflict(%q,%q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
