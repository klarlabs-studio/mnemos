package govwrite_test

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
)

// seedDependenceFixture stores an advisory, a blog that cites it by name, a
// digest whose metadata says it derives from the blog, and independent
// release notes (ADR 0038). The governed Events write detects the blog's and
// the digest's dependencies itself; nothing here records one by hand.
//
//	cl_false   evidence advisory + blog   (the copy folded onto the original)
//	cl_digest  evidence digest            (reached through blog -> digest)
//	cl_date    evidence advisory + notes  (real corroboration)
//	cl_mixed   evidence blog + notes      (reached through blog, kept on notes)
func seedDependenceFixture(t *testing.T, w *govwrite.Writer) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	if _, err := w.Events(ctx, []domain.Event{
		{ID: "ev_adv", RunID: "r1", Content: "libfoo 2.3 fixed the overflow.", SchemaVersion: "1.0", SourceInputID: "vendor-advisory", Timestamp: now, IngestedAt: now, CreatedBy: "vendor"},
		{ID: "ev_notes", RunID: "r1", Content: "libfoo 2.3 was released in March.", SchemaVersion: "1.0", SourceInputID: "release-notes", Timestamp: now, IngestedAt: now, CreatedBy: "maintainer"},
	}); err != nil {
		t.Fatalf("seed sources: %v", err)
	}
	if _, err := w.Events(ctx, []domain.Event{
		{ID: "ev_blog", RunID: "r1", Content: "libfoo 2.3 fixed the overflow.\nSource: the libfoo vendor advisory.", SchemaVersion: "1.0", SourceInputID: "security-blog", Timestamp: now, IngestedAt: now, CreatedBy: "blogger"},
	}); err != nil {
		t.Fatalf("seed blog: %v", err)
	}
	if _, err := w.Events(ctx, []domain.Event{
		{ID: "ev_digest", RunID: "r1", Content: "Upgrade to libfoo 2.3 and drop the WAF rule.", SchemaVersion: "1.0", SourceInputID: "digest", Timestamp: now, IngestedAt: now, CreatedBy: "editor",
			Metadata: map[string]string{"input_derived_from": "ev_blog"}},
	}); err != nil {
		t.Fatalf("seed digest: %v", err)
	}
	for _, id := range []string{"cl_false", "cl_digest", "cl_date", "cl_mixed"} {
		seedClaim(t, w, id)
	}
	if _, err := w.EvidenceLinks(ctx, []domain.ClaimEvidence{
		{ClaimID: "cl_false", EventID: "ev_adv"}, {ClaimID: "cl_false", EventID: "ev_blog"},
		{ClaimID: "cl_digest", EventID: "ev_digest"},
		{ClaimID: "cl_date", EventID: "ev_adv"}, {ClaimID: "cl_date", EventID: "ev_notes"},
		{ClaimID: "cl_mixed", EventID: "ev_blog"}, {ClaimID: "cl_mixed", EventID: "ev_notes"},
	}); err != nil {
		t.Fatalf("seed links: %v", err)
	}
}

func statuses(t *testing.T, w *govwrite.Writer, ids ...string) map[string]domain.ClaimStatus {
	t.Helper()
	got, err := w.Conn().Claims.ListByIDs(context.Background(), ids)
	if err != nil {
		t.Fatalf("read claims: %v", err)
	}
	out := map[string]domain.ClaimStatus{}
	for _, c := range got {
		out[c.ID] = c.Status
	}
	return out
}

