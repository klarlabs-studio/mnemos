package mysql

import (
	"errors"
	"strings"
	"testing"
)

// A ping failure must report the DSN without its password. DriverDSN is the
// scheme-less "user:password@tcp(...)" form, which the old redaction regex
// could not match, so the password went out in clear.
func TestPingError_RedactsTheDriverDSN(t *testing.T) {
	const canary = "LEAKCANARYping7"
	parsed, err := ParseDSN("mysql://root:" + canary + "@db:3306/app")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(parsed.DriverDSN, canary) {
		t.Fatal("fixture: DriverDSN no longer carries the password, so this test proves nothing")
	}
	msg := pingError(errors.New("connection refused"), parsed).Error()
	if strings.Contains(msg, canary) {
		t.Fatalf("ping error leaks the password: %s", msg)
	}
	if !strings.Contains(msg, "root:***@tcp(db:3306)/app") {
		t.Errorf("ping error lost the diagnostic DSN: %s", msg)
	}
}
