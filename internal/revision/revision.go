// Package revision decides what a surprise does to the beliefs an agent acted
// on (ADR 0049).
//
// An agent records a decision, names the beliefs it rested on, and states what
// outcome it expects. When the observed outcome falls outside the expected
// band, the surprise is evidence against the CONJUNCTION of those beliefs, not
// against each one: a deploy that failed while resting on "the timeout is 30s"
// and "retries are idempotent" says at least one of them is wrong, not which.
// So a surprise never retires a belief by itself. It opens the beliefs to
// revision, and evidence decides:
//
//   - A rested-on belief that a NEWER live belief contradicts is superseded by
//     it: its valid time ends. The surprise is the tie-breaker the contradiction
//     lacked, and the newer belief is the evidence. The surprise is then
//     explained.
//   - A surprise one of whose beliefs is already retired (ended, archived) is
//     explained too: the premise set it refuted no longer stands.
//   - Otherwise the surprise is unexplained and every rested-on belief is
//     flagged: active → contested. Contested is
//     the lifecycle's existing state for a live disagreement that is kept and
//     shown, never hidden (internal/query/admission.go).
//   - When a surprise is explained, the flags it set earlier on the other
//     beliefs it rested on are cleared (contested → active), unless another
//     unexplained surprise still rests on them. One cause explains the error;
//     its partners are exonerated ("explaining away").
//
// What is never revised: a belief rested on by a decision whose outcome was
// confirmed, a belief that was only recalled, a deprecated belief, and a
// contested belief this package did not flag. A promoted (human-endorsed)
// belief is flagged but never superseded, for the same reason ForgetRefuted
// exempts it.
//
// Plan is pure and deterministic: the same inputs give the same steps in the
// same order, whatever order the store returned rows in, and re-planning after
// the steps were applied yields no further steps for the same surprises.
package revision

