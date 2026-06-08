package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
	"go.klarlabs.de/mnemos/internal/workflow"
)

// telemetry handles the workspace North Star metric and the optional
// (default-off, opt-in) anonymized usage payload.
//
// The North Star is "weekly active projects with ≥1 evidence-backed
// query." Locally that maps to: at least one run_id observed in the
// past 7 days, with at least one claim that carries citations or has
// non-zero verify count. The CLI surfaces a workspace-scoped JSON
// view; opt-in operators may forward the same shape to the configured
// endpoint.
//
// Privacy posture:
//   - install_id is a random 16-byte hex token persisted under the data
//     directory. It is not derived from any user-identifying value and
//     is regenerated when deleted.
//   - The payload contains aggregated counts, not events, claims, or
//     queries. No claim text, no run names, no file paths leave the
//     host.
//   - Telemetry is OFF by default. Two independent gates must hold for
//     anything to be sent: opt-in flag set AND a non-empty endpoint
//     configured via MNEMOS_TELEMETRY_ENDPOINT.

// telemetryFile is the marker under the data dir whose presence means
// the operator has explicitly opted into anonymized telemetry. Absent
// = opted out (default).
const telemetryFile = "telemetry_optin"

// installIDFile is the persistent random ID under the data dir.
const installIDFile = "install_id"

// envTelemetryEndpoint is the URL that receives the JSON payload when
// telemetry is enabled. Default empty: no destination configured = no
// network requests, even with opt-in active.
const envTelemetryEndpoint = "MNEMOS_TELEMETRY_ENDPOINT"

// envTelemetryOptIn lets operators flip on telemetry via env without
// touching the filesystem flag — useful for ephemeral CI or container
// environments. Truthy values: "1", "true", "yes" (case-insensitive).
const envTelemetryOptIn = "MNEMOS_TELEMETRY_OPTIN"

// WorkspaceMetrics is the North Star payload shape. Same struct is
// rendered by `mnemos metrics --workspace` and POSTed to the telemetry
// endpoint when opt-in is active.
type WorkspaceMetrics struct {
	InstallID                   string    `json:"install_id"`
	WorkspaceFingerprint        string    `json:"workspace_fingerprint"`
	AsOf                        time.Time `json:"as_of"`
	Version                     string    `json:"version"`
	GoVersion                   string    `json:"go_version"`
	OS                          string    `json:"os"`
	Arch                        string    `json:"arch"`
	ActiveRunIDs7d              int       `json:"active_run_ids_7d"`
	Events7d                    int       `json:"events_7d"`
	Claims7d                    int       `json:"claims_7d"`
	EvidenceBackedClaims7d      int       `json:"evidence_backed_claims_7d"`
	WeeklyActive                bool      `json:"weekly_active"`
	TotalClaims                 int       `json:"total_claims"`
	AvgTrustScore               float64   `json:"avg_trust_score"`
	ContestedClaims             int       `json:"contested_claims"`
	TelemetryOptIn              bool      `json:"telemetry_opt_in"`
	TelemetryEndpointConfigured bool      `json:"telemetry_endpoint_configured"`
}

// dataDir returns the directory used for non-database persistent state
// (install_id, telemetry flag). Honours XDG_DATA_HOME with the same
// fallback as resolveDBPath.
func dataDir() string {
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "data"
		}
		dataHome = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataHome, "mnemos")
}

