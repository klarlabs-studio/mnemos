package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/auth"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/query"
)

// handleAgent dispatches `mnemos agent <subcommand>`. The token
// subcommand for agents lives here too rather than under `mnemos
// token` so the agent surface stays self-contained — issuing an agent
// token requires looking the agent up to fetch its scopes, which is
// agent-shaped, not token-shaped.
func handleAgent(args []string, _ Flags) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: agent requires a subcommand")
		fmt.Fprintln(os.Stderr, "  mnemos agent create --name <n> --owner <user-id> --scope <s> [--scope <s>...] [--run <id>...]")
		fmt.Fprintln(os.Stderr, "  mnemos agent list")
		fmt.Fprintln(os.Stderr, "  mnemos agent revoke <agent-id>")
		fmt.Fprintln(os.Stderr, "  mnemos agent token issue --agent <id> [--ttl <duration>]")
		fmt.Fprintln(os.Stderr, "  mnemos agent heal --claim <id> --statement \"<new truth>\" [--reason \"...\"] [--json]")
		os.Exit(int(ExitUsage))
	}
	switch args[0] {
	case "create":
		handleAgentCreate(args[1:])
	case "list":
		handleAgentList(args[1:])
	case "revoke":
		handleAgentRevoke(args[1:])
	case "token":
		handleAgentToken(args[1:])
	case "heal":
		handleAgentHeal(args[1:])
	default:
		exitWithMnemosError(false, NewUserError("unknown agent subcommand %q", args[0]))
	}
}

func handleAgentCreate(args []string) {
	name, owner := "", ""
	var scopes, runs []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--name":
			if i+1 >= len(args) {
				exitWithMnemosError(false, NewUserError("--name requires a value"))
				return
			}
			name = args[i+1]
			i++
		case "--owner":
			if i+1 >= len(args) {
				exitWithMnemosError(false, NewUserError("--owner requires a value"))
				return
			}
			owner = args[i+1]
			i++
		case "--scope":
			if i+1 >= len(args) {
				exitWithMnemosError(false, NewUserError("--scope requires a value"))
				return
			}
			scopes = append(scopes, args[i+1])
			i++
		case "--run":
			if i+1 >= len(args) {
				exitWithMnemosError(false, NewUserError("--run requires a value"))
				return
			}
			runs = append(runs, args[i+1])
			i++
		default:
			exitWithMnemosError(false, NewUserError("unknown flag %q", args[i]))
			return
		}
	}
	if strings.TrimSpace(name) == "" || strings.TrimSpace(owner) == "" {
		exitWithMnemosError(false, NewUserError("--name and --owner are both required"))
		return
	}

	conn, err := openConn(context.Background())
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "open database"))
		return
	}
	defer closeConn(conn)
	ctx := context.Background()

	// Resolve owner up front so the FK error message stays a friendly
	// "user not found" rather than a raw constraint violation.
	if _, err := conn.Users.GetByID(ctx, owner); err != nil {
		exitWithMnemosError(false, NewUserError("owner user %q not found — create it with 'mnemos user create'", owner))
		return
	}

	id, err := newAgentID()
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "generate agent id"))
		return
	}
	agent := domain.Agent{
		ID:          id,
		Name:        name,
		OwnerID:     owner,
		Scopes:      scopes,
		AllowedRuns: runs,
		Status:      domain.AgentStatusActive,
		CreatedAt:   time.Now().UTC(),
	}
	if err := conn.Agents.Create(ctx, agent); err != nil {
		exitWithMnemosError(false, NewSystemError(err, "create agent"))
		return
	}
	scopeDisplay := strings.Join(scopes, ",")
	if scopeDisplay == "" {
		scopeDisplay = "(none)"
	}
	runDisplay := strings.Join(runs, ",")
	if runDisplay == "" {
		runDisplay = "* (any)"
	}
	fmt.Printf("agent_id=%s name=%q owner=%s scopes=%s runs=%s status=%s\n",
		agent.ID, agent.Name, agent.OwnerID, scopeDisplay, runDisplay, agent.Status)
	fmt.Println("\nNext: mnemos agent token issue --agent " + agent.ID + " — to mint a JWT for this agent.")
}

