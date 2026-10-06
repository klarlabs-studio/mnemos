package browse

// window returns up to limit items of sorted starting at start, and whether
// more remain after them.
func window[T any](sorted []T, start, limit int) ([]T, bool) {
	if start >= len(sorted) {
		return []T{}, false
	}
	rest := sorted[start:]
	if len(rest) > limit {
		return rest[:limit], true
	}
	return rest, false
}