func TestRetractEvent_FollowsDependenceAndKeepsIndependentCorroboration(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()
	seedDependenceFixture(t, w)

	// What the writes recorded, read back from the store.
	deps, err := w.Conn().Dependencies.ListDependents(ctx, []string{"ev_adv", "ev_blog"})
	if err != nil {
		t.Fatal(err)
	}
	var recorded []string
	for _, d := range deps {
		recorded = append(recorded, fmt.Sprintf("%s->%s %s/%s %q", d.EventID, d.DependsOn, d.Kind, d.Signal, d.Reference))
	}
	sort.Strings(recorded)
	want := []string{
		`ev_blog->ev_adv cites/named_source "Source: the libfoo vendor advisory."`,
		`ev_digest->ev_blog derived_from/metadata "input_derived_from: ev_blog"`,
	}
	if fmt.Sprint(recorded) != fmt.Sprint(want) {
		t.Fatalf("recorded dependencies =\n %v\nwant\n %v", recorded, want)
	}

	rep, err := w.RetractEvent(ctx, "ev_adv", domain.ChangeNote{Reason: "advisory withdrawn", ChangedBy: "carol"})
	if err != nil {
		t.Fatalf("RetractEvent: %v", err)
	}
	got := statuses(t, w, "cl_false", "cl_digest", "cl_date", "cl_mixed")
	wantStatus := map[string]domain.ClaimStatus{
		"cl_false": domain.ClaimStatusDeprecated, "cl_digest": domain.ClaimStatusDeprecated,
		"cl_date": domain.ClaimStatusActive, "cl_mixed": domain.ClaimStatusActive,
	}
	for id, s := range wantStatus {
		if got[id] != s {
			t.Errorf("%s status = %s, want %s", id, got[id], s)
		}
	}

	paths := map[string]string{}
	for _, p := range rep.Impact.BeliefPaths {
		paths[p.BeliefID] = p.Outcome + " " + strings.Join(p.Via, ">")
	}
	wantPaths := map[string]string{
		"cl_false":  "retracted ev_adv",
		"cl_date":   "rescored ev_adv",
		"cl_digest": "retracted ev_adv>ev_blog>ev_digest",
		"cl_mixed":  "kept ev_adv>ev_blog",
	}
	if fmt.Sprint(paths) != fmt.Sprint(wantPaths) {
		t.Errorf("belief paths = %v, want %v", paths, wantPaths)
	}
	var hops []string
	for _, h := range rep.Impact.DependentEpisodes {
		hops = append(hops, h.EventID+"->"+h.DependsOn)
	}
	if fmt.Sprint(hops) != "[ev_blog->ev_adv ev_digest->ev_blog]" {
		t.Errorf("dependent episodes = %v", hops)
	}
	if fmt.Sprint(rep.Impact.RetractedBeliefs) != "[cl_false cl_digest]" {
		t.Errorf("retracted beliefs = %v", rep.Impact.RetractedBeliefs)
	}
	if len(rep.Impact.Rescored) != 1 || rep.Impact.Rescored[0].ID != "cl_date" || rep.Impact.Rescored[0].RemainingEvidence != 1 {
		t.Errorf("rescored = %+v, want cl_date on one remaining episode", rep.Impact.Rescored)
	}
	// The dependents themselves are not retracted (ADR 0038 §4).
	for _, id := range []string{"ev_blog", "ev_digest"} {
		if _, retracted, err := govwrite.LookupRetraction(ctx, w.Conn(), id); err != nil || retracted {
			t.Errorf("%s retracted = %v (%v), want a dependent episode left as it is", id, retracted, err)
		}
	}

	// Un-retraction restores what the dependence withdrew.
	if _, err := w.UnretractEvent(ctx, "ev_adv", domain.ChangeNote{Reason: "advisory reinstated", ChangedBy: "carol"}); err != nil {
		t.Fatalf("UnretractEvent: %v", err)
	}
	for id, s := range statuses(t, w, "cl_false", "cl_digest") {
		if s != domain.ClaimStatusActive {
			t.Errorf("%s status after un-retraction = %s, want active", id, s)
		}
	}
}

// A belief withdrawn with a copy is not restored by un-retracting the copy
// while the copy's own source is still retracted: the copy is not independent
// evidence of anything (ADR 0038 §5).
func TestUnretractEvent_DependentOfARetractedSourceIsNotLiveEvidence(t *testing.T) {
	t.Parallel()
	w := newWriter(t)
	ctx := context.Background()
	seedDependenceFixture(t, w)
	seedClaim(t, w, "cl_blog_only")
	if _, err := w.EvidenceLinks(ctx, []domain.ClaimEvidence{{ClaimID: "cl_blog_only", EventID: "ev_blog"}}); err != nil {
		t.Fatal(err)
	}
	note := domain.ChangeNote{Reason: "r", ChangedBy: "carol"}
	if _, err := w.RetractEvent(ctx, "ev_blog", note); err != nil {
		t.Fatal(err)
	}
	if _, err := w.RetractEvent(ctx, "ev_adv", note); err != nil {
		t.Fatal(err)
	}
	rep, err := w.UnretractEvent(ctx, "ev_blog", note)
	if err != nil {
		t.Fatal(err)
	}
	if s := statuses(t, w, "cl_blog_only")["cl_blog_only"]; s != domain.ClaimStatusDeprecated {
		t.Errorf("cl_blog_only = %s, want still deprecated while the advisory the blog copies is retracted", s)
	}
	skipped := false
	for _, sk := range rep.Impact.Skipped {
		skipped = skipped || (sk.ID == "cl_blog_only" && strings.Contains(sk.Reason, "no live evidence"))
	}
	if !skipped {
		t.Errorf("skipped = %+v, want cl_blog_only skipped for lack of live evidence", rep.Impact.Skipped)
	}
}
