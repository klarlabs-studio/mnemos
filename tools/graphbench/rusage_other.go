//go:build !unix

package main

// maxRSS is unavailable without getrusage; the report records 0.
func maxRSS() int64 { return 0 }
