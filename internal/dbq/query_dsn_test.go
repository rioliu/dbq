package dbq_test

import (
	"strings"
	"testing"

	"github.com/rioliu/dbq/internal/dbq"

	"github.com/go-sql-driver/mysql"
)

// Regression: building mysql.Config manually zeroed AllowNativePasswords,
// so any account using the mysql_native_password plugin failed the handshake
// with "this user requires mysql native password authentication".
func TestMySQLConfigAllowsNativePassword(t *testing.T) {
	p := dbq.Profile{Type: "mysql", Host: "h", Database: "d", User: "u", Password: "pw"}
	cfg, err := dbq.MySQLConfig(p)
	if err != nil {
		t.Fatalf("MySQLConfig: %v", err)
	}
	if !cfg.AllowNativePasswords {
		t.Fatal("AllowNativePasswords must be true (driver default)")
	}
	if !cfg.ParseTime {
		t.Fatal("ParseTime must be true for DATETIME scanning")
	}

	dsn := cfg.FormatDSN()
	if strings.Contains(dsn, "allowNativePasswords=false") {
		t.Fatalf("DSN disables native passwords: %s", dsn)
	}
	// Round-trip: the serialized DSN must parse back with the flag intact.
	parsed, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}
	if !parsed.AllowNativePasswords {
		t.Fatalf("round-trip lost AllowNativePasswords: %s", dsn)
	}
	if parsed.DBName != "d" || parsed.User != "u" {
		t.Fatalf("round-trip lost fields: %+v", parsed)
	}
}

func TestMySQLConfigDialTimeout(t *testing.T) {
	p := dbq.Profile{Type: "mysql", Host: "h", Database: "d", TimeoutSeconds: 7}
	cfg, err := dbq.MySQLConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Timeout.Seconds() != 7 {
		t.Fatalf("dial timeout = %v, want 7s", cfg.Timeout)
	}
}
