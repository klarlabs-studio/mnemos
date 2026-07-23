package store

import "regexp"

// dsnPasswordRE matches the "://user:password@" segment of a URL DSN. The
// username is any run of non-":/@" characters, the password any run of non-"@/"
// characters — so it masks the whole password (percent-encoded special chars
// included) and stops at the credential-terminating "@".
var dsnPasswordRE = regexp.MustCompile(`(://[^:/@]+:)[^@/]*(@)`)

// RedactDSN masks the password in a URL-form DSN so it is safe to print in logs
// and error messages. It never returns the original password. A credential-free
// DSN — sqlite://…, memory://, or a networked DSN with no "user:pass@" — has no
// match and is returned unchanged. Uses a regex (not url.Parse) so the "***"
// placeholder is emitted literally rather than percent-encoded.
func RedactDSN(dsn string) string {
	return dsnPasswordRE.ReplaceAllString(dsn, "${1}***${2}")
}
