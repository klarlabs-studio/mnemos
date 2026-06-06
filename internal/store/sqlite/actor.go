package sqlite

import "go.klarlabs.de/mnemos/internal/domain"

// actorOr returns s if non-empty, otherwise the SystemUser sentinel.
// Lets callers leave domain.X.CreatedBy unset on internal write paths
// (CLI ingest, pipeline) and have the row land as authored by <system>
// rather than as authored by the empty string.
func actorOr(s string) string {
	if s == "" {
		return domain.SystemUser
	}
	return s
}
