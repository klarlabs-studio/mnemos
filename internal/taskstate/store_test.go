package taskstate

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/store"
	_ "go.klarlabs.de/mnemos/internal/store/memory"
)

func seededConn(t *testing.T, texts ...string) *store.Conn {
	t.Helper()
	ctx := context.Background()
	conn, err := store.Open(ctx, "memory://")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var claims []domain.Claim
	for i, text := range texts {
		claims = append(claims, domain.Claim{
			ID: "c" + string(rune('a'+i)), Text: text, Type: domain.ClaimTypeFact,
			Confidence: 0.9, Status: domain.ClaimStatusActive, CreatedAt: now,
		})
	}
	if err := conn.Claims.Upsert(ctx, claims); err != nil {
		t.Fatal(err)
	}
	return conn
}

func TestStoreProbe_Assess(t *testing.T) {
	conn := seededConn(t,
		"The deploy pipeline runs on GitHub Actions",
		"The billing service is stateless",
	)
	p := StoreProbe{Conn: conn}
	tests := []struct {
		name        string
		text        string
		wantKnown   string
		wantAgainst string
	}{
		{"exact restatement is known", "The deploy pipeline runs on GitHub Actions.", "ca", ""},
		{"new statement is novel", "Invoices must be issued in EUR", "", ""},
		{"negation is surprising", "The billing service is not stateless", "", "cb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := p.Assess(context.Background(), tt.text)
			if err != nil {
				t.Fatal(err)
			}
			if v.KnownAs != tt.wantKnown {
				t.Errorf("KnownAs = %q, want %q", v.KnownAs, tt.wantKnown)
			}
			if got := strings.Join(v.Contradicts, ","); got != tt.wantAgainst {
				t.Errorf("Contradicts = %q, want %q", got, tt.wantAgainst)
			}
		})
	}
}

func TestStoreProbe_Missing(t *testing.T) {
	p := StoreProbe{Conn: seededConn(t, "x is y")}
	got, err := p.Missing(context.Background(), []string{"ca", "nope"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "nope" {
		t.Fatalf("missing = %v, want [nope]", got)
	}
}

// The handoff writes through the governed writer, and what the STORE returns
// is candidate claims evidenced by the handoff episode — never beliefs.
func TestService_HandOffWritesCandidatesThroughGovernedWriter(t *testing.T) {
	conn := seededConn(t, "Latency budget for checkout is 200ms")
	svc := Service{Blocks: conn.Blocks, Probe: StoreProbe{Conn: conn}, Limit: 2000}
	ctx := context.Background()
	for _, it := range []Item{
		{Kind: KindGoal, Text: "Cut checkout latency"},
		{Kind: KindHypothesis, Text: "The tax lookup is synchronous", RestsOn: []string{"ca", "ca"}},
		{Kind: KindQuestion, Text: "Is the tax API cached?"},
	} {
		if _, err := svc.Admit(ctx, "agent", "s1", it); err != nil {
			t.Fatal(err)
		}
	}
	w, err := govwrite.Wrap(conn, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.HandOffAll(ctx, w, "agent", "tester")
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || len(res[0].Candidates) != 2 {
		t.Fatalf("handoff = %+v, want one session with 2 candidates", res)
	}
	stored, err := conn.Claims.ListByIDs(ctx, res[0].Candidates)
	if err != nil || len(stored) != 2 {
		t.Fatalf("stored = %d, %v", len(stored), err)
	}
	for _, c := range stored {
		if c.Lifecycle != domain.ClaimLifecycleCandidate {
			t.Errorf("%q lifecycle = %q, want candidate", c.Text, c.Lifecycle)
		}
	}
	links, err := conn.Claims.ListEvidenceByClaimIDs(ctx, res[0].Candidates)
	if err != nil || len(links) != 2 || links[0].EventID != res[0].EventID {
		t.Fatalf("evidence = %+v, %v; want each candidate evidenced by %s", links, err, res[0].EventID)
	}
	if st, _ := svc.Load(ctx, "agent", "s1"); len(st.Items) != 0 {
		t.Fatal("block must be cleared after a successful handoff")
	}
}

func TestService_AdmitLoadRemove(t *testing.T) {
	conn := seededConn(t, "The office coffee machine is broken")
	svc := Service{Blocks: conn.Blocks, Probe: StoreProbe{Conn: conn}, Limit: 2000}
	ctx := context.Background()
	steps := []struct {
		item  Item
		admit bool
	}{
		{Item{Kind: KindGoal, Text: "Migrate billing to Postgres"}, true},
		{Item{Kind: KindConstraint, Text: "Never deploy on Fridays"}, true},
		{Item{Kind: KindQuestion, Text: "The office coffee machine is broken"}, false},
	}
	for _, s := range steps {
		res, err := svc.Admit(ctx, "agent", "s1", s.item)
		if err != nil {
			t.Fatal(err)
		}
		if res.Decision.Admit != s.admit {
			t.Fatalf("%q admit = %v, want %v", s.item.Text, res.Decision.Admit, s.admit)
		}
	}
	st, err := svc.Load(ctx, "agent", "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(st.Items))
	}
	removed, err := svc.Remove(ctx, "agent", "s1", "never deploy on fridays")
	if err != nil || !removed {
		t.Fatalf("remove = %v, %v", removed, err)
	}
	if st, _ = svc.Load(ctx, "agent", "s1"); len(st.Items) != 1 {
		t.Fatalf("after remove items = %d, want 1", len(st.Items))
	}
}
