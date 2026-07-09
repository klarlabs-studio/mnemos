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
)

const defaultTokenTTL = 90 * 24 * time.Hour // 90 days

// handleUser dispatches `mnemos user <subcommand>`.
func handleUser(args []string, _ Flags) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: user requires a subcommand")
		fmt.Fprintln(os.Stderr, "  mnemos user create --name <n> --email <e>")
		fmt.Fprintln(os.Stderr, "  mnemos user list")
		fmt.Fprintln(os.Stderr, "  mnemos user revoke <id>")
		os.Exit(int(ExitUsage))
	}
	switch args[0] {
	case "create":
		handleUserCreate(args[1:])
	case "list":
		handleUserList(args[1:])
	case "revoke":
		handleUserRevoke(args[1:])
	default:
		exitWithMnemosError(false, NewUserError("unknown user subcommand %q", args[0]))
	}
}

func handleUserCreate(args []string) {
	name, email := "", ""
	var scopes []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--name":
			if i+1 >= len(args) {
				exitWithMnemosError(false, NewUserError("--name requires a value"))
				return
			}
			name = args[i+1]
			i++
		case "--email":
			if i+1 >= len(args) {
				exitWithMnemosError(false, NewUserError("--email requires a value"))
				return
			}
			email = args[i+1]
			i++
		case "--scope":
			if i+1 >= len(args) {
				exitWithMnemosError(false, NewUserError("--scope requires a value"))
				return
			}
			scopes = append(scopes, args[i+1])
			i++
		default:
			exitWithMnemosError(false, NewUserError("unknown flag %q", args[i]))
			return
		}
	}
	if strings.TrimSpace(name) == "" || strings.TrimSpace(email) == "" {
		exitWithMnemosError(false, NewUserError("--name and --email are both required"))
		return
	}
	// Empty --scope leaves the slice nil; the schema default (["*"])
	// applies on insert and the resulting user keeps full access.
	// Operators who want a least-privilege user must pass explicit
	// --scope arguments.

	conn, err := openConn(context.Background())
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "open database"))
		return
	}
	defer closeConn(conn)

	id, err := newUserID()
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "generate user id"))
		return
	}
	user := domain.User{
		ID:        id,
		Name:      name,
		Email:     email,
		Status:    domain.UserStatusActive,
		Scopes:    scopes,
		CreatedAt: time.Now().UTC(),
	}
	if err := conn.Users.Create(context.Background(), user); err != nil {
		exitWithMnemosError(false, NewSystemError(err, "create user"))
		return
	}
	scopeDisplay := strings.Join(scopes, ",")
	if scopeDisplay == "" {
		scopeDisplay = "* (default)"
	}
	fmt.Printf("user_id=%s name=%q email=%s status=%s scopes=%s\n",
		user.ID, user.Name, user.Email, user.Status, scopeDisplay)
	fmt.Println("\nNext: mnemos token issue --user " + user.ID + " — to mint a JWT for this user.")
}

func handleUserList(args []string) {
	if len(args) > 0 {
		exitWithMnemosError(false, NewUserError("user list takes no arguments"))
		return
	}
	conn, err := openConn(context.Background())
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "open database"))
		return
	}
	defer closeConn(conn)

	users, err := conn.Users.List(context.Background())
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "list users"))
		return
	}
	if len(users) == 0 {
		fmt.Println("(no users)")
		return
	}
	fmt.Printf("%-26s %-12s %-30s %-30s %-26s %s\n", "ID", "STATUS", "NAME", "EMAIL", "SCOPES", "CREATED")
	for _, u := range users {
		scopes := strings.Join(u.Scopes, ",")
		if scopes == "" {
			scopes = "*"
		}
		fmt.Printf("%-26s %-12s %-30s %-30s %-26s %s\n",
			u.ID, u.Status, truncate(u.Name, 30), truncate(u.Email, 30),
			truncate(scopes, 26), u.CreatedAt.Format(time.RFC3339))
	}
}

