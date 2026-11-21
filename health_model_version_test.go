package mnemos

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"testing"
)

// The thresholds each health model version grades under, as a fingerprint of
// every name=value pair. When a threshold changes, this test fails until
// HealthModelVersion is bumped and the new fingerprint is added here — a
// snapshot series must be able to tell a change of rules from a change of the
// brain (#389). Never edit an existing entry: old snapshots were graded by it.
var healthModelFingerprints = map[string]string{
	"health/v1": "f16c379b6ee4376c099000438de107e579946f9c34222c78d75ead0e82354b29",
}

func healthThresholdFingerprint() string {
	th := healthThresholds()
	names := make([]string, 0, len(th))
	for n := range th {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		fmt.Fprintf(&b, "%s=%g\n", n, th[n])
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func TestHealthModelVersion_PinsThresholds(t *testing.T) {
	got := healthThresholdFingerprint()
	want, ok := healthModelFingerprints[HealthModelVersion]
	if !ok {
		t.Fatalf("HealthModelVersion %q has no fingerprint; add %q: %q", HealthModelVersion, HealthModelVersion, got)
	}
	if got != want {
		t.Fatalf("health thresholds changed under %s (fingerprint %s, pinned %s): bump HealthModelVersion "+
			"and add the new version's fingerprint, leaving the old entry in place", HealthModelVersion, got, want)
	}
}
