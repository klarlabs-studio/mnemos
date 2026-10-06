package store

import (
	"regexp"
	"strings"
)

// dsnPasswordRE matches the "://user:password@" segment of a URL DSN. The
// username is any run of non-":/@" characters, the password any run of non-"@/"
// characters — so it masks the whole password (percent-encoded special chars
// included) and stops at the credential-terminating "@".
var dsnPasswordRE = regexp.MustCompile(`(://[^:/@]+:)[^@/]*(@)`)

// driverPasswordRE matches the scheme-less driver form "user:password@tcp(…)"
// (or unix(…), or any "@proto(" address) that go-sql-driver/mysql consumes.
// The MySQL provider builds this form internally, so it reaches error messages
// even though no user ever types it.
var driverPasswordRE = regexp.MustCompile(`^([^:/@()\s]+:)[^@]*(@[a-z]+\()`)

// sensitiveParams are query-parameter names whose value is a credential,
// compared case-insensitively. libSQL/Turso carries its token as ?authToken=,
// libpq accepts ?password= and ?sslpassword=, and the rest are names a DSN for
// some driver or proxy is known to use.
var sensitiveParams = map[string]bool{
	"authtoken": true, "auth_token": true, "token": true, "access_token": true,
	"password": true, "passwd": true, "pwd": true, "pass": true, "sslpassword": true,
	"secret": true, "client_secret": true, "apikey": true, "api_key": true, "key": true,
}

// RedactDSN masks every credential in a DSN so it is safe to print in logs,
// error messages and tool output. It never returns a credential. It handles:
//
//   - URL userinfo: scheme://user:password@host → scheme://user:***@host
//   - credential query parameters: ?authToken=…, ?password=…, … → =***
//   - the scheme-less driver form: user:password@tcp(host)/db → user:***@tcp(host)/db
//
// A credential-free DSN (sqlite://…, memory://, a networked DSN without
// credentials) is returned unchanged. It works on the string rather than via
// url.Parse, so it also redacts DSNs url.Parse rejects — exactly the ones that
// end up in "malformed dsn" errors — and emits "***" literally rather than
// percent-encoded.
func RedactDSN(dsn string) string {
	out := dsnPasswordRE.ReplaceAllString(dsn, "${1}***${2}")
	if !strings.Contains(out, "://") {
		out = driverPasswordRE.ReplaceAllString(out, "${1}***${2}")
	}
	return redactQueryParams(out)
}

// redactQueryParams masks the value of every sensitive query parameter and
// leaves the others, and their order, untouched.
func redactQueryParams(dsn string) string {
	q := strings.IndexByte(dsn, '?')
	if q < 0 {
		return dsn
	}
	query, fragment := dsn[q+1:], ""
	if h := strings.IndexByte(query, '#'); h >= 0 {
		query, fragment = query[:h], query[h:]
	}
	parts := strings.Split(query, "&")
	changed := false
	for i, p := range parts {
		name, _, hasValue := strings.Cut(p, "=")
		if hasValue && sensitiveParams[strings.ToLower(name)] {
			parts[i] = name + "=***"
			changed = true
		}
	}
	if !changed {
		return dsn
	}
	return dsn[:q+1] + strings.Join(parts, "&") + fragment
}
