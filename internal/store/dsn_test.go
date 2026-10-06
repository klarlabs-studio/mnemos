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
		// Forms the original regex missed, every one of which reached an error
		// message with the credential in clear.
		{
			"libsql authToken",
			"libsql://db.turso.io?authToken=" + fixtureSecret,
			"libsql://db.turso.io?authToken=***",
		},
		{
			"libsql authToken, other params kept",
			"libsql://db.turso.io?namespace=a&authToken=" + fixtureSecret + "&tls=1",
			"libsql://db.turso.io?namespace=a&authToken=***&tls=1",
		},
		{
			"param name is case-insensitive",
			"libsql://db.turso.io?AUTHTOKEN=" + fixtureSecret,
			"libsql://db.turso.io?AUTHTOKEN=***",
		},
		{
			"postgres password param",
			"postgres://thor@db/thor?sslmode=require&password=" + fixtureSecret,
			"postgres://thor@db/thor?sslmode=require&password=***",
		},
		{
			"userinfo and param together",
			fixtureDSN("postgres", "thor:"+fixtureSecret, "db/thor?sslpassword="+fixtureSecret),
			"postgres://thor:***@db/thor?sslpassword=***",
		},
		{"mysql driver form", "root:hunter2@tcp(db:3306)/app?parseTime=true", "root:***@tcp(db:3306)/app?parseTime=true"},
		{"mysql driver form, unix socket", "root:hunter2@unix(/tmp/mysql.sock)/app", "root:***@unix(/tmp/mysql.sock)/app"},
		{"driver form without password", "root@tcp(db:3306)/app", "root@tcp(db:3306)/app"},
		{"no query, no credentials", "postgres://db/thor", "postgres://db/thor"},
		{"harmless params untouched", "sqlite:///x.db?_pragma=busy_timeout(5000)", "sqlite:///x.db?_pragma=busy_timeout(5000)"},
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
			for _, secret := range []string{"s3cr3t", "hunter2", fixtureSecret} {
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

// fixtureSecret and fixtureDSN keep fake credentials out of single literals,
// which a secret scanner cannot tell from committed real ones (SEC-073/161).
const fixtureSecret = "fixturesecret"

func fixtureDSN(scheme, userinfo, rest string) string {
	return scheme + "://" + userinfo + "@" + rest
}
