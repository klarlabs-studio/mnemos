package brainbench

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/extract"
	"go.klarlabs.de/mnemos/internal/pipeline"
	"go.klarlabs.de/mnemos/internal/relate"
	"go.klarlabs.de/mnemos/internal/store"
)

// SeedStats records what seeding produced, so the report can show the shape of
// the brain both arms started from.
type SeedStats struct {
	Events        int `json:"events"`
	Claims        int `json:"claims"`
	Relationships int `json:"relationships"`
	Embeddings    int `json:"embeddings"`
	// DedupedAtIngest counts claims the ingest-time exact-text deduper
	// collapsed BEFORE consolidation ever saw them. It matters because that
	// redundancy is not available for consolidation to take credit for: the
	// harness measures what consolidation adds ON TOP of ingest dedupe, which
	// is the only fair comparison.
	DedupedAtIngest int `json:"deduped_at_ingest"`
	// Changes counts the timeline's curation operations applied.
	Changes int `json:"changes,omitempty"`
	// Phases holds the real write-clock instants of a timeline scenario's
	// phases. As-of probes are answered against them.
	Phases PhaseTimes `json:"phases,omitempty"`

	// IngestDuration is the wall time spent in the ingest path proper —
	// extraction, dedupe, relationship detection, persistence and embedding
	// — summed over documents. CurationDuration is the time spent applying a
	// timeline's changes. Neither includes the phase-boundary sleeps. Both are
	// wall-clock and vary run to run, so they feed the cost report and never
	// a metric (the noise arm would flag them).
	IngestDuration   time.Duration `json:"-"`
	CurationDuration time.Duration `json:"-"`
}

// Seed builds a brain at dsn from the scenario corpus, or from its timeline.
//
// It reproduces the production ingest path (`mnemos process --embed`) offline
// and deterministically: rule-based extraction, ingest-time exact-text dedupe
// against what is already stored, within-batch relationship detection plus
// incremental detection against the existing store, then event and claim
// embeddings. Documents are ingested one at a time, in order, because that is
// how a brain actually accumulates — one capture session at a time — and it is
// the only way cross-session near-duplicates arise. Ingesting the corpus as a
// single batch would hand the ingest deduper the redundancy that consolidation
// is meant to be evaluated on.
//
// No LLM and no network: extraction is rule-based and embeddings come from the
// deterministic stub. The report says so; results here say nothing about the
// LLM extraction path.
func Seed(ctx context.Context, dsn string, sc Scenario) (SeedStats, error) {
	conn, err := store.Open(ctx, dsn)
	if err != nil {
		return SeedStats{}, fmt.Errorf("brainbench: open seed store: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if conn.Embeddings == nil {
		return SeedStats{}, fmt.Errorf("brainbench: store %q has no embeddings repository; "+
			"the dedupe stage of consolidation would be unmeasurable", dsn)
	}

	s := seeder{conn: conn, scenario: sc.ID, engine: extract.NewEngine(), relator: relate.NewEngine()}
	if len(sc.Timeline) > 0 {
		return s.timeline(ctx, sc.Timeline)
	}

	// A single wall-clock anchor for the whole seed. Deriving each document's
	// timestamp from a per-document time.Now() would make two arms seeded in
	// separate runs differ by milliseconds for no reason; anchoring once keeps
	// the seed a pure function of the scenario plus one instant.
	now := time.Now().UTC()
	for _, doc := range sc.Corpus {
		// IngestedAt is the transaction time and is genuinely "now" even for
		// a back-dated document — back-dating models a fact observed long
		// ago, not a row written long ago.
		if err := s.ingest(ctx, doc.ID, doc.Source, doc.Text, now.AddDate(0, 0, -doc.AgeDays), now); err != nil {
			return s.stats, err
		}
	}
	return s.stats, nil
}

// seeder ingests documents into one store and accumulates SeedStats.
type seeder struct {
	conn     *store.Conn
	scenario string
	engine   extract.Engine
	relator  relate.Engine
	stats    SeedStats
}

// ingest runs one document through the production ingest path.
// observedAt is the event's valid time; ingestedAt its transaction time.
func (s *seeder) ingest(ctx context.Context, docID, source, text string, observedAt, ingestedAt time.Time) error {
	began := time.Now()
	defer func() { s.stats.IngestDuration += time.Since(began) }()
	// domain.Event requires a source input id. Defaulting it to the doc id
	// keeps `source` an optional, human-facing label in scenario YAML instead
	// of a mandatory field every author must remember to repeat.
	if source == "" {
		source = docID
	}
	ev := domain.Event{
		ID:            deterministicEventID(s.scenario, docID),
		RunID:         s.scenario,
		SchemaVersion: "1.0",
		Content:       text,
		SourceInputID: source,
		Timestamp:     observedAt,
		IngestedAt:    ingestedAt,
	}

	claims, links, err := s.engine.Extract(ctx, []domain.Event{ev})
	if err != nil {
		return fmt.Errorf("brainbench: extract %s/%s: %w", s.scenario, docID, err)
	}

	existing, err := s.conn.Claims.ListAll(ctx)
	if err != nil {
		return fmt.Errorf("brainbench: list existing claims: %w", err)
	}

	before := len(claims)
	claims, links = pipeline.DedupeAgainstExisting(claims, links, existing)
	s.stats.DedupedAtIngest += before - len(claims)

	rels, err := s.relator.Detect(claims)
	if err != nil {
		return fmt.Errorf("brainbench: relate %s/%s: %w", s.scenario, docID, err)
	}
	if len(existing) > 0 && len(claims) > 0 {
		inc, err := s.relator.DetectIncremental(claims, existing)
		if err != nil {
			return fmt.Errorf("brainbench: incremental relate %s/%s: %w", s.scenario, docID, err)
		}
		rels = append(rels, inc...)
	}

	// Stamp a single attributed actor so nothing in the seeded brain is
	// attributed to the ambient OS user — that would make the seed depend on
	// who ran it.
	for i := range claims {
		claims[i].CreatedBy = SeedActor
	}
	for i := range rels {
		rels[i].CreatedBy = SeedActor
	}
	ev.CreatedBy = SeedActor

	if err := pipeline.PersistArtifacts(ctx, s.conn, []domain.Event{ev}, claims, links, rels); err != nil {
		return fmt.Errorf("brainbench: persist %s/%s: %w", s.scenario, docID, err)
	}

	if err := s.conn.Embeddings.Upsert(ctx, ev.ID, "event", EmbedText(ev.Content), StubEmbedderModel, ""); err != nil {
		return fmt.Errorf("brainbench: embed event %s: %w", ev.ID, err)
	}
	s.stats.Embeddings++
	for _, c := range claims {
		if err := s.conn.Embeddings.Upsert(ctx, c.ID, "claim", EmbedText(c.Text), StubEmbedderModel, ""); err != nil {
			return fmt.Errorf("brainbench: embed claim %s: %w", c.ID, err)
		}
		s.stats.Embeddings++
	}

	s.stats.Events++
	s.stats.Claims += len(claims)
	s.stats.Relationships += len(rels)
	return nil
}

// SeedActor attributes every seeded write, so a seed does not vary with the OS
// user running the harness.
const SeedActor = "brainbench"

// deterministicEventID derives a stable event id from the scenario and document
// ids. Stable ids keep a re-seeded brain comparable to an earlier one; a random
// or timestamp-derived id would make every run's stored rows differ for reasons
// unrelated to the experiment.
func deterministicEventID(scenarioID, docID string) string {
	sum := sha256.Sum256([]byte(scenarioID + "\x00" + docID))
	return "ev_bb_" + hex.EncodeToString(sum[:8])
}
