package brainbench

import (
	"context"
	"fmt"
	"sort"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
	"go.klarlabs.de/mnemos/internal/trust"
)

// MetricTrustDecayByIdentity is the scored trust_decay: the ADR 0032 projection
// counted per belief IDENTITY rather than per claim row.
const MetricTrustDecayByIdentity = "trust_decay_by_identity"

// Identity maps a claim id to the id of the claim that holds its belief after
// the treatment's merges: a merged duplicate maps to the end of its
// Claim.MergedInto chain. Claims absent from the map are their own identity.
//
// It is read from the TREATMENT arm and applied to every arm. The arms are
// copies of one seed, so a claim id names the same claim in each; the untreated
// arms carry no redirects of their own, and without the treatment's map they
// would count a duplicate and its original as two beliefs where the treatment
// counts one — a difference in counting, not in the brain.
type Identity map[string]string

// identityFrom builds the identity map from the redirects in a claim list.
func identityFrom(claims []domain.Claim) Identity {
	next := make(map[string]string)
	for _, c := range claims {
		if c.MergedInto != "" && c.MergedInto != c.ID {
			next[c.ID] = c.MergedInto
		}
	}
	ids := make(Identity, len(next))
	for id := range next {
		ids[id] = Identity(next).canonical(id)
	}
	return ids
}

// canonical follows id's redirect chain to the claim that holds the belief
// now. A cycle (which a merge never writes) stops at the first repeat rather
// than looping.
func (ids Identity) canonical(id string) string {
	seen := map[string]bool{id: true}
	for {
		next, ok := ids[id]
		if !ok || seen[next] {
			return id
		}
		seen[next] = true
		id = next
	}
}

// readIdentity reads the merge redirects the treatment left behind. It reads
// before the treatment arm is measured; measurement never writes merged_into,
// but reading first keeps the census-first rule.
func readIdentity(ctx context.Context, dsn string) (Identity, error) {
	conn, err := store.Open(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("brainbench: open identity handle: %w", err)
	}
	defer func() { _ = conn.Close() }()
	claims, err := conn.Claims.ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("brainbench: list claims for identity: %w", err)
	}
	return identityFrom(claims), nil
}

// identityTrustDecay is the ADR 0032 trust_decay ratio over belief identities.
//
// The ADR 0019 vital counts claim rows, which is right for a live brain and
// wrong for a comparison across a merge: the untreated arm holds a duplicate
// and its original as two beliefs, the treatment holds one, and folding a
// not-yet-crossing duplicate away shrinks the expected crossings and raises
// the ratio — the merge is scored as decay that no belief underwent (or, when
// the folded claim was the one crossing, as recovery that none underwent).
//
// Each identity is measured once, on its live member with the latest freshness
// reference. In the treatment that is the survivor, the only live member, and
// it carries the union of the group's evidence. In an untreated arm it is the
// freshest restatement, which is what that survivor's freshness becomes. So a
// correct merge is neutral, while a merge that kept the stale claim and lost
// the fresh evidence reads as the decay it is.
//
// The projection is trust.ProjectDecay — the vital's own function — and an
// identity with no canonical trust inputs is excluded, as the vital excludes
// it. Too few identities on their way to the floor is unknown, never 0.
func identityTrustDecay(claims []domain.Claim, inputs map[string]domain.TrustInput, ids Identity, now time.Time) Metric {
	type rep struct {
		id string
		in domain.TrustInput
	}
	reps := make(map[string]rep)
	for _, c := range claims {
		if !c.IsCurrent() || c.Status == domain.ClaimStatusDeprecated {
			continue
		}
		in, ok := inputs[c.ID]
		if !ok {
			continue
		}
		key := ids.canonical(c.ID)
		cur, seen := reps[key]
		if !seen || fresherRepresentative(c.ID, in, cur.id, cur.in, key) {
			reps[key] = rep{id: c.ID, in: in}
		}
	}
	keys := make([]string, 0, len(reps))
	for k := range reps {
		keys = append(keys, k)
	}
	sort.Strings(keys) // summation order fixed, so the noise arm reproduces it bit for bit
	var tally trust.DecayTally
	for _, k := range keys {
		tally.Add(reps[k].in, now)
	}
	ratio, ok := tally.Ratio()
	if !ok {
		return UnknownMetric(MetricTrustDecayByIdentity, LowerBetter, fmt.Sprintf(
			"%d belief identit(ies) on their way to the freshness floor, %.1f expected to reach it within %.0f days; too few to compare against the steady rate",
			tally.Approaching, tally.Expected, trust.DecayHorizonDays))
	}
	return KnownMetric(MetricTrustDecayByIdentity, LowerBetter, ratio, tally.Approaching, "x_steady_rate")
}

// fresherRepresentative reports whether candidate should replace current as an
// identity's representative: the later freshness reference wins; on a tie the
// canonical survivor, then the lower id, so the choice never depends on order.
func fresherRepresentative(candID string, cand domain.TrustInput, curID string, cur domain.TrustInput, canonical string) bool {
	cr, ur := trust.Reference(cand), trust.Reference(cur)
	if !cr.Equal(ur) {
		return cr.After(ur)
	}
	if (candID == canonical) != (curID == canonical) {
		return candID == canonical
	}
	return candID < curID
}
