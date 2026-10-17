package narration

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

// The verdict cache is what makes a pass over a whole brain resumable. A local
// 8B model judges about 20 claims in 25 seconds, so tens of thousands of claims
// take many hours and will outlast any single job budget. With the cache, a
// re-run pays only for the claims the last one did not reach.
//
// The key covers the prompt, the model identity and the claim text, so changing
// any of them judges again rather than reusing a verdict the current judge
// would not give.

func (j *Judge) cacheKey(text string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(PromptVersion))
	_, _ = h.Write([]byte("\n"))
	_, _ = h.Write([]byte(j.identity))
	_, _ = h.Write([]byte("\n"))
	_, _ = h.Write([]byte(text))
	return hex.EncodeToString(h.Sum(nil))
}

func (j *Judge) cached(text string) (Verdict, bool) {
	if j.cacheDir == "" {
		return Unknown, false
	}
	data, err := os.ReadFile(filepath.Join(j.cacheDir, j.cacheKey(text)+".txt"))
	if err != nil {
		return Unknown, false
	}
	switch string(data) {
	case "keep":
		return Keep, true
	case "drop":
		return Drop, true
	}
	return Unknown, false
}

// store records a verdict. Cache I/O never fails the pass: a verdict that
// cannot be written is simply judged again next time.
func (j *Judge) store(text string, v Verdict) {
	if j.cacheDir == "" {
		return
	}
	word := "keep"
	if v == Drop {
		word = "drop"
	}
	if err := os.MkdirAll(j.cacheDir, 0o750); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(j.cacheDir, j.cacheKey(text)+".txt"), []byte(word), 0o600)
}
