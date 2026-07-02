package mnemos

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Info returns this Memory's runtime configuration. See [Info].
func (m *memory) Info() Info { return m.info }

// deriveInfo builds the immutable [Info] snapshot from the resolved
// config and storage DSN. Called once by [New].
func deriveInfo(cfg config, dsn string) Info {
	backend, ns := parseBackendNamespace(dsn)
	return Info{
		Backend:   backend,
		Namespace: ns,
		Mode:      cfg.mode.String(),
		Embedder:  embedderLabel(cfg),
		Actor:     cfg.actorID,
	}
}

// parseBackendNamespace extracts the URL scheme (backend) and the
// ?namespace= value (default "mnemos") from a storage DSN.
func parseBackendNamespace(dsn string) (backend, namespace string) {
	backend = dsn
	if i := strings.Index(dsn, "://"); i >= 0 {
		backend = dsn[:i]
	}
	namespace = "mnemos"
	if u, err := url.Parse(dsn); err == nil {
		if v := u.Query().Get("namespace"); v != "" {
			namespace = v
		}
	}
	return backend, namespace
}

// embedderLabel names the embedding source for [Info.Embedder].
func embedderLabel(cfg config) string {
	switch cfg.mode {
	case modeEnhanced:
		return "enhanced"
	case modeShared:
		return "shared"
	default:
		if cfg.embedder != nil {
			return "custom"
		}
		return "none"
	}
}

// RememberClaimWithEvidence records one observation event per non-blank
// evidence string, then stores a fact claim linking them. It is the
// convenience wrapper over RememberEvent + RememberClaim for the common
// "here's a statement and the references backing it" case. See the
// [Memory] interface for the contract.
func (m *memory) RememberClaimWithEvidence(ctx context.Context, text string, evidence []string, validFrom time.Time) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", errors.New("mnemos: RememberClaimWithEvidence: text is required")
	}
	if validFrom.IsZero() {
		validFrom = time.Now().UTC()
	}

	eventIDs := make([]string, 0, len(evidence))
	for _, ref := range evidence {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		// Set an explicit id so we can link the claim to it (RememberEvent
		// generates one internally but does not return it).
		eid := uuid.NewString()
		if err := m.RememberEvent(ctx, Event{ID: eid, At: validFrom, Type: "observation", Content: ref}); err != nil {
			return "", err
		}
		eventIDs = append(eventIDs, eid)
	}
	if len(eventIDs) == 0 {
		return "", errors.New("mnemos: RememberClaimWithEvidence: at least one non-blank evidence reference is required")
	}

	return m.RememberClaim(ctx, ClaimItem{
		Text:      text,
		Type:      "fact",
		EventIDs:  eventIDs,
		ValidFrom: validFrom,
	})
}
