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
