package store

import "testing"

func TestRedactDSN(t *testing.T) {
	cases := []struct {
		name string
		dsn  string
		want string
	}{
		{"postgres with password", "postgres://thor:s3cr3t@postgres:5432/thor?sslmode=require", "postgres://thor:***@postgres:5432/thor?sslmode=require"},
		{"mysql with password", "mysql://root:hunter2@db:3306/app", "mysql://root:***@db:3306/app"},
		{"no password", "postgres://thor@postgres:5432/thor", "postgres://thor@postgres:5432/thor"},
		{"sqlite (no credentials)", "sqlite:///var/lib/mnemos/mnemos.db", "sqlite:///var/lib/mnemos/mnemos.db"},
		{"memory", "memory://", "memory://"},
		{"empty password", "postgres://thor:@postgres:5432/thor", "postgres://thor:***@postgres:5432/thor"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := RedactDSN(c.dsn)
			if got == c.dsn && c.want != c.dsn {
				t.Fatalf("RedactDSN(%q) did not redact; got %q", c.dsn, got)
			}
			if got != c.want {
				t.Errorf("RedactDSN(%q) = %q, want %q", c.dsn, got, c.want)
			}
			// The real password must never survive, whatever the form.
			for _, secret := range []string{"s3cr3t", "hunter2"} {
				if contains(c.dsn, secret) && contains(got, secret) {
					t.Errorf("RedactDSN(%q) leaked the password %q: %q", c.dsn, secret, got)
				}
			}
		})
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
