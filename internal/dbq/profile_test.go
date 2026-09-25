package dbq_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rioliu/dbq/internal/dbq"
)

func writeProfiles(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "profiles.toml")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

const sampleTOML = `
[defaults]
max_rows = 500
timeout_seconds = 15
readonly = true

[profiles.dev-sqlite]
type = "sqlite"
path = "/tmp/dev.db"

[profiles.prod-mysql]
type = "mysql"
host = "db1.example.com"
port = 3306
user = "agent_ro"
password = "hunter2"
database = "appdb"

[profiles.admin-pg]
type = "postgres"
host = "db2.example.com"
user = "admin"
password_env = "DBQ_PASS_ADMIN_PG"
database = "appdb"
readonly = false
max_rows = 50
`

func TestLoadValid(t *testing.T) {
	path := writeProfiles(t, sampleTOML, 0o600)
	cfg, err := dbq.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Profiles) != 3 {
		t.Fatalf("want 3 profiles, got %d", len(cfg.Profiles))
	}

	dev := cfg.Profiles["dev-sqlite"]
	if dev.Type != "sqlite" || dev.Path != "/tmp/dev.db" {
		t.Fatalf("sqlite profile wrong: %+v", dev)
	}
	if !dev.ResolvedReadOnly() {
		t.Fatal("defaults.readonly=true should apply")
	}
	if dev.ResolvedMaxRows() != 500 || dev.ResolvedTimeoutSeconds() != 15 {
		t.Fatalf("defaults not applied: rows=%d timeout=%d", dev.ResolvedMaxRows(), dev.ResolvedTimeoutSeconds())
	}

	admin := cfg.Profiles["admin-pg"]
	if admin.ResolvedReadOnly() {
		t.Fatal("profile readonly=false must override defaults")
	}
	if admin.PasswordEnv != "DBQ_PASS_ADMIN_PG" {
		t.Fatalf("password_env not parsed: %+v", admin)
	}
	if admin.ResolvedMaxRows() != 50 {
		t.Fatalf("profile max_rows override lost: %d", admin.ResolvedMaxRows())
	}
}

func TestLoadRejectsInsecurePermissions(t *testing.T) {
	path := writeProfiles(t, sampleTOML, 0o644)
	if _, err := dbq.Load(path); err == nil {
		t.Fatal("must reject profiles readable by group/other")
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := dbq.Load(filepath.Join(t.TempDir(), "nope.toml")); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadUnknownType(t *testing.T) {
	path := writeProfiles(t, "[profiles.x]\ntype = \"mongodb\"\n", 0o600)
	if _, err := dbq.Load(path); err == nil {
		t.Fatal("expected error for unknown type")
	}
}

func TestLoadOracleProfile(t *testing.T) {
	cases := []string{
		"[profiles.o]\ntype = \"oracle\"\nhost = \"db.example.com\"\ndatabase = \"ORCL\"\n",
		"[profiles.o]\ntype = \"oracle\"\nhost = \"db.example.com\"\nsid = \"ORCL\"\n",
	}
	for _, c := range cases {
		path := writeProfiles(t, c, 0o600)
		cfg, err := dbq.Load(path)
		if err != nil {
			t.Fatalf("oracle profile must load: %v", err)
		}
		p := cfg.Profiles["o"]
		if !p.ResolvedReadOnly() {
			t.Fatal("oracle must default to readonly")
		}
	}
	// missing both database and sid
	path := writeProfiles(t, "[profiles.o]\ntype = \"oracle\"\nhost = \"h\"\n", 0o600)
	if _, err := dbq.Load(path); err == nil {
		t.Fatal("oracle without database/sid must fail")
	}
}

func TestLoadMissingRequiredFields(t *testing.T) {
	cases := []string{
		"[profiles.x]\ntype = \"mysql\"\n",                  // no host
		"[profiles.x]\ntype = \"postgres\"\nhost = \"h\"\n", // no database
		"[profiles.x]\ntype = \"sqlite\"\n",                 // no path
		"[profiles.x]\n",                                    // no type
	}
	for _, c := range cases {
		path := writeProfiles(t, c, 0o600)
		if _, err := dbq.Load(path); err == nil {
			t.Fatalf("expected error for: %q", c)
		}
	}
}

func TestResolvePasswordEnv(t *testing.T) {
	path := writeProfiles(t, sampleTOML, 0o600)
	cfg, err := dbq.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	admin := cfg.Profiles["admin-pg"]

	if err := os.Setenv("DBQ_PASS_ADMIN_PG", "s3cret"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Unsetenv("DBQ_PASS_ADMIN_PG") })

	got, err := admin.ResolvePassword()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "s3cret" {
		t.Fatalf("wrong password resolved: %q", got)
	}

	os.Unsetenv("DBQ_PASS_ADMIN_PG")
	if _, err := admin.ResolvePassword(); err == nil {
		t.Fatal("missing env var must be an error")
	}
}
