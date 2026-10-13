package domain

import "testing"

// Texts that differ only in case, surrounding space, inner spacing or
// sentence punctuation state the same belief and must share a key.
func TestClaimTextKey_SameBeliefSameKey(t *testing.T) {
	same := []string{
		"The retry budget is 3 attempts",
		"the retry budget is 3 attempts.",
		"  The retry   budget is 3 attempts!  ",
		"The retry budget, is 3 attempts",
	}
	want := ClaimTextKey(same[0])
	for _, s := range same[1:] {
		if got := ClaimTextKey(s); got != want {
			t.Errorf("ClaimTextKey(%q) = %q, want %q (same belief as %q)", s, got, want, same[0])
		}
	}
}

func TestClaimTextKey_DifferentBeliefDifferentKey(t *testing.T) {
	if ClaimTextKey("The retry budget is 3 attempts") == ClaimTextKey("The retry budget is 5 attempts") {
		t.Fatal("different beliefs share a key")
	}
}

// The key is a fixed-width column value on every SQL backend, so its length
// must not depend on the text.
func TestClaimTextKey_FixedWidth(t *testing.T) {
	for _, s := range []string{"", "x", "a much longer belief about the deploy pipeline and its gates"} {
		if got := len(ClaimTextKey(s)); got != ClaimTextKeyLen {
			t.Errorf("len(ClaimTextKey(%q)) = %d, want %d", s, got, ClaimTextKeyLen)
		}
	}
}

func TestNormalizeClaimText(t *testing.T) {
	if got := NormalizeClaimText("  Hello,  World!\tAgain. "); got != "hello world again" {
		t.Errorf("NormalizeClaimText = %q", got)
	}
}
