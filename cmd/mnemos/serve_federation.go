package main

import (
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/felixgeelhaar/mnemos/internal/domain"
	"github.com/felixgeelhaar/mnemos/internal/store"
)

// federationExportResponse is the wire shape for
// GET /v1/federation/export. Carries anonymized playbooks — the
// shared abstractions Mnemos synthesises from per-tenant lessons.
// Nothing tenant-specific surfaces: created_by, derived_from_lessons
// ids, scope tuples, and per-tenant ids are stripped before render.
//
// The opt-in gate (MNEMOS_FEDERATION_ENABLED=true) is the load-bearing
// safety property. Without it the endpoint refuses to serve so a
// forgotten env flag can never leak a sister org's playbook corpus.
type federationExportResponse struct {
	GeneratedAt time.Time           `json:"generated_at"`
	Source      string              `json:"source"`  // "mnemos"
	Version     string              `json:"version"` // schema version
	Playbooks   []federationPlaybook `json:"playbooks"`
	TotalCount  int                 `json:"total_count"`
}

// federationPlaybook is the anonymized projection. Steps and the
// statement are preserved (they ARE the value); identity and
// provenance back to a specific tenant's lessons are not.
type federationPlaybook struct {
	Trigger      string                  `json:"trigger"`
	Statement    string                  `json:"statement"`
	Steps        []federationPlaybookStep `json:"steps"`
	Confidence   float64                 `json:"confidence"`
	LessonCount  int                     `json:"lesson_count"` // how many lessons backed this playbook; the count travels, the ids don't
	DerivedAt    time.Time               `json:"derived_at"`
	LastVerified time.Time               `json:"last_verified,omitempty"`
}

type federationPlaybookStep struct {
	Order       int    `json:"order"`
	Action      string `json:"action"`
	Description string `json:"description,omitempty"`
	Condition   string `json:"condition,omitempty"`
}

// federationExportVersion is the wire-format generation for the
// mnemos federation payload. Bump on shape changes so pull-mirror
// clients can reject incompatible versions explicitly.
const federationExportVersion = "v1"

func mnemosFederationEnabled() bool {
	v := os.Getenv("MNEMOS_FEDERATION_ENABLED")
	if v == "" {
		return false
	}
	enabled, _ := strconv.ParseBool(v)
	return enabled
}

func makeFederationExportHandler(conn *store.Conn) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !mnemosFederationEnabled() {
			writeError(w, http.StatusNotImplemented,
				"federation export disabled (set MNEMOS_FEDERATION_ENABLED=true to opt in)")
			return
		}
		playbooks, err := conn.Playbooks.ListAll(r.Context())
		if err != nil {
			writeInternalError(w, "list playbooks for federation", err)
			return
		}
		writeJSON(w, http.StatusOK, federationExportResponse{
			GeneratedAt: time.Now().UTC(),
			Source:      "mnemos",
			Version:     federationExportVersion,
			Playbooks:   anonymizePlaybooks(playbooks),
			TotalCount:  len(playbooks),
		})
	}
}

// anonymizePlaybooks projects each Playbook into the federation wire
// shape, stripping tenant-identifying fields. Pure function, no IO —
// tested directly so the anonymization contract is auditable.
func anonymizePlaybooks(in []domain.Playbook) []federationPlaybook {
	out := make([]federationPlaybook, 0, len(in))
	for _, p := range in {
		row := federationPlaybook{
			Trigger:      p.Trigger,
			Statement:    p.Statement,
			Confidence:   p.Confidence,
			LessonCount:  len(p.DerivedFromLessons),
			DerivedAt:    p.DerivedAt,
			LastVerified: p.LastVerified,
		}
		row.Steps = make([]federationPlaybookStep, 0, len(p.Steps))
		for _, s := range p.Steps {
			row.Steps = append(row.Steps, federationPlaybookStep{
				Order:       s.Order,
				Action:      s.Action,
				Description: s.Description,
				Condition:   s.Condition,
			})
		}
		out = append(out, row)
	}
	return out
}
