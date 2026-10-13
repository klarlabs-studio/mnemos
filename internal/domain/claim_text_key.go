package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// claimTextPunctuation is the sentence punctuation that does not change what a
// claim says. Tabs fold into spaces so whitespace collapses uniformly.
var claimTextPunctuation = strings.NewReplacer(",", "", ".", "", ";", "", ":", "", "!", "", "?", "", "\t", " ")

// NormalizeClaimText reduces claim text to the form under which two texts are
// the same belief: lower case, sentence punctuation removed, whitespace
// collapsed. Extraction's in-batch dedup and the write-time duplicate check
// both use it, so a text that collapses within one batch also collapses onto a
// stored claim.
func NormalizeClaimText(text string) string {
	normalized := claimTextPunctuation.Replace(strings.ToLower(strings.TrimSpace(text)))
	return strings.Join(strings.Fields(normalized), " ")
}

// ClaimTextKeyLen is the width of a [ClaimTextKey]: 32 hex characters, 128 bits
// of SHA-256.
const ClaimTextKeyLen = 32

// ClaimTextKey is the identity under which storage finds a claim by what it
// says: a fixed-width digest of [NormalizeClaimText]. Backends persist it so
// the write path can ask "is this belief already held?" with one indexed
// lookup instead of loading every claim.
func ClaimTextKey(text string) string {
	sum := sha256.Sum256([]byte(NormalizeClaimText(text)))
	return hex.EncodeToString(sum[:])[:ClaimTextKeyLen]
}
