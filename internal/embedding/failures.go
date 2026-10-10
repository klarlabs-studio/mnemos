package embedding

import "sync/atomic"

var failures atomic.Int64

// RecordFailure counts one background embed that stored no vector (#457).
func RecordFailure() { failures.Add(1) }

// Failures returns how many background embeds in this process stored no
// vector. `mnemos serve` exports it as mnemos_embed_failed_total.
func Failures() int64 { return failures.Load() }