// installID returns the persistent 16-byte hex token for this install.
// Generated on first call; subsequent calls return the same value.
// A regeneration after the file is deleted produces a new ID — that's
// intentional, lets operators rotate without touching anything else.
func installID() (string, error) {
	d := dataDir()
	p := filepath.Join(d, installIDFile)
	b, err := os.ReadFile(p)
	if err == nil {
		s := strings.TrimSpace(string(b))
		if len(s) >= 16 {
			return s, nil
		}
	}
	if err := os.MkdirAll(d, 0o750); err != nil {
		return "", fmt.Errorf("install_id: mkdir %s: %w", d, err)
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("install_id: rand: %w", err)
	}
	id := hex.EncodeToString(buf)
	if err := os.WriteFile(p, []byte(id+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("install_id: write %s: %w", p, err)
	}
	return id, nil
}

// telemetryOptIn reports whether the operator has opted in. True iff:
//   - MNEMOS_TELEMETRY_OPTIN env is truthy, OR
//   - a marker file exists under the data dir.
func telemetryOptIn() bool {
	if v := strings.ToLower(strings.TrimSpace(os.Getenv(envTelemetryOptIn))); v == "1" || v == "true" || v == "yes" {
		return true
	}
	if _, err := os.Stat(filepath.Join(dataDir(), telemetryFile)); err == nil {
		return true
	}
	return false
}

// setTelemetryOptIn writes or removes the marker file. Env-based opt-in
// is unaffected (it survives at the env layer until the operator
// removes it themselves).
func setTelemetryOptIn(enable bool) error {
	d := dataDir()
	p := filepath.Join(d, telemetryFile)
	if !enable {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(d, 0o750); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600)
}

// workspaceFingerprint returns a stable but anonymous identifier for
// the current workspace — the SHA-256 prefix of the absolute path of
// the resolved DB. We don't want the path itself leaving the host;
// this lets the dashboard count distinct workspaces without seeing
// where they live on disk.
func workspaceFingerprint() string {
	p := resolveDBPath()
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	h := sha256Hex(abs)
	if len(h) > 16 {
		return h[:16]
	}
	return h
}

// computeWorkspaceMetrics gathers the North Star payload.
func computeWorkspaceMetrics(ctx context.Context, conn *store.Conn) (WorkspaceMetrics, error) {
	now := time.Now().UTC()
	cutoff := now.Add(-7 * 24 * time.Hour)

	allEvents, err := conn.Events.ListAll(ctx)
	if err != nil {
		return WorkspaceMetrics{}, fmt.Errorf("list events: %w", err)
	}
	allClaims, err := conn.Claims.ListAll(ctx)
	if err != nil {
		return WorkspaceMetrics{}, fmt.Errorf("list claims: %w", err)
	}

	runs7d := map[string]struct{}{}
	events7d := 0
	for _, e := range allEvents {
		// IngestedAt is when this Mnemos instance saw the event;
		// Timestamp may be earlier (event-time vs ingest-time).
		// "Active" means recent activity on this instance, so use
		// IngestedAt.
		if e.IngestedAt.After(cutoff) {
			events7d++
			if e.RunID != "" {
				runs7d[e.RunID] = struct{}{}
			}
		}
	}

	claims7d := 0
	evidenceBacked7d := 0
	contested := 0
	var trustSum float64
	for _, c := range allClaims {
		if c.Status == domain.ClaimStatusContested {
			contested++
		}
		trustSum += c.TrustScore
		if c.CreatedAt.After(cutoff) {
			claims7d++
			if c.CitationCount > 0 || c.VerifyCount > 0 {
				evidenceBacked7d++
			}
		}
	}
	avgTrust := 0.0
	if len(allClaims) > 0 {
		avgTrust = trustSum / float64(len(allClaims))
	}

	id, err := installID()
	if err != nil {
		return WorkspaceMetrics{}, err
	}

	return WorkspaceMetrics{
		InstallID:                   id,
		WorkspaceFingerprint:        workspaceFingerprint(),
		AsOf:                        now,
		Version:                     mnemosVersion(),
		GoVersion:                   runtime.Version(),
		OS:                          runtime.GOOS,
		Arch:                        runtime.GOARCH,
		ActiveRunIDs7d:              len(runs7d),
		Events7d:                    events7d,
		Claims7d:                    claims7d,
		EvidenceBackedClaims7d:      evidenceBacked7d,
		WeeklyActive:                len(runs7d) > 0 && evidenceBacked7d > 0,
		TotalClaims:                 len(allClaims),
		AvgTrustScore:               roundTo(avgTrust, 4),
		ContestedClaims:             contested,
		TelemetryOptIn:              telemetryOptIn(),
		TelemetryEndpointConfigured: strings.TrimSpace(os.Getenv(envTelemetryEndpoint)) != "",
	}, nil
}

// sendTelemetry POSTs the metrics payload when both gates are open
// (opt-in active AND endpoint configured). Returns nil-without-send if
// either gate is closed; that's not an error, it's the default state.
func sendTelemetry(ctx context.Context, m WorkspaceMetrics) (sent bool, err error) {
	if !m.TelemetryOptIn {
		return false, nil
	}
	endpoint := strings.TrimSpace(os.Getenv(envTelemetryEndpoint))
	if endpoint == "" {
		return false, nil
	}
	body, err := json.Marshal(m)
	if err != nil {
		return false, fmt.Errorf("marshal: %w", err)
	}
	// Endpoint is operator-supplied via MNEMOS_TELEMETRY_ENDPOINT and gated
	// by an explicit opt-in marker; gosec G107/G104 are accepted here.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return false, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "mnemos-cli/"+mnemosVersion())
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("post: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return false, fmt.Errorf("telemetry endpoint returned %d", resp.StatusCode)
	}
	return true, nil
}

// telemetryStatusBanner returns a one-line human-readable summary of
// the current telemetry state. Surfaced by `metrics --workspace` so
// the operator can see at a glance whether anything would be sent.
func telemetryStatusBanner() string {
	parts := []string{}
	if telemetryOptIn() {
		parts = append(parts, "opted-in")
	} else {
		parts = append(parts, "opted-out")
	}
	if strings.TrimSpace(os.Getenv(envTelemetryEndpoint)) != "" {
		parts = append(parts, "endpoint set")
	} else {
		parts = append(parts, "no endpoint")
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

func mnemosVersion() string {
	if v := strings.TrimSpace(version); v != "" {
		return v
	}
	return "dev"
}

// handleWorkspaceMetrics renders the North Star JSON payload, optionally
// posting it when --telemetry-send is passed AND opt-in + endpoint
// gates are open.
func handleWorkspaceMetrics(send bool, f Flags) {
	err := runJob("metrics-workspace", map[string]string{}, f.Verbose, func(ctx context.Context, job *workflow.Job, conn *store.Conn) error {
		if err := job.SetStatus("loading", ""); err != nil {
			return err
		}
		m, err := computeWorkspaceMetrics(ctx, conn)
		if err != nil {
			return NewSystemError(err, "compute workspace metrics")
		}

		if f.Human {
			fmt.Println("Mnemos Workspace Metrics")
			fmt.Printf("Install ID:           %s\n", m.InstallID)
			fmt.Printf("Workspace:            %s\n", m.WorkspaceFingerprint)
			fmt.Printf("Active runs (7d):     %d\n", m.ActiveRunIDs7d)
			fmt.Printf("Events (7d):          %d\n", m.Events7d)
			fmt.Printf("Claims (7d):          %d\n", m.Claims7d)
			fmt.Printf("Evidence-backed (7d): %d\n", m.EvidenceBackedClaims7d)
			fmt.Printf("Weekly active:        %t\n", m.WeeklyActive)
			fmt.Printf("Total claims:         %d\n", m.TotalClaims)
			fmt.Printf("Avg trust score:      %.4f\n", m.AvgTrustScore)
			fmt.Printf("Telemetry status:     %s\n", telemetryStatusBanner())
		} else {
			b, err := json.MarshalIndent(m, "", "  ")
			if err != nil {
				return NewSystemError(err, "encode workspace metrics")
			}
			fmt.Println(string(b))
		}

		if send {
			sent, sendErr := sendTelemetry(ctx, m)
			switch {
			case sendErr != nil:
				return NewSystemError(sendErr, "send telemetry")
			case sent:
				fmt.Fprintln(os.Stderr, "telemetry: payload sent")
			default:
				reason := "telemetry not opted-in"
				if m.TelemetryOptIn && !m.TelemetryEndpointConfigured {
					reason = "no MNEMOS_TELEMETRY_ENDPOINT configured"
				}
				fmt.Fprintln(os.Stderr, "telemetry: not sent ("+reason+")")
			}
		}
		return nil
	})
	exitWithMnemosError(f.Verbose, err)
}
