package main

import (
	"context"
	"strconv"
	"time"
)

// handleVerify routes `mnemos verify <claim-id> [--half-life-days N]`.
// Bumps the claim's last_verified to now and increments verify_count.
// An optional --half-life-days writes a per-claim freshness override.
func handleVerify(args []string, _ Flags) {
	if len(args) == 0 {
		exitWithMnemosError(false, NewUserError("verify requires a claim id"))
		return
	}
	claimID := args[0]
	rest := args[1:]
	var halfLife float64
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case "--half-life-days":
			if i+1 >= len(rest) {
				exitWithMnemosError(false, NewUserError("--half-life-days requires a value"))
				return
			}
			f, err := strconv.ParseFloat(rest[i+1], 64)
			if err != nil || f < 0 {
				exitWithMnemosError(false, NewUserError("--half-life-days must be a non-negative float"))
				return
			}
			halfLife = f
			i++
		default:
			exitWithMnemosError(false, NewUserError("unknown flag %q", rest[i]))
			return
		}
	}
	ctx := context.Background()
	w, err := openWriter(ctx)
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "open store"))
		return
	}
	defer closeWriter(w)
	now := time.Now().UTC()
	if err := w.MarkVerified(ctx, claimID, now, halfLife); err != nil {
		exitWithMnemosError(false, NewSystemError(err, "mark verified"))
		return
	}
	emitJSON(map[string]any{
		"id":             claimID,
		"verified_at":    now.Format(time.RFC3339),
		"half_life_days": halfLife,
	})
}
