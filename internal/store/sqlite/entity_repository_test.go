package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
)

func TestFindOrCreate_DedupsByNormalizedName(t *testing.T) {
	db, err := open(filepath.Join(t.TempDir(), "ent.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	repo := NewEntityRepository(db)

	a, err := repo.FindOrCreate(ctx, "Felix Geelhaar", domain.EntityTypePerson, "")
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	b, err := repo.FindOrCreate(ctx, "felix  geelhaar", domain.EntityTypePerson, "")
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if a.ID != b.ID {
		t.Fatalf("normalized-name dedup broken: %q vs %q", a.ID, b.ID)
	}
	// Different type → different entity even with same name.
	c, err := repo.FindOrCreate(ctx, "Felix Geelhaar", domain.EntityTypeProject, "")
	if err != nil {
		t.Fatalf("type-distinct create: %v", err)
	}
	if c.ID == a.ID {
		t.Fatalf("type should partition: person and project share an id")
	}
}

func TestLinkClaimAndListClaimsForEntity(t *testing.T) {
	db, err := open(filepath.Join(t.TempDir(), "ent.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	now := nowRFC()

	if _, err := db.Exec(
		`INSERT INTO claims (id, text, type, confidence, status, created_at)
		 VALUES ('cl1', 'felix bought coffee', 'fact', 0.8, 'active', ?)`, now,
	); err != nil {
		t.Fatalf("seed claim: %v", err)
	}

	repo := NewEntityRepository(db)
	e, err := repo.FindOrCreate(ctx, "Felix", domain.EntityTypePerson, "")
	if err != nil {
		t.Fatalf("create entity: %v", err)
	}
	if err := repo.LinkClaim(ctx, "cl1", e.ID, "subject"); err != nil {
		t.Fatalf("link: %v", err)
	}
	// Idempotent.
	if err := repo.LinkClaim(ctx, "cl1", e.ID, "subject"); err != nil {
		t.Fatalf("re-link: %v", err)
	}

	got, err := repo.ListClaimsForEntity(ctx, e.ID)
	if err != nil {
		t.Fatalf("list claims: %v", err)
	}
	if len(got) != 1 || got[0].ID != "cl1" {
		t.Fatalf("expected [cl1], got %+v", got)
	}
}

func TestMerge_RedirectsClaimEntities(t *testing.T) {
	db, err := open(filepath.Join(t.TempDir(), "ent.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	now := nowRFC()

	for _, id := range []string{"cl1", "cl2"} {
		if _, err := db.Exec(
			`INSERT INTO claims (id, text, type, confidence, status, created_at)
			 VALUES (?, 'x', 'fact', 0.8, 'active', ?)`, id, now,
		); err != nil {
			t.Fatalf("seed claim %s: %v", id, err)
		}
	}

	repo := NewEntityRepository(db)
	winner, _ := repo.FindOrCreate(ctx, "Felix Geelhaar", domain.EntityTypePerson, "")
	// Force a duplicate row by using a different normalized name.
	loser, _ := repo.FindOrCreate(ctx, "felixgeelhaar", domain.EntityTypePerson, "")
	if winner.ID == loser.ID {
		t.Fatal("test setup: two distinct entities expected")
	}
	_ = repo.LinkClaim(ctx, "cl1", winner.ID, "subject")
	_ = repo.LinkClaim(ctx, "cl2", loser.ID, "subject")

	if err := repo.Merge(ctx, winner.ID, loser.ID); err != nil {
		t.Fatalf("merge: %v", err)
	}

	got, _ := repo.ListClaimsForEntity(ctx, winner.ID)
	ids := make(map[string]bool)
	for _, c := range got {
		ids[c.ID] = true
	}
	if !ids["cl1"] || !ids["cl2"] {
		t.Fatalf("winner should have absorbed both claims, got %+v", ids)
	}
	gone, _ := repo.ListClaimsForEntity(ctx, loser.ID)
	if len(gone) != 0 {
		t.Fatalf("loser should have no claims after merge, got %+v", gone)
	}
}

func TestMerge_RejectsSelfMerge(t *testing.T) {
	db, _ := open(filepath.Join(t.TempDir(), "ent.db"))
	t.Cleanup(func() { _ = db.Close() })
	repo := NewEntityRepository(db)
	if err := repo.Merge(context.Background(), "x", "x"); err != ErrEntityMergeSelf {
		t.Fatalf("expected ErrEntityMergeSelf, got %v", err)
	}
}

func TestClaimIDsMissingEntityLinks(t *testing.T) {
	db, _ := open(filepath.Join(t.TempDir(), "ent.db"))
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	now := nowRFC()
	for _, id := range []string{"linked", "orphan"} {
		if _, err := db.Exec(
			`INSERT INTO claims (id, text, type, confidence, status, created_at)
			 VALUES (?, 'x', 'fact', 0.8, 'active', ?)`, id, now,
		); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	repo := NewEntityRepository(db)
	e, _ := repo.FindOrCreate(ctx, "X", domain.EntityTypeConcept, "")
	_ = repo.LinkClaim(ctx, "linked", e.ID, "mention")

	missing, err := repo.ClaimIDsMissingEntityLinks(ctx)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(missing) != 1 || missing[0] != "orphan" {
		t.Fatalf("expected [orphan], got %v", missing)
	}
}

func nowRFC() string {
	return "2026-04-26T00:00:00Z"
}
