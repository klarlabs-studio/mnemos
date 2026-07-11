package query

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// PrecedencePolicy governs which brain tier wins when a federated read surfaces
// the same topic from both the tenant/repo brain (hippocampus) and the global
// brain (neocortex). See ADR 0011, "Read-time precedence". It is a small closed
// enum, never free-form.
type PrecedencePolicy string

const (
	// PrecedenceTenantWins keeps the tenant/repo tier authoritative on conflict.
	// This is the local-dev default and preserves pre-ADR-0011 behavior exactly.
	PrecedenceTenantWins PrecedencePolicy = "tenant-wins"
	// PrecedenceGlobalWins lets the vetted global (neocortex) tier win on
	// conflict — the default for curated product brains (e.g. pet-medical).
	PrecedenceGlobalWins PrecedencePolicy = "global-wins"
	// PrecedenceSurfaceDissonance keeps both conflicting claims and flags the
	// disagreement so the agent reconciles before acting; neither tier silently
	// wins.
	PrecedenceSurfaceDissonance PrecedencePolicy = "surface-dissonance"
)

// DefaultPrecedence is the policy used when MNEMOS_PRECEDENCE is unset. It
// preserves the historical tenant-wins behavior byte-for-byte.
const DefaultPrecedence = PrecedenceTenantWins

// EnvPrecedence is the environment variable that selects the read-time
// precedence policy.
const EnvPrecedence = "MNEMOS_PRECEDENCE"

// ParsePrecedence resolves a precedence-policy string. Empty input yields the
// default (tenant-wins); any unrecognized value is rejected with a clear error.
func ParsePrecedence(s string) (PrecedencePolicy, error) {
	switch PrecedencePolicy(strings.TrimSpace(s)) {
	case "":
		return DefaultPrecedence, nil
	case PrecedenceTenantWins:
		return PrecedenceTenantWins, nil
	case PrecedenceGlobalWins:
		return PrecedenceGlobalWins, nil
	case PrecedenceSurfaceDissonance:
		return PrecedenceSurfaceDissonance, nil
	default:
		return DefaultPrecedence, fmt.Errorf(
			"invalid %s %q (want one of: tenant-wins, global-wins, surface-dissonance)", EnvPrecedence, s)
	}
}

// PrecedenceFromEnv reads MNEMOS_PRECEDENCE and parses it, defaulting to
// tenant-wins when unset and erroring on an unrecognized value.
func PrecedenceFromEnv() (PrecedencePolicy, error) {
	return ParsePrecedence(os.Getenv(EnvPrecedence))
}

// PrecedenceOrDefault reads MNEMOS_PRECEDENCE and returns the parsed policy,
// silently falling back to the tenant-wins default when the value is invalid.
// Callers on fail-open read paths (the hooks) use this so a misconfiguration
// degrades to today's behavior rather than dropping memory injection; startup
// validation surfaces the clear error separately.
func PrecedenceOrDefault() PrecedencePolicy {
	p, err := PrecedenceFromEnv()
	if err != nil {
		return DefaultPrecedence
	}
	return p
}

// precedenceNegations are the polarity markers used to tell "X is fast" from
// "X is not fast" when detecting a same-topic, opposing-polarity conflict.
var precedenceNegations = map[string]bool{
	"not": true, "no": true, "never": true, "cannot": true, "without": true,
	"false": true, "isn't": true, "aren't": true, "wasn't": true, "weren't": true,
	"don't": true, "doesn't": true, "didn't": true, "won't": true, "can't": true,
	"shouldn't": true, "couldn't": true, "wouldn't": true, "n't": true,
}

// polarityCore normalizes text to an order-independent content core plus a
// polarity flag: it lowercases, strips surrounding punctuation, removes negation
// markers (recording that polarity flipped), sorts the remaining words, and
// joins them. Two claims with the same core but opposite polarity are a
// same-topic disagreement.
func polarityCore(text string) (core string, negated bool) {
	words := strings.Fields(strings.ToLower(text))
	kept := make([]string, 0, len(words))
	for _, w := range words {
		w = strings.Trim(w, ",.;:!?()[]{}\"'`")
		if w == "" {
			continue
		}
		if precedenceNegations[w] {
			negated = !negated
			continue
		}
		kept = append(kept, w)
	}
	sort.Strings(kept)
	return strings.Join(kept, " "), negated
}

// Conflict reports whether two claim texts assert the same topic with opposing
// polarity — the signal that a tenant belief and a global belief disagree. It is
// deliberately conservative: identical text (a mere duplicate) is not a
// conflict, and empty cores never conflict.
func Conflict(a, b string) bool {
	aCore, aNeg := polarityCore(a)
	bCore, bNeg := polarityCore(b)
	if aCore == "" || bCore == "" {
		return false
	}
	return aCore == bCore && aNeg != bNeg
}