import (
	"sort"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// ReasonPrefix starts the status-history reason of every transition this
// package causes. It is how a later pass recognises a flag it set (and so may
// clear it) and how `recompute-contested`, which clears contested statuses its
// heuristic would not assign, knows to leave a surprise flag alone.
const ReasonPrefix = "surprise-revision:"

// StepKind names one revision.
type StepKind string

// Revision step kinds.
const (
	// StepFlag moves an active belief to contested.
	StepFlag StepKind = "flag"
	// StepSupersede ends the belief's valid time; By is the newer belief that
	// contradicts it.
	StepSupersede StepKind = "supersede"
	// StepClear returns a belief this package flagged to active.
	StepClear StepKind = "clear"
)

// Surprise is one reconciled decision expectation whose observation fell
// outside its tolerance.
type Surprise struct {
	DecisionID string
	RestsOn    []string
}

// Belief is the slice of a claim's state Plan reads.
type Belief struct {
	ID     string
	Status domain.ClaimStatus
	// Current is domain.Claim.IsCurrent: neither ended nor archived.
	Current  bool
	Promoted bool
	// FlaggedBySurprise reports that the belief's latest status transition
	// was a StepFlag (its reason starts with ReasonPrefix).
	FlaggedBySurprise bool
	CreatedAt         time.Time
	ValidFrom         time.Time
}

// Inputs is everything a plan depends on.
type Inputs struct {
	// Surprises are the refuted decision expectations, all of them, not only
	// the ones reconciled in this pass: re-planning is how a surprise that had
	// no contrary evidence when it happened is revised when evidence arrives.
	Surprises []Surprise
	// Beliefs holds every rested-on belief and every belief that contradicts
	// one. A rested-on id missing here is skipped.
	Beliefs map[string]Belief
	// Contradicts maps a claim id to the ids of the claims it has a live
	// contradicts relationship with, in either direction.
	Contradicts map[string][]string
}

// Step is one revision to apply.
type Step struct {
	ClaimID string
	Kind    StepKind
	// By is the superseding belief, for StepSupersede.
	By string
	// DecisionIDs are the surprised decisions behind the step, sorted.
	DecisionIDs []string
}

// Result is a plan.
type Result struct {
	// Steps are sorted by claim id; one per claim at most.
	Steps []Step
	// ExplainedBy maps each explained decision to the rested-on belief whose
	// supersession explained it (the lowest id when several did).
	ExplainedBy map[string]string
}

// Plan computes the revisions the surprises call for.
func Plan(in Inputs) Result {
	surprises := normalise(in.Surprises)
	res := Result{ExplainedBy: map[string]string{}}

	supersededBy := map[string]string{}
	steps := map[string]*Step{}
	addDecision := func(s *Step, d string) {
		for _, have := range s.DecisionIDs {
			if have == d {
				return
			}
		}
		s.DecisionIDs = append(s.DecisionIDs, d)
	}

	// 1. Re-check each rested-on belief against newer contradicting evidence.
	for _, s := range surprises {
		partners := toSet(s.RestsOn)
		for _, id := range s.RestsOn {
			b, ok := in.Beliefs[id]
			if !ok || !revisable(b) || b.Promoted {
				continue
			}
			by, ok := supersederFor(b, in, partners)
			if !ok {
				continue
			}
			supersededBy[id] = by
			step := steps[id]
			if step == nil {
				step = &Step{ClaimID: id, Kind: StepSupersede, By: by}
				steps[id] = step
			}
			addDecision(step, s.DecisionID)
			if _, done := res.ExplainedBy[s.DecisionID]; !done {
				res.ExplainedBy[s.DecisionID] = id
			}
		}
	}

	// A surprise one of whose beliefs is already retired stays explained: the
	// premise set it refuted no longer stands. This is also what keeps a plan
	// idempotent, since applying a supersession retires the belief that
	// explained the surprise.
	for _, s := range surprises {
		if _, done := res.ExplainedBy[s.DecisionID]; done {
			continue
		}
		for _, id := range s.RestsOn {
			if b, ok := in.Beliefs[id]; ok && !b.Current {
				res.ExplainedBy[s.DecisionID] = id
				break
			}
		}
	}

	// 2. An unexplained surprise flags the active beliefs it rested on; a
	// belief rested on by unexplained surprises keeps its flag.
	unexplainedOn := map[string][]string{}
	explainedOn := map[string][]string{}
	for _, s := range surprises {
		_, explained := res.ExplainedBy[s.DecisionID]
		for _, id := range s.RestsOn {
			if explained {
				explainedOn[id] = append(explainedOn[id], s.DecisionID)
			} else {
				unexplainedOn[id] = append(unexplainedOn[id], s.DecisionID)
			}
		}
	}
	for id, ds := range unexplainedOn {
		b, ok := in.Beliefs[id]
		if !ok || !revisable(b) || b.Status != domain.ClaimStatusActive {
			continue
		}
		if _, gone := supersededBy[id]; gone {
			continue
		}
		step := &Step{ClaimID: id, Kind: StepFlag}
		for _, d := range ds {
			addDecision(step, d)
		}
		steps[id] = step
	}

	// 3. Explaining away: a flag this package set is cleared once every
	// surprise resting on the belief is explained.
	for id, ds := range explainedOn {
		if len(unexplainedOn[id]) > 0 {
			continue
		}
		if _, gone := supersededBy[id]; gone {
			continue
		}
		b, ok := in.Beliefs[id]
		if !ok || !b.Current || !b.FlaggedBySurprise || b.Status != domain.ClaimStatusContested {
			continue
		}
		step := &Step{ClaimID: id, Kind: StepClear}
		for _, d := range ds {
			addDecision(step, d)
		}
		steps[id] = step
	}

	for _, s := range steps {
		sort.Strings(s.DecisionIDs)
		res.Steps = append(res.Steps, *s)
	}
	sort.Slice(res.Steps, func(i, j int) bool { return res.Steps[i].ClaimID < res.Steps[j].ClaimID })
	return res
}

// revisable reports whether a belief can be revised at all: it is still
// current and was not deliberately retired.
func revisable(b Belief) bool {
	return b.Current && b.Status != domain.ClaimStatusDeprecated
}

// supersederFor returns the newest current belief that contradicts b, was
// created after it, and is not one of the beliefs the same decision rested on
// (two partners in one surprise cannot decide against each other: the surprise
// says nothing about which of them is wrong). Ties on creation time go to the
// lowest id.
func supersederFor(b Belief, in Inputs, partners map[string]bool) (string, bool) {
	best, bestAt := "", time.Time{}
	for _, cid := range in.Contradicts[b.ID] {
		if partners[cid] || cid == b.ID {
			continue
		}
		c, ok := in.Beliefs[cid]
		if !ok || !revisable(c) || !c.CreatedAt.After(b.CreatedAt) {
			continue
		}
		if best == "" || c.CreatedAt.After(bestAt) || (c.CreatedAt.Equal(bestAt) && cid < best) {
			best, bestAt = cid, c.CreatedAt
		}
	}
	return best, best != ""
}

// normalise sorts surprises by decision id and each RestsOn list, dropping
// duplicates, so Plan cannot depend on row order.
func normalise(in []Surprise) []Surprise {
	out := make([]Surprise, 0, len(in))
	for _, s := range in {
		ids := make([]string, 0, len(s.RestsOn))
		seen := map[string]bool{}
		for _, id := range s.RestsOn {
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			ids = append(ids, id)
		}
		sort.Strings(ids)
		out = append(out, Surprise{DecisionID: s.DecisionID, RestsOn: ids})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DecisionID < out[j].DecisionID })
	return out
}

func toSet(ids []string) map[string]bool {
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out
}
