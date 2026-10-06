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
		"postgres://thor:" + canary + "@127.0.0.1:1/thor?sslmode=disable",
		"mysql://root:" + canary + "@127.0.0.1:1/app",
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
