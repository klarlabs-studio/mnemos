package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/consolidate"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
)

// seedSourceClaim writes one active claim (with a single anchoring evidence
// event, so trust is computable) into a source brain at dsn. global tags it with
// the reserved "remember globally" confidence component.
func seedSourceClaim(t *testing.T, dsn, id, text, typ string, confidence float64, global bool) {
	t.Helper()
	ctx := context.Background()
	conn, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open source %s: %v", dsn, err)
	}
	defer func() { _ = conn.Close() }()

	now := time.Now().UTC()
	eid := "e_" + id
	if err := conn.Events.Append(ctx, domain.Event{
		ID: eid, Content: text, SourceInputID: "src_" + id, Timestamp: now, IngestedAt: now,
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}
	c := domain.Claim{
		ID: id, Text: text, Type: domain.ClaimType(typ), Confidence: confidence,
		Status: domain.ClaimStatusActive, CreatedAt: now, ValidFrom: now,
	}
	if global {
		c.ConfidenceComponents = map[string]float64{consolidate.FloatGlobalComponent: 1}
	}
	if err := conn.Claims.Upsert(ctx, []domain.Claim{c}); err != nil {
		t.Fatalf("upsert claim: %v", err)
	}
	if err := conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{{ClaimID: id, EventID: eid}}); err != nil {
		t.Fatalf("upsert evidence: %v", err)
	}
}

// centralTexts returns the set of claim texts present in the central brain.
func centralTexts(t *testing.T, dsn string) map[string]bool {
	t.Helper()
	ctx := context.Background()
	conn, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open central %s: %v", dsn, err)
	}
	defer func() { _ = conn.Close() }()
	claims, err := conn.Claims.ListAll(ctx)
	if err != nil {
		t.Fatalf("list central claims: %v", err)
	}
	out := map[string]bool{}
	for _, c := range claims {
		out[c.Text] = true
	}
	return out
}

const (
	generalText  = "prefer idempotent retries with exponential backoff for flaky upstreams"
	lowTrustText = "maybe we should try switching the json parser someday"
	repoText     = "raise the worker pool size in /srv/app/internal/pool value"
	globalText   = "pin the base image tag for reproducible container builds"
)

func seedFloatBackSource(t *testing.T, srcDSN string) {
	seedSourceClaim(t, srcDSN, "c_general", generalText, "fact", 0.9, false)
	seedSourceClaim(t, srcDSN, "c_lowtrust", lowTrustText, "hypothesis", 0.2, false)
	seedSourceClaim(t, srcDSN, "c_repo", repoText, "decision", 0.95, false)
	// Low trust AND (would-be) skippable, but tagged --global → floats anyway.
	seedSourceClaim(t, srcDSN, "c_global", globalText, "decision", 0.1, true)
}

// TestFloatBack_ApplySelectsByImportanceAndGlobalTag is the end-to-end gate:
// under --apply, a high-trust general learning and an explicitly --global-tagged
// claim float up into the central brain, while a low-trust claim and a
// repo-specific claim do not.
func TestFloatBack_ApplySelectsByImportanceAndGlobalTag(t *testing.T) {
	dir := t.TempDir()
	srcDSN := "sqlite://" + filepath.Join(dir, "repo.db")
	centralDSN := "sqlite://" + filepath.Join(dir, "central.db")
	seedFloatBackSource(t, srcDSN)

	out := captureStdout(t, func() {
		handleFloatBack([]string{"--from", srcDSN, "--to", centralDSN, "--apply"}, Flags{})
	})
	if !contains(out, `"applied": true`) {
		t.Fatalf("expected applied:true in output, got: %s", out)
	}

	got := centralTexts(t, centralDSN)
	if !got[generalText] {
		t.Errorf("general high-trust learning should have floated up; central=%v", got)
	}
	if !got[globalText] {
		t.Errorf("--global-tagged claim should have floated up unconditionally; central=%v", got)
	}
	if got[lowTrustText] {
		t.Errorf("low-trust claim must NOT float up")
	}
	if got[repoText] {
		t.Errorf("repo-specific claim must NOT float up")
	}
	if len(got) != 2 {
		t.Errorf("want exactly 2 floated claims in central, got %d: %v", len(got), got)
	}
}

// TestFloatBack_DryRunWritesNothing confirms the default (no --apply) never
// writes to the central brain.
func TestFloatBack_DryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	srcDSN := "sqlite://" + filepath.Join(dir, "repo.db")
	centralDSN := "sqlite://" + filepath.Join(dir, "central.db")
	seedFloatBackSource(t, srcDSN)

	out := captureStdout(t, func() {
		handleFloatBack([]string{"--from", srcDSN, "--to", centralDSN}, Flags{})
	})
	if !contains(out, `"dry_run": true`) {
		t.Fatalf("expected dry_run:true in output, got: %s", out)
	}
	// The plan still reports what WOULD float.
	if !contains(out, `"floated_count": 2`) {
		t.Fatalf("expected floated_count:2 in dry-run plan, got: %s", out)
	}
	if got := centralTexts(t, centralDSN); len(got) != 0 {
		t.Fatalf("dry-run must not write; central has %d claims: %v", len(got), got)
	}
}

// TestFloatBack_Idempotent proves a second --apply does not duplicate: the
// content-addressed dedup against the central brain skips everything the first
// run already floated.
func TestFloatBack_Idempotent(t *testing.T) {
	dir := t.TempDir()
	srcDSN := "sqlite://" + filepath.Join(dir, "repo.db")
	centralDSN := "sqlite://" + filepath.Join(dir, "central.db")
	seedFloatBackSource(t, srcDSN)

	run := func() string {
		return captureStdout(t, func() {
			handleFloatBack([]string{"--from", srcDSN, "--to", centralDSN, "--apply"}, Flags{})
		})
	}
	_ = run()
	out := run()
	if !contains(out, `"floated_count": 0`) {
		t.Fatalf("second run should float nothing (all deduped), got: %s", out)
	}
	if got := centralTexts(t, centralDSN); len(got) != 2 {
		t.Fatalf("re-running must not duplicate; central has %d claims: %v", len(got), got)
	}
}

// TestSameBrain covers the source==central refusal gate: float-back refuses when
// the two DSNs resolve to the same store (nothing to float), across SQLite path
// spellings, while distinct stores are not conflated.
func TestSameBrain(t *testing.T) {
	dir := t.TempDir()
	a := "sqlite://" + filepath.Join(dir, "brain.db")
	// Same file, different DSN spelling (query param) → same brain.
	aParam := a + "?_busy_timeout=5000"
	b := "sqlite://" + filepath.Join(dir, "other.db")

	if !sameBrain(a, a) {
		t.Errorf("identical DSNs must be the same brain")
	}
	if !sameBrain(a, aParam) {
		t.Errorf("same file with different params must be the same brain")
	}
	if sameBrain(a, b) {
		t.Errorf("distinct files must NOT be the same brain")
	}
}
