package taskstate

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/pipeline"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/relate"
	"go.klarlabs.de/mnemos/internal/store"
)

// nearDuplicate is the content-token Jaccard above which a stored claim counts
// as already holding a statement. Exact restatements are caught first by the
// indexed text-key lookup; this catches reworded ones.
const nearDuplicate = 0.8

// probeID names the transient claim the probe relates against the store. It is
// never written.
const probeID = "taskstate-probe"

// StoreProbe answers [Probe] from a store, read-only. It uses the write path's
// own machinery: the indexed text-key duplicate check, the relate candidate
// index, and the incremental contradiction detector — so "known" and
// "surprising" mean exactly what they mean at ingest.
type StoreProbe struct {
	Conn *store.Conn
}

// Assess implements [Probe].
func (p StoreProbe) Assess(ctx context.Context, text string) (Verdict, error) {
	exact, err := p.Conn.Claims.ListLiveByTextKeys(ctx, []string{domain.ClaimTextKey(text)})
	if err != nil {
		return Verdict{}, fmt.Errorf("text-key lookup: %w", err)
	}
	probe := domain.Claim{
		ID: probeID, Text: text, Type: domain.ClaimTypeFact, Confidence: 1,
		Status: domain.ClaimStatusActive,
	}
	candidates, err := pipeline.ExistingForRelate(ctx, p.Conn, []domain.Claim{probe})
	if err != nil {
		return Verdict{}, fmt.Errorf("relate candidates: %w", err)
	}
	live := candidates[:0:0]
	for _, c := range candidates {
		if c.Status != domain.ClaimStatusDeprecated && c.ValidTo.IsZero() {
			live = append(live, c)
		}
	}
	var v Verdict
	rels, err := relate.NewEngine().DetectIncremental([]domain.Claim{probe}, live)
	if err != nil {
		return Verdict{}, fmt.Errorf("contradiction detection: %w", err)
	}
	seen := map[string]bool{}
	for _, r := range rels {
		if r.Type != domain.RelationshipTypeContradicts {
			continue
		}
		other := r.ToClaimID
		if other == probeID {
			other = r.FromClaimID
		}
		if other != probeID && !seen[other] {
			seen[other] = true
			v.Contradicts = append(v.Contradicts, other)
		}
	}
	sort.Strings(v.Contradicts)
	// Content tokens drop negation, so a contradiction looks like a perfect
	// near-duplicate; a statement that contradicts a claim does not restate it.
	if len(exact) > 0 {
		v.KnownAs = exact[0].ID
	} else {
		agreeing := live[:0:0]
		for _, c := range live {
			if !seen[c.ID] {
				agreeing = append(agreeing, c)
			}
		}
		v.KnownAs = nearestDuplicate(text, agreeing)
	}
	return v, nil
}

func nearestDuplicate(text string, claims []domain.Claim) string {
	a := relate.ContentTokens(text)
	if len(a) == 0 {
		return ""
	}
	best, bestID := 0.0, ""
	for _, c := range claims {
		b := relate.ContentTokens(c.Text)
		inter := 0
		for t := range a {
			if _, ok := b[t]; ok {
				inter++
			}
		}
		union := len(a) + len(b) - inter
		if union == 0 {
			continue
		}
		if j := float64(inter) / float64(union); j >= nearDuplicate && j > best {
			best, bestID = j, c.ID
		}
	}
	return bestID
}

// Missing implements [Probe].
func (p StoreProbe) Missing(ctx context.Context, ids []string) ([]string, error) {
	found, err := p.Conn.Claims.ListByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	have := make(map[string]bool, len(found))
	for _, c := range found {
		have[c.ID] = true
	}
	var missing []string
	for _, id := range ids {
		if !have[id] {
			missing = append(missing, id)
		}
	}
	return missing, nil
}

// ErrUnsupported is returned when the backend has no working-memory block store.
var ErrUnsupported = errors.New("taskstate: working-memory blocks not supported on this backend")

// Service reads and writes task-state blocks through a block repository.
type Service struct {
	Blocks ports.BlockRepository
	Probe  Probe
	// Limit is the block's character budget.
	Limit int
	// Eviction is the overflow policy; the zero value is [EvictByPriority].
	Eviction Eviction
	// Now stamps writes; nil means time.Now.
	Now func() time.Time
}

// AdmitResult reports one admission.
type AdmitResult struct {
	Decision Decision
	Evicted  []Item
	State    State
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// Load returns owner's task state for session (empty when none is held).
func (s Service) Load(ctx context.Context, owner, session string) (State, error) {
	if s.Blocks == nil {
		return State{}, ErrUnsupported
	}
	b, ok, err := s.Blocks.Get(ctx, owner, Label(session))
	if err != nil || !ok {
		return State{}, err
	}
	return Parse(b.Content)
}

func (s Service) save(ctx context.Context, owner, session string, st State) error {
	if len(st.Items) == 0 {
		return s.Blocks.Delete(ctx, owner, Label(session))
	}
	return s.Blocks.Upsert(ctx, domain.WorkingMemoryBlock{
		Owner: owner, Label: Label(session), Content: Render(st), UpdatedAt: s.now(),
	})
}

// Admit gates it into owner's session task state and persists the result.
func (s Service) Admit(ctx context.Context, owner, session string, it Item) (AdmitResult, error) {
	st, err := s.Load(ctx, owner, session)
	if err != nil {
		return AdmitResult{}, err
	}
	next, d, evicted, err := Admit(ctx, st, it, s.Probe, s.Limit, s.Eviction)
	if err != nil {
		return AdmitResult{}, err
	}
	if d.Admit {
		if err := s.save(ctx, owner, session, next); err != nil {
			return AdmitResult{}, err
		}
	}
	return AdmitResult{Decision: d, Evicted: evicted, State: next}, nil
}

// Remove drops the item matching text and persists the result.
func (s Service) Remove(ctx context.Context, owner, session, text string) (bool, error) {
	st, err := s.Load(ctx, owner, session)
	if err != nil {
		return false, err
	}
	next, removed := Remove(st, text)
	if !removed {
		return false, nil
	}
	return true, s.save(ctx, owner, session, next)
}

// Clear deletes the session's block, after a handoff.
func (s Service) Clear(ctx context.Context, owner, session string) error {
	if s.Blocks == nil {
		return ErrUnsupported
	}
	return s.Blocks.Delete(ctx, owner, Label(session))
}