func handleAgentList(args []string) {
	if len(args) > 0 {
		exitWithMnemosError(false, NewUserError("agent list takes no arguments"))
		return
	}
	conn, err := openConn(context.Background())
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "open database"))
		return
	}
	defer closeConn(conn)

	agents, err := conn.Agents.List(context.Background())
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "list agents"))
		return
	}
	if len(agents) == 0 {
		fmt.Println("(no agents)")
		return
	}
	fmt.Printf("%-26s %-12s %-26s %-22s %-22s %s\n", "ID", "STATUS", "OWNER", "NAME", "SCOPES", "ALLOWED_RUNS")
	for _, a := range agents {
		runs := strings.Join(a.AllowedRuns, ",")
		if runs == "" {
			runs = "*"
		}
		fmt.Printf("%-26s %-12s %-26s %-22s %-22s %s\n",
			a.ID, a.Status, a.OwnerID, truncate(a.Name, 22),
			truncate(strings.Join(a.Scopes, ","), 22), runs)
	}
}

func handleAgentRevoke(args []string) {
	if len(args) != 1 {
		exitWithMnemosError(false, NewUserError("agent revoke <agent-id>"))
		return
	}
	id := args[0]

	conn, err := openConn(context.Background())
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "open database"))
		return
	}
	defer closeConn(conn)

	if err := conn.Agents.UpdateStatus(context.Background(), id, domain.AgentStatusRevoked); err != nil {
		exitWithMnemosError(false, NewSystemError(err, "revoke agent"))
		return
	}
	fmt.Printf("agent_id=%s status=revoked\n", id)
	fmt.Println("Note: the agent's existing JWTs remain valid until they expire.")
	fmt.Println("To revoke individual tokens, use: mnemos token revoke <jti>")
}

func handleAgentToken(args []string) {
	if len(args) == 0 || args[0] != "issue" {
		exitWithMnemosError(false, NewUserError("agent token issue --agent <id> [--ttl <duration>]"))
		return
	}
	args = args[1:]

	agentID := ""
	tenant := ""
	ttl := defaultTokenTTL
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--agent":
			if i+1 >= len(args) {
				exitWithMnemosError(false, NewUserError("--agent requires a value"))
				return
			}
			agentID = args[i+1]
			i++
		case "--tenant":
			if i+1 >= len(args) {
				exitWithMnemosError(false, NewUserError("--tenant requires a value"))
				return
			}
			tenant = args[i+1]
			i++
		case "--ttl":
			if i+1 >= len(args) {
				exitWithMnemosError(false, NewUserError("--ttl requires a duration like 24h"))
				return
			}
			d, err := time.ParseDuration(args[i+1])
			if err != nil {
				exitWithMnemosError(false, NewUserError("invalid --ttl: %v", err))
				return
			}
			if d <= 0 {
				exitWithMnemosError(false, NewUserError("--ttl must be positive"))
				return
			}
			ttl = d
			i++
		default:
			exitWithMnemosError(false, NewUserError("unknown flag %q", args[i]))
			return
		}
	}
	if strings.TrimSpace(agentID) == "" {
		exitWithMnemosError(false, NewUserError("--agent is required"))
		return
	}
	tenant = strings.TrimSpace(tenant)
	if tenant != "" && !validTenantID(tenant) {
		exitWithMnemosError(false, NewUserError("invalid --tenant %q (allowed: letters, digits, . _ : -, max 128)", tenant))
		return
	}

	conn, err := openConn(context.Background())
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "open database"))
		return
	}
	defer closeConn(conn)

	agent, err := conn.Agents.GetByID(context.Background(), agentID)
	if err != nil {
		exitWithMnemosError(false, NewUserError("agent %s not found", agentID))
		return
	}
	if agent.Status != domain.AgentStatusActive {
		exitWithMnemosError(false, NewUserError("agent %s is %s; cannot issue token", agentID, agent.Status))
		return
	}

	secret, err := loadJWTSecret()
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "load JWT secret"))
		return
	}
	token, jti, err := auth.NewIssuer(secret).IssueAgentTokenFull(agent.ID, agent.Scopes, agent.AllowedRuns, tenant, ttl)
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "issue token"))
		return
	}

	runDisplay := strings.Join(agent.AllowedRuns, ",")
	if runDisplay == "" {
		runDisplay = "* (any)"
	}
	fmt.Printf("agent=%s jti=%s expires_in=%s scopes=%s runs=%s\n",
		agent.ID, jti, ttl, strings.Join(agent.Scopes, ","), runDisplay)
	fmt.Println("\nToken (save this — it will not be shown again):")
	fmt.Println(token)
}

func newAgentID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "agt_" + hex.EncodeToString(b), nil
}

