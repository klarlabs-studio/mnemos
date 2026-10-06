package main

import (
	"strings"
	"testing"
)

// configure_environment returns the apply result's lines to the MCP client, so
// they land in an agent transcript. A brain that cannot be reached used to be
// reported as "cannot reach brain <raw DSN>", password included.
func TestApplyInitPlan_UnreachableBrainNeverEchoesTheCredential(t *testing.T) {
	const canary = "LEAKCANARYinit42"
	for _, dsn := range []string{
		credDSN("postgres", "thor:"+canary, "127.0.0.1:1/thor?sslmode=disable"),
		credDSN("mysql", "root:"+canary, "127.0.0.1:1/app"),
		"libsql://127.0.0.1:1?authToken=" + canary,
	} {
		r := applyInitPlan(initPlan{dsn: dsn, backend: "postgres"})
		if !r.fail {
			t.Fatalf("%s: expected the unreachable brain to fail the plan; lines=%v", dsn, r.lines)
		}
		for _, line := range r.lines {
			if strings.Contains(line, canary) {
				t.Errorf("result line echoes the credential: %q", line)
			}
		}
	}
}

// credDSN assembles "scheme://userinfo@rest" at run time. Fixture DSNs carry a
// fake credential on purpose; written as one literal they read, to a secret
// scanner, exactly like a committed real one (SEC-073).
func credDSN(scheme, userinfo, rest string) string {
	return scheme + "://" + userinfo + "@" + rest
}
