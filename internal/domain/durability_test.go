package domain

import "testing"

func TestDurability_Normalized(t *testing.T) {
	for _, tc := range []struct {
		in   Durability
		want Durability
	}{
		{"durable", DurabilityDurable},
		{" Durable ", DurabilityDurable},
		{"SESSION", DurabilitySessionLocal},
		{"", DurabilityUnknown},
		// Anything unrecognised must degrade to unknown, never to
		// session-local: a typo must not silently demote a belief.
		{"sessionish", DurabilityUnknown},
		{"garbage", DurabilityUnknown},
	} {
		if got := tc.in.Normalized(); got != tc.want {
			t.Errorf("%q.Normalized() = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The whole back catalogue is unclassified. Treating unknown as session-local
// would demote a brain's worth of knowledge on absence of evidence.
func TestDurability_UnknownIsNotSessionLocal(t *testing.T) {
	if DurabilityUnknown.IsSessionLocal() {
		t.Fatal("unknown must not be session-local")
	}
	if DurabilityDurable.IsSessionLocal() {
		t.Fatal("durable must not be session-local")
	}
	if !DurabilitySessionLocal.IsSessionLocal() {
		t.Fatal("session must be session-local")
	}
}
