package synthesize

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
)

// PlaybookOptions tunes the playbook synthesis pass. Zero values
// reproduce the project defaults.
type PlaybookOptions struct {
	// MinLessons is the smallest number of corroborating lessons a
	// trigger cluster must carry before emitting a playbook.
	// Defaults to domain.PlaybookMinLessons (1).
	MinLessons int
	// MinConfidence is the floor below which candidate playbooks are
	// dropped. Defaults to domain.PlaybookConfidenceMin (0.55).
	MinConfidence float64
	// Now overrides the wall clock for deterministic tests.
	Now func() time.Time
	// Source labels emitted playbooks. Defaults to "synthesize".
	Source string
	// CreatedBy stamps emitted playbooks. Defaults to domain.SystemUser.
	CreatedBy string
}

func (o PlaybookOptions) withDefaults() PlaybookOptions {
	if o.MinLessons == 0 {
		o.MinLessons = domain.PlaybookMinLessons
	}
	if o.MinConfidence == 0 {
		o.MinConfidence = domain.PlaybookConfidenceMin
	}
	if o.Now == nil {
		o.Now = func() time.Time { return time.Now().UTC() }
	}
	if o.Source == "" {
		o.Source = "synthesize"
	}
	if o.CreatedBy == "" {
		o.CreatedBy = domain.SystemUser
	}
	return o
}

// PlaybookResult reports counts from one synthesis pass.
type PlaybookResult struct {
	TriggerClusters  int
	PlaybooksEmitted int
	Skipped          int
	PlaybookIDs      []string
}

// Playbooks scans every Lesson, groups them by Trigger,
// and emits one Playbook per cluster. The playbook's confidence is
// the corroboration-weighted mean of the cluster's lesson
// confidences; the playbook's steps come from the dominant action
// kind across the cluster's evidence (rollback, restart, etc.).
//
// Idempotent: cluster id is sha256(scope, trigger), so re-running
// upserts the same playbook row identity-stably.
func Playbooks(ctx context.Context, lessons ports.LessonRepository, playbooks ports.PlaybookRepository, opts PlaybookOptions) (PlaybookResult, error) {
	opts = opts.withDefaults()

	allLessons, err := lessons.ListAll(ctx)
	if err != nil {
		return PlaybookResult{}, fmt.Errorf("list lessons: %w", err)
	}

	clusters := groupLessonsByTrigger(allLessons)
	res := PlaybookResult{TriggerClusters: len(clusters)}
	res.PlaybookIDs = make([]string, 0, len(clusters))
	now := opts.Now().UTC()

	for _, c := range clusters {
		if len(c.Lessons) < opts.MinLessons {
			res.Skipped++
			continue
		}
		score := scorePlaybookCluster(c)
		if score < opts.MinConfidence {
			res.Skipped++
			continue
		}
		pb := domain.Playbook{
			ID:                 playbookClusterID(c),
			Trigger:            c.Trigger,
			Statement:          fmt.Sprintf("Respond to %s", c.Trigger),
			Scope:              c.Scope,
			Steps:              composePlaybookSteps(c),
			DerivedFromLessons: lessonIDsFor(c.Lessons),
			Confidence:         score,
			DerivedAt:          now,
			LastVerified:       now,
			Source:             opts.Source,
			CreatedBy:          opts.CreatedBy,
		}
		if err := playbooks.Append(ctx, pb); err != nil {
			return res, fmt.Errorf("append playbook %s: %w", pb.ID, err)
		}
		res.PlaybookIDs = append(res.PlaybookIDs, pb.ID)
		res.PlaybooksEmitted++
	}
	return res, nil
}

type playbookCluster struct {
	Scope   domain.LessonScope
	Trigger string
	Lessons []domain.Lesson
}

func groupLessonsByTrigger(lessons []domain.Lesson) []playbookCluster {
	type key struct {
		Trigger string
		Service string
		Env     string
		Team    string
	}
	buckets := map[key]*playbookCluster{}
	for _, l := range lessons {
		if l.Trigger == "" {
			// Lessons without a trigger label cannot seed a
			// trigger-keyed playbook. Skip silently rather than
			// emit folkloric defaults.
			continue
		}
		k := key{Trigger: l.Trigger, Service: l.Scope.Service, Env: l.Scope.Env, Team: l.Scope.Team}
		c, exists := buckets[k]
		if !exists {
			c = &playbookCluster{Scope: l.Scope, Trigger: l.Trigger}
			buckets[k] = c
		}
		c.Lessons = append(c.Lessons, l)
	}
	out := make([]playbookCluster, 0, len(buckets))
	for _, c := range buckets {
		sort.Slice(c.Lessons, func(i, j int) bool { return c.Lessons[i].Confidence > c.Lessons[j].Confidence })
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Trigger != out[j].Trigger {
			return out[i].Trigger < out[j].Trigger
		}
		return out[i].Scope.Service < out[j].Scope.Service
	})
	return out
}

// scorePlaybookCluster averages the cluster's lesson confidences,
// then nudges upward by a small per-lesson corroboration bonus
// capped at 0.95 so a single high-confidence lesson can seed a
// playbook without dominating multi-lesson clusters.
func scorePlaybookCluster(c playbookCluster) float64 {
	if len(c.Lessons) == 0 {
		return 0
	}
	sum := 0.0
	for _, l := range c.Lessons {
		sum += l.Confidence
	}
	mean := sum / float64(len(c.Lessons))
	bonus := 0.05 * float64(len(c.Lessons)-1)
	score := mean + bonus
	if score > 0.95 {
		score = 0.95
	}
	if score < 0 {
		score = 0
	}
	return score
}

// composePlaybookSteps emits a minimal ordered step list seeded by
// the cluster's dominant lesson kind. The first step always
// "investigate" — a generic check before action — so even thin
// clusters emit something useful. The second step is the lesson's
// dominant Kind verb; subsequent steps round out the trigger
// pattern with verify and document.
func composePlaybookSteps(c playbookCluster) []domain.PlaybookStep {
	if len(c.Lessons) == 0 {
		return nil
	}
	dom := c.Lessons[0].Kind
	steps := []domain.PlaybookStep{
		{Order: 1, Action: "investigate", Description: fmt.Sprintf("Confirm the %s pattern is in effect", c.Trigger)},
	}
	if dom != "" {
		steps = append(steps, domain.PlaybookStep{
			Order:       2,
			Action:      dom,
			Description: fmt.Sprintf("Apply the %s response — the cluster's dominant operational kind", dom),
		})
	}
	steps = append(steps,
		domain.PlaybookStep{Order: len(steps) + 1, Action: "verify", Description: "Confirm metrics returned to baseline"},
		domain.PlaybookStep{Order: len(steps) + 2, Action: "document", Description: "Record action+outcome via record_action / record_outcome"},
	)
	return steps
}

func lessonIDsFor(ls []domain.Lesson) []string {
	out := make([]string, 0, len(ls))
	for _, l := range ls {
		out = append(out, l.ID)
	}
	return out
}

func playbookClusterID(c playbookCluster) string {
	h := sha256.New()
	_, _ = h.Write([]byte(c.Scope.Service))
	_, _ = h.Write([]byte("|"))
	_, _ = h.Write([]byte(c.Scope.Env))
	_, _ = h.Write([]byte("|"))
	_, _ = h.Write([]byte(c.Scope.Team))
	_, _ = h.Write([]byte("|"))
	_, _ = h.Write([]byte(c.Trigger))
	return "pb_" + hex.EncodeToString(h.Sum(nil)[:8])
}
