package main

import "testing"

// TestParseQueryArgs_WriteBackFlagsAdvance guards against the infinite-loop
// regression where --salient/--hebbian did not consume their token: a boolean query
// flag placed before the question must be parsed AND leave the question intact.
// (Before the fix, this test would hang.)
func TestParseQueryArgs_WriteBackFlagsAdvance(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want func(queryArgs) bool
	}{
		{"salient", []string{"--salient", "why did it break"}, func(q queryArgs) bool { return q.salient }},
		{"hebbian", []string{"--hebbian", "why did it break"}, func(q queryArgs) bool { return q.hebbian }},
		{"reconsolidate", []string{"--reconsolidate", "why did it break"}, func(q queryArgs) bool { return q.reconsolidate }},
		{"inhibit", []string{"--inhibit", "why did it break"}, func(q queryArgs) bool { return q.inhibit }},
		{"combined", []string{"--salient", "--hebbian", "--inhibit", "why did it break"}, func(q queryArgs) bool { return q.salient && q.hebbian && q.inhibit }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			qa, err := parseQueryArgs(tc.args)
			if err != nil {
				t.Fatalf("parseQueryArgs: %v", err)
			}
			if !tc.want(qa) {
				t.Errorf("flag not set for %v", tc.args)
			}
			if qa.question != "why did it break" {
				t.Errorf("question = %q, want %q (flag token leaked or consumed the question)", qa.question, "why did it break")
			}
		})
	}
}
