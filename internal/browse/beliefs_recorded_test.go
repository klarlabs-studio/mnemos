package browse_test

import (
	"context"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/browse"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/page"
	"go.klarlabs.de/mnemos/internal/pipeline"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/store"
	_ "go.klarlabs.de/mnemos/internal/store/memory"
	_ "go.klarlabs.de/mnemos/internal/store/sqlite"
)

// ADR 0036 step 2: the belief browse answers a recorded-as-of read with the
// same rewind as recall. Before it, GET /v1/beliefs?recorded_as_of= filtered
// today's claims on created_at only: a belief retracted after T read as
// deprecated, and one merged after T read as a redirect, so the browse and
// recall disagreed about what the brain held at T.
func TestBeliefs_RecordedAsOfRewindsStatusAndMerges(t *testing.T) {
	for name, dsn := range map[string]string{
		"memory": "memory://browse-recorded",
		"sqlite": "sqlite://" + filepath.Join(t.TempDir(), "browse.db"),
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			conn, err := store.Open(ctx, dsn)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			t.Cleanup(func() { _ = conn.Close() })
			tuesday := seedMergeTimeline(t, conn)

			cases := []struct {
				name   string
				filter page.ClaimFilter
				want   []string
			}{
				{"every belief held at T", page.ClaimFilter{RecordedAsOf: tuesday}, []string{"cl-held", "cl-merged", "cl-retracted"}},
				{"active at T", page.ClaimFilter{RecordedAsOf: tuesday, Status: "active"}, []string{"cl-held", "cl-merged", "cl-retracted"}},
				{"nothing was deprecated at T", page.ClaimFilter{RecordedAsOf: tuesday, Status: "deprecated"}, nil},
				{"run scope follows merged evidence", page.ClaimFilter{RecordedAsOf: tuesday, RunID: "timeline"}, []string{"cl-held", "cl-merged", "cl-retracted"}},
				{"today", page.ClaimFilter{Status: "active"}, []string{"cl-held", "cl-new", "cl-winner"}},
			}
			for _, tc := range cases {
				for _, via := range []struct {
					name string
					list func() (listed, error)
				}{
					{"Beliefs", func() (listed, error) { return pageOf(browse.Beliefs(ctx, conn, tc.filter, nil, 50, 0)) }},
					{"BeliefsInGo", func() (listed, error) { return pageOf(browse.BeliefsInGo(ctx, conn, tc.filter, nil, 50, 0)) }},
				} {
					got, err := via.list()
					if err != nil {
						t.Fatalf("%s %s: %v", tc.name, via.name, err)
					}
					if !equal(got.ids, tc.want) {
						t.Errorf("%s via %s = %v, want %v", tc.name, via.name, got.ids, tc.want)
					}
					if !tc.filter.RecordedAsOf.IsZero() {
						for _, c := range got.claims {
							if c.Status != domain.ClaimStatusActive || c.MergedInto != "" {
								t.Errorf("%s via %s: %s reads as status=%q merged_into=%q at T", tc.name, via.name, c.ID, c.Status, c.MergedInto)
							}
						}
					}
				}
			}
		})
	}
}

// seedMergeTimeline records three beliefs on Monday, then on Thursday retracts
// one, folds another into a restatement and records a new one. It returns an
// instant between the two days.
func seedMergeTimeline(t *testing.T, conn *store.Conn) time.Time {
	t.Helper()
	ctx := context.Background()
	add := func(eventID, claimID, text string) {
		t.Helper()
		now := time.Now().UTC()
		if err := conn.Events.Append(ctx, domain.Event{
			ID: eventID, RunID: "timeline", SchemaVersion: "v1", SourceInputID: "timeline",
			Content: text, Timestamp: now, IngestedAt: now,
		}); err != nil {
			t.Fatalf("append %s: %v", eventID, err)
		}
		if err := conn.Claims.Upsert(ctx, []domain.Claim{{
			ID: claimID, Text: text, Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive,
			Confidence: 0.8, CreatedAt: now,
		}}); err != nil {
			t.Fatalf("upsert %s: %v", claimID, err)
		}
		if err := conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{{ClaimID: claimID, EventID: eventID}}); err != nil {
			t.Fatalf("evidence %s: %v", claimID, err)
		}
	}
	tick := func() time.Time {
		time.Sleep(5 * time.Millisecond)
		at := time.Now().UTC()
		time.Sleep(5 * time.Millisecond)
		return at
	}

	add("ev-held", "cl-held", "the payments pool size is 50")
	add("ev-retracted", "cl-retracted", "the outage was caused by disk saturation")
	add("ev-merged", "cl-merged", "deploys go out on tuesdays")
	tuesday := tick()

	retracted, err := conn.Claims.ListByIDs(ctx, []string{"cl-retracted"})
	if err != nil || len(retracted) != 1 {
		t.Fatalf("load cl-retracted: %v", err)
	}
	retracted[0].Status = domain.ClaimStatusDeprecated
	if err := conn.Claims.UpsertWithReason(ctx, retracted, "retracted: root cause analysis"); err != nil {
		t.Fatalf("retract: %v", err)
	}
	add("ev-winner", "cl-winner", "deploys happen every tuesday")
	if _, err := pipeline.ApplySemanticDedupe(ctx, conn, pipeline.SemanticDedupePlan{
		Merges: []pipeline.SemanticMerge{{WinnerID: "cl-winner", DuplicateIDs: []string{"cl-merged"}}},
	}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	add("ev-new", "cl-new", "the payments pool size is 80")
	tick()
	return tuesday
}

type listed struct {
	ids    []string
	claims []domain.Claim
}

func pageOf(pg ports.ClaimPage, err error) (listed, error) {
	if err != nil {
		return listed{}, err
	}
	out := listed{claims: pg.Claims}
	for _, c := range out.claims {
		out.ids = append(out.ids, c.ID)
	}
	sort.Strings(out.ids)
	return out, nil
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
