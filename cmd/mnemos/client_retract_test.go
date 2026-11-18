package main

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"go.klarlabs.de/mnemos/client"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
)

// The Go client's RetractEvent against a real `serve` over SQLite: the report
// decodes with the same content the governed writer stored, a claims:read
// token is refused, and an unknown event is a 404.
func TestClient_RetractEvent_AgainstServe(t *testing.T) {
	f := newRunScopeFixture(t)
	f.seedRun(t, "run-a", "ev-a", "cl-a")
	ctx := context.Background()

	reader := client.New(f.srv.URL, client.WithToken(f.openToken(t, []string{"claims:read"})))
	_, err := reader.RetractEvent(ctx, "ev-a", "no")
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
		t.Fatalf("claims:read token: err = %v, want 403", err)
	}

	writer := client.New(f.srv.URL, client.WithToken(f.openToken(t, []string{domain.ScopeClaimsWrite})))
	if _, err := writer.RetractEvent(ctx, "ev-missing", ""); !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Fatalf("unknown event: err = %v, want 404", err)
	}

	got, err := writer.RetractEvent(ctx, "ev-a", "stale source")
	if err != nil {
		t.Fatalf("RetractEvent: %v", err)
	}
	if got.EventID != "ev-a" || got.AlreadyRetracted || got.Reason != "stale source" {
		t.Errorf("report = %+v", got)
	}
	if c := got.Impact.RetractedClaims; len(c) != 1 || c[0] != "cl-a" {
		t.Errorf("retracted claims = %v, want [cl-a]", c)
	}

	stored, ok, err := govwrite.LookupRetraction(ctx, f.conn, "ev-a")
	if err != nil || !ok {
		t.Fatalf("lookup: ok=%v err=%v", ok, err)
	}
	at, err := client.ParseTime(got.RetractedAt)
	if err != nil || !at.Equal(stored.RetractedAt) || stored.RetractedBy != got.RetractedBy {
		t.Errorf("client report %+v differs from stored record %+v (parse err %v)", got, stored, err)
	}

	again, err := writer.RetractEvent(ctx, "ev-a", "")
	if err != nil || !again.AlreadyRetracted {
		t.Errorf("repeat: %+v, %v; want already retracted", again, err)
	}
}

// The Go client's UnretractEvent against a real `serve` over SQLite: the
// report decodes with the content the store holds afterwards, and the
// refusals map to 403, 404 and 409.
func TestClient_UnretractEvent_AgainstServe(t *testing.T) {
	f := newRunScopeFixture(t)
	f.seedRun(t, "run-a", "ev-a", "cl-a")
	f.seedRun(t, "run-a", "ev-never", "cl-never")
	ctx := context.Background()
	writer := client.New(f.srv.URL, client.WithToken(f.openToken(t, []string{domain.ScopeClaimsWrite})))
	if _, err := writer.RetractEvent(ctx, "ev-a", "stale source"); err != nil {
		t.Fatalf("RetractEvent: %v", err)
	}

	var apiErr *client.APIError
	reader := client.New(f.srv.URL, client.WithToken(f.openToken(t, []string{"claims:read"})))
	if _, err := reader.UnretractEvent(ctx, "ev-a", "no"); !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
		t.Fatalf("claims:read token: err = %v, want 403", err)
	}
	if _, err := writer.UnretractEvent(ctx, "ev-missing", ""); !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Fatalf("unknown event: err = %v, want 404", err)
	}
	if _, err := writer.UnretractEvent(ctx, "ev-never", ""); !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict {
		t.Fatalf("never retracted: err = %v, want 409", err)
	}

	got, err := writer.UnretractEvent(ctx, "ev-a", "source was fine")
	if err != nil {
		t.Fatalf("UnretractEvent: %v", err)
	}
	if got.EventID != "ev-a" || got.AlreadyUnretracted || got.Reason != "source was fine" || got.Generation != 1 {
		t.Errorf("report = %+v", got)
	}
	if r := got.Impact.Restored; len(r) != 1 || r[0].ID != "cl-a" || r[0].Status != string(domain.ClaimStatusActive) {
		t.Errorf("restored = %+v, want cl-a back to active", r)
	}
	claims, err := f.conn.Claims.ListByIDs(ctx, []string{"cl-a"})
	if err != nil || len(claims) != 1 || claims[0].Status != domain.ClaimStatusActive {
		t.Errorf("cl-a in the store = %+v (%v), want active", claims, err)
	}
	if _, ok, err := govwrite.LookupRetraction(ctx, f.conn, "ev-a"); err != nil || ok {
		t.Errorf("ev-a still retracted (ok=%v err=%v)", ok, err)
	}

	again, err := writer.UnretractEvent(ctx, "ev-a", "")
	if err != nil || !again.AlreadyUnretracted {
		t.Errorf("repeat: %+v, %v; want already un-retracted", again, err)
	}
}