// handleAgentHeal implements the Agent Self-Healing Loop:
//
//	mnemos agent heal --claim <id> --statement "<new truth>" [--reason "..."] [--json]
//
// When an agent receives a verdict with action=update, it calls this
// command to update the stale claim in Mnemos and confirm the new
// trust score. The workflow is:
//
//	Query → Verdict(action=update) → Agent calls heal → Mnemos updated
//
// The command:
//  1. Fetches the existing claim by ID.
//  2. Replaces its Text with the new statement.
//  3. Upserts it with the provided reason so the audit trail captures why.
//  4. Calls WhyTrustClaim to compute and return the updated trust report.
//
// Output is either human-readable or JSON (--json).
func handleAgentHeal(args []string) {
	claimID, statement, reason := "", "", ""
	jsonOut := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--claim":
			if i+1 >= len(args) {
				exitWithMnemosError(false, NewUserError("--claim requires a claim ID"))
				return
			}
			claimID = args[i+1]
			i++
		case "--statement":
			if i+1 >= len(args) {
				exitWithMnemosError(false, NewUserError("--statement requires a value"))
				return
			}
			statement = args[i+1]
			i++
		case "--reason":
			if i+1 >= len(args) {
				exitWithMnemosError(false, NewUserError("--reason requires a value"))
				return
			}
			reason = args[i+1]
			i++
		case "--json":
			jsonOut = true
		default:
			exitWithMnemosError(false, NewUserError("unknown heal flag %q", args[i]))
			return
		}
	}
	if strings.TrimSpace(claimID) == "" {
		exitWithMnemosError(false, NewUserError("--claim is required"))
		return
	}
	if strings.TrimSpace(statement) == "" {
		exitWithMnemosError(false, NewUserError("--statement is required"))
		return
	}
	if reason == "" {
		reason = "agent self-healing: belief updated after verdict action=update"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	gw, err := openWriter(ctx)
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "open database"))
		return
	}
	defer closeWriter(gw)
	conn := gw.Conn()

	// 1. Fetch the existing claim.
	claims, err := conn.Claims.ListByIDs(ctx, []string{claimID})
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "look up claim"))
		return
	}
	if len(claims) == 0 {
		exitWithMnemosError(false, NewUserError("claim %q not found", claimID))
		return
	}
	claim := claims[0]
	oldText := claim.Text

	// 2. Replace the claim text with the agent's corrected statement.
	claim.Text = strings.TrimSpace(statement)

	// 3. Persist the update with an audit reason.
	if _, err := gw.Claims(ctx, []domain.Claim{claim}, govwrite.ClaimReason{Reason: reason}); err != nil {
		exitWithMnemosError(false, NewSystemError(err, "update claim"))
		return
	}

	// 4. Compute the updated trust report so the agent can confirm
	//    the heal was accepted and see the new trust score.
	eng := query.NewEngine(conn.Events, conn.Claims, conn.Relationships)
	report, err := eng.WhyTrustClaim(ctx, claimID)
	if err != nil {
		// Non-fatal: heal succeeded but trust report failed.
		// Emit a minimal result rather than masking the update.
		if jsonOut {
			emitJSON(map[string]any{
				"action":      "healed",
				"claim_id":    claimID,
				"old_text":    oldText,
				"new_text":    claim.Text,
				"trust_score": nil,
				"trust_error": err.Error(),
			})
		} else {
			fmt.Printf("healed claim_id=%s\n", claimID)
			fmt.Printf("  old: %s\n", oldText)
			fmt.Printf("  new: %s\n", claim.Text)
			fmt.Printf("  reason: %s\n", reason)
			fmt.Printf("  trust_score: (unavailable: %v)\n", err)
		}
		return
	}

	if jsonOut {
		emitJSON(map[string]any{
			"action":      "healed",
			"claim_id":    claimID,
			"old_text":    oldText,
			"new_text":    claim.Text,
			"trust_score": report.Score,
			"signals":     report.Signals,
		})
		return
	}

	fmt.Printf("healed claim_id=%s trust_score=%.3f\n", claimID, report.Score)
	fmt.Printf("  old: %s\n", oldText)
	fmt.Printf("  new: %s\n", claim.Text)
	fmt.Printf("  reason: %s\n", reason)
	if len(report.Signals) > 0 {
		fmt.Println("  trust signals:")
		for _, s := range report.Signals {
			fmt.Printf("    • %s: %.3f (weight=%.2f, contribution=%.3f)", s.Name, s.Value, s.Weight, s.Contribution)
			if s.Detail != "" {
				fmt.Printf(" — %s", s.Detail)
			}
			fmt.Println()
		}
	}
}
