package extract

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

func TestEngineExtractCreatesClaimAndEvidencePerEvent(t *testing.T) {
	engine := Engine{
		now: func() time.Time {
			return time.Date(2026, 4, 12, 13, 0, 0, 0, time.UTC)
		},
		nextID: seqClaimIDs(),
	}

	events := []domain.Event{
		{ID: "ev_1", Content: "We decided to pause the rollout."},
		{ID: "ev_2", Content: "Revenue might recover next quarter."},
		{ID: "ev_3", Content: "The churn rate increased to 7%."},
	}

	claims, evidence, err := engine.Extract(context.Background(), events)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if len(claims) != 3 {
		t.Fatalf("Extract() claims len = %d, want 3", len(claims))
	}
	if len(evidence) != 3 {
		t.Fatalf("Extract() evidence len = %d, want 3", len(evidence))
	}
	if claims[0].Type != domain.ClaimTypeDecision {
		t.Fatalf("claim[0] type = %q, want decision", claims[0].Type)
	}
	if claims[1].Type != domain.ClaimTypeHypothesis {
		t.Fatalf("claim[1] type = %q, want hypothesis", claims[1].Type)
	}
	if claims[2].Type != domain.ClaimTypeFact {
		t.Fatalf("claim[2] type = %q, want fact", claims[2].Type)
	}
	if evidence[0].EventID != "ev_1" || evidence[0].ClaimID != claims[0].ID {
		t.Fatalf("evidence[0] mismatch: %+v claim=%s", evidence[0], claims[0].ID)
	}
}

func TestEngineExtractSplitsSentencesAndDedupes(t *testing.T) {
	engine := Engine{
		now:    func() time.Time { return time.Date(2026, 4, 12, 13, 5, 0, 0, time.UTC) },
		nextID: seqClaimIDs(),
	}

	events := []domain.Event{
		{ID: "ev_1", Content: "Revenue increased to 10%. Revenue increased to 10%!"},
		{ID: "ev_2", Content: "We decided to expand EU operations."},
	}

	claims, evidence, err := engine.Extract(context.Background(), events)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if len(claims) != 2 {
		t.Fatalf("Extract() claims len = %d, want 2", len(claims))
	}
	if len(evidence) != 2 {
		t.Fatalf("Extract() evidence len = %d, want 2", len(evidence))
	}
	if claims[0].Confidence <= 0.8 {
		t.Fatalf("claims[0] confidence = %f, expected boosted fact confidence", claims[0].Confidence)
	}
}

func TestEngineExtractMarksContestedClaims(t *testing.T) {
	engine := Engine{
		now:    func() time.Time { return time.Date(2026, 4, 12, 13, 10, 0, 0, time.UTC) },
		nextID: seqClaimIDs(),
	}

	events := []domain.Event{
		{ID: "ev_1", Content: "Revenue decreased after launch."},
		{ID: "ev_2", Content: "Revenue did not decrease after launch."},
	}

	claims, _, err := engine.Extract(context.Background(), events)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if len(claims) != 2 {
		t.Fatalf("Extract() claims len = %d, want 2", len(claims))
	}
	if claims[0].Status != domain.ClaimStatusContested {
		t.Fatalf("claims[0] status = %q, want contested", claims[0].Status)
	}
	if claims[1].Status != domain.ClaimStatusContested {
		t.Fatalf("claims[1] status = %q, want contested", claims[1].Status)
	}
}

func TestEngineExtractPreservesDecimalsInSentenceSplit(t *testing.T) {
	engine := Engine{
		now:    func() time.Time { return time.Date(2026, 4, 12, 13, 15, 0, 0, time.UTC) },
		nextID: seqClaimIDs(),
	}

	events := []domain.Event{
		{ID: "ev_1", Content: "Revenue grew 3.5% in Q3."},
	}

	claims, _, err := engine.Extract(context.Background(), events)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if len(claims) != 1 {
		t.Fatalf("Extract() claims len = %d, want 1", len(claims))
	}
	if !strings.Contains(claims[0].Text, "3.5%") {
		t.Fatalf("claim text = %q, want it to contain '3.5%%'", claims[0].Text)
	}
}

func TestEngineExtractMarksSamePolarityContradictions(t *testing.T) {
	engine := Engine{
		now:    func() time.Time { return time.Date(2026, 4, 12, 13, 20, 0, 0, time.UTC) },
		nextID: seqClaimIDs(),
	}

	events := []domain.Event{
		{ID: "ev_1", Content: "We will use React for the frontend."},
		{ID: "ev_2", Content: "We will use Vue for the frontend."},
	}

	claims, _, err := engine.Extract(context.Background(), events)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if len(claims) != 2 {
		t.Fatalf("Extract() claims len = %d, want 2", len(claims))
	}
	if claims[0].Status != domain.ClaimStatusContested {
		t.Fatalf("claims[0] status = %q, want contested", claims[0].Status)
	}
	if claims[1].Status != domain.ClaimStatusContested {
		t.Fatalf("claims[1] status = %q, want contested", claims[1].Status)
	}
}

func TestCleanMarkdownStripsFormatting(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"bold asterisks", "We **migrated** to SQLite.", "We migrated to SQLite."},
		{"bold underscores", "We __migrated__ to SQLite.", "We migrated to SQLite."},
		{"strikethrough", "We use ~~MongoDB~~ SQLite.", "We use MongoDB SQLite."},
		{"checkbox done", "- [x] Ship v0.4", "Ship v0.4"},
		{"checkbox open", "- [ ] Ship v0.5", "Ship v0.5"},
		{"bullet", "- Item content", "Item content"},
		{"numbered", "1. First item", "First item"},
		{"link", "See [docs](https://example.com) for more.", "See docs for more."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := cleanMarkdown(c.in)
			if got != c.want {
				t.Fatalf("cleanMarkdown(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func seqClaimIDs() func() (string, error) {
	i := 0
	return func() (string, error) {
		id := "cl_test_" + strconv.Itoa(i)
		i++
		return id, nil
	}
}

// Opposite polarity only means disagreement when the claims are about the same
// thing. The bar was a flat "share >= 2 tokens" with no subject anchor, so
// bulk-ingesting a document of technical bullets — which all draw on the same
// vocabulary — marked 53% of claims contested (70% in the worst project),
// pairing entirely unrelated lines. Measured on a real 3,013-claim corpus, the
// anchor cut false contested by 76% (2,110 -> 504) without losing the genuine
// case below.
func TestMarkContested_UnrelatedClaimsNotContested(t *testing.T) {
	claims := []domain.Claim{
		{ID: "c1", Text: "Frame: nonce LE, supp-size includes size-byte and marker, magic bytes"},
		{ID: "c2", Text: "Next session should not start the M1 remainder before approval flows"},
		{ID: "c3", Text: "Postgres tsvector and pgvector are auto-wired by capability"},
	}
	markContestedClaims(claims)
	for _, c := range claims {
		if c.Status == domain.ClaimStatusContested {
			t.Errorf("unrelated claim marked contested: %q", c.Text)
		}
	}
}

// The genuine case must survive: same subject, opposite polarity.
func TestMarkContested_SameSubjectOppositePolarityStillContests(t *testing.T) {
	claims := []domain.Claim{
		{ID: "c1", Text: "Revenue decreased after launch"},
		{ID: "c2", Text: "Revenue did not decrease after launch"},
	}
	markContestedClaims(claims)
	for _, c := range claims {
		if c.Status != domain.ClaimStatusContested {
			t.Errorf("genuine polarity contradiction not contested: %q (status %q)", c.Text, c.Status)
		}
	}
}