func handleUserRevoke(args []string) {
	if len(args) != 1 {
		exitWithMnemosError(false, NewUserError("user revoke <user-id>"))
		return
	}
	id := args[0]

	conn, err := openConn(context.Background())
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "open database"))
		return
	}
	defer closeConn(conn)

	if err := conn.Users.UpdateStatus(context.Background(), id, domain.UserStatusRevoked); err != nil {
		exitWithMnemosError(false, NewSystemError(err, "revoke user"))
		return
	}
	fmt.Printf("user_id=%s status=revoked\n", id)
	fmt.Println("Note: the user's existing JWTs remain valid until they expire.")
	fmt.Println("To revoke individual tokens, use: mnemos token revoke <jti>")
}

// handleToken dispatches `mnemos token <subcommand>`.
func handleToken(args []string, _ Flags) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: token requires a subcommand")
		fmt.Fprintln(os.Stderr, "  mnemos token issue --user <id> [--ttl <duration>]")
		fmt.Fprintln(os.Stderr, "  mnemos token revoke <jti>")
		os.Exit(int(ExitUsage))
	}
	switch args[0] {
	case "issue":
		handleTokenIssue(args[1:])
	case "revoke":
		handleTokenRevoke(args[1:])
	default:
		exitWithMnemosError(false, NewUserError("unknown token subcommand %q", args[0]))
	}
}

func handleTokenIssue(args []string) {
	userID := ""
	tenant := ""
	ttl := defaultTokenTTL
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--user":
			if i+1 >= len(args) {
				exitWithMnemosError(false, NewUserError("--user requires a value"))
				return
			}
			userID = args[i+1]
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
	if strings.TrimSpace(userID) == "" {
		exitWithMnemosError(false, NewUserError("--user is required"))
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

	user, err := conn.Users.GetByID(context.Background(), userID)
	if err != nil {
		exitWithMnemosError(false, NewUserError("user %s not found", userID))
		return
	}
	if user.Status != domain.UserStatusActive {
		exitWithMnemosError(false, NewUserError("user %s is %s; cannot issue token", userID, user.Status))
		return
	}

	secret, err := loadJWTSecret()
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "load JWT secret"))
		return
	}
	token, jti, err := auth.NewIssuer(secret).IssueUserTokenWithTenant(user, tenant, ttl)
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "issue token"))
		return
	}

	tenantDisplay := tenant
	if tenantDisplay == "" {
		tenantDisplay = "(none)"
	}
	fmt.Printf("user=%s tenant=%s jti=%s expires_in=%s\n", user.ID, tenantDisplay, jti, ttl)
	fmt.Println("\nToken (save this — it will not be shown again):")
	fmt.Println(token)
}

func handleTokenRevoke(args []string) {
	if len(args) != 1 {
		exitWithMnemosError(false, NewUserError("token revoke <jti>"))
		return
	}
	jti := args[0]

	conn, err := openConn(context.Background())
	if err != nil {
		exitWithMnemosError(false, NewSystemError(err, "open database"))
		return
	}
	defer closeConn(conn)

	// We don't know the original expiry from the JTI alone (the JWT
	// would carry it in claims, but the operator typed the JTI not the
	// token). Use a far-future expires_at so the denylist entry stays
	// in place; periodic purge with a near-now cutoff will leave it
	// alone until then.
	rt := domain.RevokedToken{
		JTI:       jti,
		RevokedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(365 * 24 * time.Hour),
	}
	if err := conn.RevokedTokens.Add(context.Background(), rt); err != nil {
		exitWithMnemosError(false, NewSystemError(err, "revoke token"))
		return
	}
	fmt.Printf("jti=%s revoked\n", jti)
}

// loadJWTSecret resolves the signing secret using the project root
// when one exists, otherwise the user's home directory.
func loadJWTSecret() ([]byte, error) {
	_, projectRoot, _ := findProjectDB()
	path := auth.DefaultSecretPath(projectRoot)
	secret, created, err := auth.LoadOrCreateSecret(path)
	if err != nil {
		return nil, err
	}
	if created {
		fmt.Fprintf(os.Stderr, "warning: generated new JWT secret at %s — any previously-issued tokens are invalid\n", path)
	}
	return secret, nil
}

func newUserID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "usr_" + hex.EncodeToString(b), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
