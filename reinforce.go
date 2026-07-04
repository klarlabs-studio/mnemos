package mnemos

import (
	"context"
	"fmt"
	"math"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// ReinforcementRetention is the weight kept on a playbook's synthesized
// (derivation-time) confidence when the sleep pass reinforces it with observed
// outcomes. The complement (1 - retention) is the weight given to the measured
// success rate of the outcomes on the actions the playbook's lessons are built
// from. 0.7 keeps the skill's learned prior dominant while letting reality bend
// it: a playbook whose underlying actions keep failing decays toward that
// reality; one whose actions keep succeeding is reinforced toward certainty.
const ReinforcementRetention = 0.7

// ReinforceMinOutcomes is the minimum number of scored outcomes (success /
// failure / partial — "unknown" carries no signal and is ignored) a playbook
// must accumulate across its evidence before its confidence is reinforced.
// Below this the success rate is too noisy to move a learned confidence, so the
// playbook is left untouched.
const ReinforceMinOutcomes = 2

// reinforcePlaybooks bends each playbook's confidence toward the observed
// success rate of the outcomes recorded against the actions its lessons were
// derived from — the skill-learning half of the sleep pass (T2.2). It is a
// deterministic, no-LLM update: synthesize writes a playbook's confidence from
// its lessons once; this closes the loop by letting real outcomes reinforce or
// decay that confidence over time, turning the skill store from write-only into
// self-tuning.
//
// Attribution note: the signal is corroboration-by-underlying-evidence — "the
// actions this playbook's lessons are built from succeeded X% of the time" — not
// decision-level attribution ("this playbook was consulted for decision D"). The
// latter would need an explicit decision→playbook link; the former reuses the
// links synthesize already writes and is a sound reinforcement signal for a
// playbook derived from those actions.
func (m *memory) reinforcePlaybooks(ctx context.Context) (int, error) {
	// Backends without the skill layer (or without outcomes) have nothing to
	// reinforce — best-effort, like the trust scorer.
	if m.conn.Playbooks == nil || m.conn.Lessons == nil || m.conn.Outcomes == nil {
		return 0, nil
	}
	playbooks, err := m.conn.Playbooks.ListAll(ctx)
	if err != nil {
		return 0, fmt.Errorf("list playbooks: %w", err)
	}
	now := time.Now().UTC()
	n := 0
	for _, p := range playbooks {
		rate, scored, err := m.playbookSuccessRate(ctx, p)
		if err != nil {
			return n, err
		}
		if scored < ReinforceMinOutcomes {
			continue // not enough observed reality to move a learned confidence
		}
		updated := clamp01(ReinforcementRetention*p.Confidence + (1-ReinforcementRetention)*rate)
		if math.Abs(updated-p.Confidence) < 1e-9 {
			continue // reality already agrees with the learned confidence
		}
		p.Confidence = updated
		p.LastVerified = now
		if err := m.conn.Playbooks.Append(ctx, p); err != nil {
			return n, fmt.Errorf("reinforce playbook %s: %w", p.ID, err)
		}
		n++
	}
	return n, nil
}

// playbookSuccessRate returns the weighted success rate in [0, 1] of the
// outcomes on the actions the playbook's lessons are built from, and the number
// of scored outcomes considered. success = 1, partial = 0.5, failure = 0;
// "unknown" outcomes are ignored (they carry no signal). Each distinct outcome
// is counted once even when two of the playbook's lessons share an action.
func (m *memory) playbookSuccessRate(ctx context.Context, p domain.Playbook) (rate float64, scored int, err error) {
	lessonIDs, err := m.conn.Playbooks.ListLessons(ctx, p.ID)
	if err != nil {
		return 0, 0, fmt.Errorf("list playbook lessons %s: %w", p.ID, err)
	}
	actionIDs := map[string]struct{}{}
	for _, lid := range lessonIDs {
		evidence, err := m.conn.Lessons.ListEvidence(ctx, lid)
		if err != nil {
			return 0, 0, fmt.Errorf("list lesson evidence %s: %w", lid, err)
		}
		for _, aid := range evidence {
			actionIDs[aid] = struct{}{}
		}
	}
	seen := map[string]struct{}{}
	var sum float64
	for aid := range actionIDs {
		outcomes, err := m.conn.Outcomes.ListByActionID(ctx, aid)
		if err != nil {
			return 0, 0, fmt.Errorf("list outcomes for action %s: %w", aid, err)
		}
		for _, o := range outcomes {
			if _, dup := seen[o.ID]; dup {
				continue
			}
			w, ok := outcomeWeight(o.Result)
			if !ok {
				continue // unknown — no signal
			}
			seen[o.ID] = struct{}{}
			sum += w
			scored++
		}
	}
	if scored == 0 {
		return 0, 0, nil
	}
	return sum / float64(scored), scored, nil
}

// outcomeWeight maps a coarse outcome verdict to a success weight in [0, 1].
// The bool is false for "unknown" (and any unrecognised value), which the caller
// treats as carrying no reinforcement signal.
func outcomeWeight(r domain.OutcomeResult) (float64, bool) {
	switch r {
	case domain.OutcomeResultSuccess:
		return 1, true
	case domain.OutcomeResultPartial:
		return 0.5, true
	case domain.OutcomeResultFailure:
		return 0, true
	default:
		return 0, false
	}
}

func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}
