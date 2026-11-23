// Package taskstate is the gated task-state working-memory block (ADR 0050,
// issue #447) — an EXPERIMENT, off unless MNEMOS_TASK_STATE is truthy.
//
// Mnemos's working-memory blocks are ungated: anything appended goes in, and
// the oldest lines fall out when the 2,000-character budget overflows. A
// task-state block differs in three ways:
//
//   - Structure: it holds typed items (goals, constraints, pending commitments,
//     active hypotheses with the claim ids they rest on, open questions), one
//     per line, so it can be parsed back, gated and handed off.
//   - Gating in: an item is admitted only when it is new to long-term memory,
//     relevant to a current goal, or surprising against an existing claim
//     (see [Gate]). Goals are always admitted: they are what relevance is
//     measured against.
//   - Placement and eviction: goals and constraints are rendered first (the
//     start of the context block, where long-context models attend best), and
//     the budget evicts the lowest-priority kind first — open questions, then
//     hypotheses, then commitments — so a constraint stated early survives a
//     long session that would push it out of a FIFO block.
//
// At session end the block is handed to consolidation ([BuildHandoff]) as
// CANDIDATE claims anchored to one episode that records the block verbatim —
// never straight in as beliefs.
package taskstate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/relate"
)

// Kind is the type of a task-state item.
type Kind string

// The item kinds, in placement (and inverse eviction) priority order.
const (
	KindGoal       Kind = "goal"
	KindConstraint Kind = "constraint"
	KindCommitment Kind = "commitment"
	KindHypothesis Kind = "hypothesis"
	KindQuestion   Kind = "question"
)

// kindOrder is the placement order; eviction walks it backwards.
var kindOrder = []Kind{KindGoal, KindConstraint, KindCommitment, KindHypothesis, KindQuestion}

func (k Kind) rank() int {
	for i, o := range kindOrder {
		if o == k {
			return i
		}
	}
	return -1
}

// Valid reports whether k is a known kind.
func (k Kind) Valid() bool { return k.rank() >= 0 }

// Header opens every rendered task-state block. Parse refuses content without
// it, so a free-text block can never be mistaken for (and handed off as) one.
const Header = "# task state"

// MaxItemText bounds one item so a single item can never consume the block.
const MaxItemText = 300

// DefaultOwner is the owner the Claude Code hooks hand off at session end; MCP
// callers that omit an owner write under it.
const DefaultOwner = "claude-code"

const (
	labelPrefix    = "task_state:"
	defaultSession = "current"
)

// Label is the working-memory block label holding session's task state.
func Label(session string) string {
	session = strings.TrimSpace(session)
	if session == "" {
		session = defaultSession
	}
	return labelPrefix + session
}

// SessionOf returns the session a task-state label belongs to.
func SessionOf(label string) (string, bool) {
	s, ok := strings.CutPrefix(label, labelPrefix)
	return s, ok && s != ""
}

// Item is one entry of the task state.
type Item struct {
	Kind Kind
	Text string
	// RestsOn lists the claim ids a hypothesis rests on.
	RestsOn []string
	// Against lists the claim ids the gate found this item contradicts.
	Against []string
}

// State is a session's task state, in admission order.
type State struct {
	Items []Item
}

var (
	// ErrInvalidItem is returned for an item with an unknown kind, no text, or
	// text over [MaxItemText].
	ErrInvalidItem = errors.New("taskstate: invalid item")
	// ErrUnknownClaim is returned when an item references a claim that does
	// not exist.
	ErrUnknownClaim = errors.New("taskstate: unknown claim id")
)

var claimIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`)

// normalize collapses whitespace so an item is always one line.
func normalize(it Item) Item {
	it.Kind = Kind(strings.ToLower(strings.TrimSpace(string(it.Kind))))
	it.Text = strings.Join(strings.Fields(it.Text), " ")
	return it
}

func validate(it Item) error {
	if !it.Kind.Valid() {
		return fmt.Errorf("%w: kind %q (want goal, constraint, commitment, hypothesis or question)", ErrInvalidItem, it.Kind)
	}
	if it.Text == "" {
		return fmt.Errorf("%w: text is required", ErrInvalidItem)
	}
	if len(it.Text) > MaxItemText {
		return fmt.Errorf("%w: text is %d characters, max %d", ErrInvalidItem, len(it.Text), MaxItemText)
	}
	for _, id := range append(append([]string{}, it.RestsOn...), it.Against...) {
		if !claimIDPattern.MatchString(id) {
			return fmt.Errorf("%w: claim id %q", ErrInvalidItem, id)
		}
	}
	return nil
}

// dedupKey is the identity of an item's statement: lowercased alphanumerics.
func dedupKey(text string) string {
	var b strings.Builder
	space := true
	for _, r := range strings.ToLower(text) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			space = false
		} else if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

// ordered returns the items in placement order, stable within a kind.
func ordered(s State) []Item {
	out := make([]Item, 0, len(s.Items))
	for _, k := range kindOrder {
		for _, it := range s.Items {
			if it.Kind == k {
				out = append(out, it)
			}
		}
	}
	return out
}

// Render writes the block: the header, then one line per item in placement
// order — goals and constraints first.
func Render(s State) string {
	var b strings.Builder
	b.WriteString(Header)
	for _, it := range ordered(s) {
		b.WriteString("\n- [")
		b.WriteString(string(it.Kind))
		if len(it.RestsOn) > 0 {
			b.WriteString(" rests_on=" + strings.Join(it.RestsOn, ","))
		}
		if len(it.Against) > 0 {
			b.WriteString(" against=" + strings.Join(it.Against, ","))
		}
		b.WriteString("] ")
		b.WriteString(it.Text)
	}
	return b.String()
}

// Parse reads a rendered block back. Empty content is an empty state.
func Parse(content string) (State, error) {
	if strings.TrimSpace(content) == "" {
		return State{}, nil
	}
	lines := strings.Split(content, "\n")
	if strings.TrimSpace(lines[0]) != Header {
		return State{}, fmt.Errorf("taskstate: block does not start with %q", Header)
	}
	var s State
	for n, line := range lines[1:] {
		it, err := parseLine(line)
		if err != nil {
			return State{}, fmt.Errorf("taskstate: line %d: %w", n+2, err)
		}
		s.Items = append(s.Items, it)
	}
	return s, nil
}

func parseLine(line string) (Item, error) {
	rest, ok := strings.CutPrefix(line, "- [")
	if !ok {
		return Item{}, fmt.Errorf("%w: %q is not an item line", ErrInvalidItem, line)
	}
	meta, text, ok := strings.Cut(rest, "] ")
	if !ok {
		return Item{}, fmt.Errorf("%w: %q has no closing bracket", ErrInvalidItem, line)
	}
	fields := strings.Fields(meta)
	if len(fields) == 0 {
		return Item{}, fmt.Errorf("%w: %q has no kind", ErrInvalidItem, line)
	}
	it := Item{Kind: Kind(fields[0]), Text: text}
	for _, f := range fields[1:] {
		key, val, _ := strings.Cut(f, "=")
		switch key {
		case "rests_on":
			it.RestsOn = strings.Split(val, ",")
		case "against":
			it.Against = strings.Split(val, ",")
		default:
			return Item{}, fmt.Errorf("%w: unknown attribute %q", ErrInvalidItem, key)
		}
	}
	it = normalize(it)
	return it, validate(it)
}

// Eviction selects which item leaves when the block overflows its budget.
type Eviction int

const (
	// EvictByPriority drops the oldest item of the lowest-priority kind
	// present: questions, then hypotheses, commitments, constraints, goals.
	EvictByPriority Eviction = iota
	// EvictOldest drops the oldest item regardless of kind — the FIFO policy
	// of ordinary blocks, kept so the experiment can ablate eviction alone.
	EvictOldest
)

// Fit evicts items until the rendered block fits limit, returning the kept
// state and the evicted items in eviction order.
func Fit(s State, limit int, policy Eviction) (State, []Item) {
	items := append([]Item(nil), s.Items...)
	var evicted []Item
	for len(items) > 0 && len(Render(State{Items: items})) > limit {
		victim := 0
		if policy == EvictByPriority {
			for i, it := range items {
				if it.Kind.rank() > items[victim].Kind.rank() {
					victim = i
				}
			}
		}
		evicted = append(evicted, items[victim])
		items = append(items[:victim], items[victim+1:]...)
	}
	return State{Items: items}, evicted
}

// Verdict is how a statement relates to long-term memory.
type Verdict struct {
	// KnownAs is the id of a stored claim the statement restates; "" when it
	// is new to long-term memory.
	KnownAs string
	// Contradicts lists stored claims the statement contradicts.
	Contradicts []string
}

// Probe answers the gate's long-term-memory questions.
type Probe interface {
	// Assess reports whether text is already known and what it contradicts.
	Assess(ctx context.Context, text string) (Verdict, error)
	// Missing returns the ids among ids that name no stored claim.
	Missing(ctx context.Context, ids []string) ([]string, error)
}

// Reason explains a gate decision.
type Reason string

// Admission and rejection reasons.
const (
	ReasonGoal       Reason = "goal"
	ReasonNovel      Reason = "new_to_long_term_memory"
	ReasonRelevant   Reason = "relevant_to_goal"
	ReasonSurprising Reason = "surprising_against_claim"
	ReasonDuplicate  Reason = "already_held"
	ReasonKnown      Reason = "known_irrelevant_unsurprising"
)

// Decision is the gate's verdict on one item.
type Decision struct {
	Admit   bool
	Reasons []Reason
	// KnownAs is the stored claim the item restates, when it does.
	KnownAs string
	// Against lists the claims the item is surprising against.
	Against []string
}

// Has reports whether r is among the decision's reasons.
func (d Decision) Has(r Reason) bool {
	for _, x := range d.Reasons {
		if x == r {
			return true
		}
	}
	return false
}

// Gate decides whether it may enter s. It normalizes and validates it first;
// an invalid item or a reference to a claim that does not exist is an error,
// not a rejection.
//
// The #446 expectation ledger is the intended second source of surprise: an
// item that contradicts an attributed prediction error. Until it lands the
// gate reads surprise from the existing contradiction detector only.
func Gate(ctx context.Context, s State, it Item, p Probe) (Decision, error) {
	it = normalize(it)
	if err := validate(it); err != nil {
		return Decision{}, err
	}
	key := dedupKey(it.Text)
	for _, held := range s.Items {
		if dedupKey(held.Text) == key {
			return Decision{Reasons: []Reason{ReasonDuplicate}}, nil
		}
	}
	if len(it.RestsOn) > 0 {
		missing, err := p.Missing(ctx, it.RestsOn)
		if err != nil {
			return Decision{}, fmt.Errorf("taskstate: check claim ids: %w", err)
		}
		if len(missing) > 0 {
			return Decision{}, fmt.Errorf("%w: %s", ErrUnknownClaim, strings.Join(missing, ", "))
		}
	}
	if it.Kind == KindGoal {
		return Decision{Admit: true, Reasons: []Reason{ReasonGoal}}, nil
	}
	v, err := p.Assess(ctx, it.Text)
	if err != nil {
		return Decision{}, fmt.Errorf("taskstate: assess against long-term memory: %w", err)
	}
	d := Decision{KnownAs: v.KnownAs, Against: v.Contradicts}
	if v.KnownAs == "" {
		d.Reasons = append(d.Reasons, ReasonNovel)
	}
	for _, held := range s.Items {
		if held.Kind == KindGoal && relate.SharesTopic(it.Text, held.Text) {
			d.Reasons = append(d.Reasons, ReasonRelevant)
			break
		}
	}
	if len(v.Contradicts) > 0 {
		d.Reasons = append(d.Reasons, ReasonSurprising)
	}
	d.Admit = len(d.Reasons) > 0
	if !d.Admit {
		d.Reasons = []Reason{ReasonKnown}
	}
	return d, nil
}

// Admit gates it and, when admitted, appends it to s and fits the result to
// limit. It returns the new state, the decision and anything evicted.
func Admit(ctx context.Context, s State, it Item, p Probe, limit int, policy Eviction) (State, Decision, []Item, error) {
	d, err := Gate(ctx, s, it, p)
	if err != nil || !d.Admit {
		return s, d, nil, err
	}
	it = normalize(it)
	it.Against = d.Against
	next := State{Items: append(append([]Item(nil), s.Items...), it)}
	kept, evicted := Fit(next, limit, policy)
	return kept, d, evicted, nil
}

// Remove drops the item whose statement matches text (resolved question,
// kept commitment). It reports whether anything was removed.
func Remove(s State, text string) (State, bool) {
	key := dedupKey(text)
	for i, it := range s.Items {
		if dedupKey(it.Text) == key {
			items := append(append([]Item(nil), s.Items[:i]...), s.Items[i+1:]...)
			return State{Items: items}, true
		}
	}
	return s, false
}

// HandoffRunID tags every handoff episode.
const HandoffRunID = "task-state"

// candidateConfidence is the prior given to a handed-off item. It is a working
// statement, not evidence-backed knowledge: consolidation and review decide
// whether it earns more.
const candidateConfidence = 0.5

// Handoff is a session's task state as consolidation input: one episode
// recording the block verbatim, and a candidate claim per assertion.
type Handoff struct {
	Event         domain.Event
	Claims        []domain.Claim
	Links         []domain.ClaimEvidence
	Relationships []domain.Relationship
}

// BuildHandoff converts s into a [Handoff]. Open questions are not assertions,
// so they produce no claim; the episode keeps them. IDs derive from owner,
// session and content, so a retried handoff rewrites the same rows. ok is false
// when there is nothing to hand off.
func BuildHandoff(owner, session string, s State, now time.Time, actor string) (Handoff, bool) {
	if len(s.Items) == 0 {
		return Handoff{}, false
	}
	content := Render(s)
	ev := domain.Event{
		ID:            "ev_ts_" + digest(owner, session, content),
		RunID:         HandoffRunID,
		Content:       content,
		SourceInputID: Label(session),
		Timestamp:     now,
		IngestedAt:    now,
		CreatedBy:     actor,
		Metadata:      map[string]string{"source": "task_state", "owner": owner, "session": session},
	}
	h := Handoff{Event: ev}
	for _, it := range ordered(s) {
		typ, text, ok := claimFor(it)
		if !ok {
			continue
		}
		id := "ts_" + digest(owner, session, string(it.Kind), dedupKey(it.Text))
		h.Claims = append(h.Claims, domain.Claim{
			ID: id, Text: text, Type: typ, Confidence: candidateConfidence,
			Status: domain.ClaimStatusActive, Lifecycle: domain.ClaimLifecycleCandidate,
			CreatedAt: now, ValidFrom: now, CreatedBy: actor,
		})
		h.Links = append(h.Links, domain.ClaimEvidence{ClaimID: id, EventID: ev.ID})
		h.Relationships = append(h.Relationships, edges(id, domain.RelationshipTypeDerivedFrom, it.RestsOn, now, actor)...)
		h.Relationships = append(h.Relationships, edges(id, domain.RelationshipTypeContradicts, it.Against, now, actor)...)
	}
	return h, true
}

func claimFor(it Item) (domain.ClaimType, string, bool) {
	switch it.Kind {
	case KindGoal:
		return domain.ClaimTypeDecision, "Goal: " + it.Text, true
	case KindConstraint:
		return domain.ClaimTypeFact, "Constraint: " + it.Text, true
	case KindCommitment:
		return domain.ClaimTypeDecision, "Commitment: " + it.Text, true
	case KindHypothesis:
		return domain.ClaimTypeHypothesis, it.Text, true
	default:
		return "", "", false
	}
}

func edges(from string, typ domain.RelationshipType, to []string, now time.Time, actor string) []domain.Relationship {
	out := make([]domain.Relationship, 0, len(to))
	for _, t := range to {
		if t == from {
			continue
		}
		out = append(out, domain.Relationship{
			ID: "rel_ts_" + digest(from, string(typ), t), Type: typ,
			FromClaimID: from, ToClaimID: t, CreatedAt: now, CreatedBy: actor,
		})
	}
	return out
}

func digest(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:12])
}
