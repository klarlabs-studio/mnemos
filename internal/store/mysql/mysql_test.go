package mysql_test

import (
	"strings"
	"testing"

	"go.klarlabs.de/mnemos/internal/store"
	"go.klarlabs.de/mnemos/internal/store/mysql"
)

func TestParseDSN_DefaultsNamespaceFromPath(t *testing.T) {
	t.Parallel()
	d, err := mysql.ParseDSN("mysql://user:pw@host:3306/cogstack")
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}
	if d.Namespace != "cogstack" {
		t.Errorf("Namespace = %q, want cogstack", d.Namespace)
	}
	if !strings.Contains(d.DriverDSN, "@tcp(host:3306)/cogstack") {
		t.Errorf("DriverDSN = %q, want libmysql tcp form with cogstack db", d.DriverDSN)
	}
	if !strings.Contains(d.DriverDSN, "parseTime=true") {
		t.Errorf("DriverDSN missing parseTime=true: %q", d.DriverDSN)
	}
	if strings.Contains(d.AdminDSN, "/cogstack") {
		t.Errorf("AdminDSN should drop db name: %q", d.AdminDSN)
	}
}

func TestParseDSN_QueryNamespaceWins(t *testing.T) {
	t.Parallel()
	d, err := mysql.ParseDSN("mysql://user:pw@host/some_db?namespace=team_x")
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}
	if d.Namespace != "team_x" {
		t.Errorf("Namespace = %q, want team_x", d.Namespace)
	}
	if strings.Contains(d.DriverDSN, "namespace=") {
		t.Errorf("DriverDSN should not contain namespace=: %q", d.DriverDSN)
	}
}

func TestParseDSN_DefaultsToMnemosWhenAbsent(t *testing.T) {
	t.Parallel()
	d, err := mysql.ParseDSN("mysql://user:pw@host:3306")
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}
	if d.Namespace != "mnemos" {
		t.Errorf("Namespace = %q, want mnemos (default)", d.Namespace)
	}
}

func TestParseDSN_AcceptsMariaDBScheme(t *testing.T) {
	t.Parallel()
	d, err := mysql.ParseDSN("mariadb://user@host/cogstack")
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}
	if d.Namespace != "cogstack" {
		t.Errorf("Namespace = %q", d.Namespace)
	}
}

func TestParseDSN_RejectsInvalidNamespace(t *testing.T) {
	t.Parallel()
	bad := []string{
		"mysql://h/Team-X",
		"mysql://h/1team",
		"mysql://h/?namespace=Team-X",
	}
	for _, dsn := range bad {
		if _, err := mysql.ParseDSN(dsn); err == nil {
			t.Errorf("ParseDSN(%q) accepted invalid namespace", dsn)
		}
	}
}

func TestParseDSN_RejectsNonMysqlScheme(t *testing.T) {
	t.Parallel()
	if _, err := mysql.ParseDSN("postgres://h/d"); err == nil {
		t.Error("ParseDSN accepted postgres:// scheme")
	}
}

func TestSupportedSchemes_IncludesMySQL(t *testing.T) {
	t.Parallel()
	got := store.SupportedSchemes()
	want := map[string]bool{"mysql": false, "mariadb": false}
	for _, s := range got {
		if _, ok := want[s]; ok {
			want[s] = true
		}
	}
	for s, seen := range want {
		if !seen {
			t.Errorf("SupportedSchemes missing %q (got %v)", s, got)
		}
	}
}
