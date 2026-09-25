package dbq_test

import (
	"os"
	"strings"
	"testing"

	"github.com/rioliu/dbq/internal/dbq"
)

const sampleProfiles = `# dbq profiles
[defaults]
max_rows = 42
readonly = true

# primary database
[profiles.prod]
type = 'mysql'
host = 'db1.internal'
port = 3306
user = 'agent_ro'
password = 'oldpw'
database = 'appdb'
readonly = true

[profiles.audit]
type = 'sqlite'
path = '/var/lib/audit.db'
max_rows = 5000
readonly = false
`

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestUpdateProfileChangesValuesAndPreservesRest(t *testing.T) {
	path := writeProfiles(t, sampleProfiles, 0o600)
	p := dbq.Profile{Type: "mysql", Host: "db2.internal", Port: 3307,
		User: "agent_ro", Password: "newpw", Database: "appdb"}

	changed, err := dbq.UpdateProfile(path, "prod", p)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true")
	}
	got := readFile(t, path)

	for _, want := range []string{
		"host = 'db2.internal'",
		"port = 3307",
		"password = 'newpw'",
		"# dbq profiles",             // header comment
		"# primary database",         // comment above the block
		"max_rows = 42",              // [defaults] untouched
		"max_rows = 5000",            // other profile's unmanaged key
		"path = '/var/lib/audit.db'", // other profile untouched
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q after update:\n%s", want, got)
		}
	}
	if strings.Contains(got, "host = 'db1.internal'") {
		t.Errorf("old host not replaced:\n%s", got)
	}

	cfg, err := dbq.Load(path)
	if err != nil {
		t.Fatalf("load after update: %v", err)
	}
	if cfg.Profiles["prod"].Host != "db2.internal" || cfg.Profiles["prod"].Port != 3307 {
		t.Errorf("prod not updated: %+v", cfg.Profiles["prod"])
	}
	if cfg.Defaults.MaxRows != 42 {
		t.Errorf("defaults lost: %+v", cfg.Defaults)
	}
}

func TestUpdateProfilePasswordToEnvRemovesInlinePassword(t *testing.T) {
	path := writeProfiles(t, sampleProfiles, 0o600)
	p := dbq.Profile{Type: "mysql", Host: "db1.internal", Port: 3306,
		User: "agent_ro", PasswordEnv: "DBQ_PW_PROD", Database: "appdb"}

	if _, err := dbq.UpdateProfile(path, "prod", p); err != nil {
		t.Fatalf("update: %v", err)
	}
	got := readFile(t, path)
	if strings.Contains(got, "password = 'oldpw'") {
		t.Errorf("inline password not removed:\n%s", got)
	}
	if !strings.Contains(got, "password_env = 'DBQ_PW_PROD'") {
		t.Errorf("password_env missing:\n%s", got)
	}
	cfg, err := dbq.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Profiles["prod"].PasswordEnv != "DBQ_PW_PROD" || cfg.Profiles["prod"].Password != "" {
		t.Errorf("unexpected creds: %+v", cfg.Profiles["prod"])
	}
}

func TestUpdateProfilePreservesUnmanagedKeys(t *testing.T) {
	path := writeProfiles(t, sampleProfiles, 0o600)
	p := dbq.Profile{Type: "sqlite", Path: "/srv/new-audit.db", ReadOnly: boolPtr(false)}

	changed, err := dbq.UpdateProfile(path, "audit", p)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true")
	}
	got := readFile(t, path)
	if !strings.Contains(got, "max_rows = 5000") {
		t.Errorf("unmanaged max_rows lost:\n%s", got)
	}
	if !strings.Contains(got, "path = '/srv/new-audit.db'") {
		t.Errorf("path not updated:\n%s", got)
	}
}

func TestUpdateProfileTypeChangeClearsStaleKeys(t *testing.T) {
	path := writeProfiles(t, sampleProfiles, 0o600)
	p := dbq.Profile{Type: "sqlite", Path: "/tmp/prod.db", ReadOnly: boolPtr(true)}

	if _, err := dbq.UpdateProfile(path, "prod", p); err != nil {
		t.Fatalf("update: %v", err)
	}
	got := readFile(t, path)
	prod := got[strings.Index(got, "[profiles.prod]"):strings.Index(got, "[profiles.audit]")]
	for _, stale := range []string{"host =", "port =", "user =", "password =", "database ="} {
		if strings.Contains(prod, stale) {
			t.Errorf("stale key %q left in prod block:\n%s", stale, prod)
		}
	}
	if !strings.Contains(prod, "path = '/tmp/prod.db'") {
		t.Errorf("path missing in prod block:\n%s", prod)
	}
}

func TestUpdateProfileNoChanges(t *testing.T) {
	path := writeProfiles(t, sampleProfiles, 0o600)
	before := readFile(t, path)
	p := dbq.Profile{Type: "sqlite", Path: "/var/lib/audit.db", ReadOnly: boolPtr(false)}

	changed, err := dbq.UpdateProfile(path, "audit", p)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if changed {
		t.Fatal("expected changed=false for identical values")
	}
	if readFile(t, path) != before {
		t.Fatal("file must not be rewritten on no-op")
	}
}

func TestUpdateProfileQuotedSectionHeader(t *testing.T) {
	path := writeProfiles(t, "[profiles.\"my-prod\"]\ntype = 'sqlite'\npath = '/a.db'\nreadonly = true\n", 0o600)
	p := dbq.Profile{Type: "sqlite", Path: "/b.db", ReadOnly: boolPtr(true)}

	changed, err := dbq.UpdateProfile(path, "my-prod", p)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true")
	}
	if !strings.Contains(readFile(t, path), "path = '/b.db'") {
		t.Fatalf("quoted header block not updated:\n%s", readFile(t, path))
	}
}

func TestUpdateProfileUnknownProfile(t *testing.T) {
	path := writeProfiles(t, sampleProfiles, 0o600)
	p := dbq.Profile{Type: "sqlite", Path: "/x.db"}
	if _, err := dbq.UpdateProfile(path, "nope", p); err == nil ||
		!strings.Contains(err.Error(), "not found") {
		t.Fatalf("want not-found error, got %v", err)
	}
}

func TestUpdateProfileRejectsInsecureFile(t *testing.T) {
	path := writeProfiles(t, sampleProfiles, 0o600)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	p := dbq.Profile{Type: "sqlite", Path: "/x.db"}
	if _, err := dbq.UpdateProfile(path, "prod", p); err == nil ||
		!strings.Contains(err.Error(), "insecure permissions") {
		t.Fatalf("want insecure-permissions error, got %v", err)
	}
}

func TestUpdateProfileValidatesBeforeWriting(t *testing.T) {
	path := writeProfiles(t, sampleProfiles, 0o600)
	before := readFile(t, path)
	// mysql without database is invalid
	p := dbq.Profile{Type: "mysql", Host: "h"}
	if _, err := dbq.UpdateProfile(path, "prod", p); err == nil {
		t.Fatal("invalid profile must be rejected")
	}
	if readFile(t, path) != before {
		t.Fatal("file must be untouched after validation failure")
	}
}

func TestUpdateProfileKeepsFileMode(t *testing.T) {
	path := writeProfiles(t, sampleProfiles, 0o600)
	p := dbq.Profile{Type: "sqlite", Path: "/changed.db", ReadOnly: boolPtr(false)}
	if _, err := dbq.UpdateProfile(path, "audit", p); err != nil {
		t.Fatalf("update: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %04o, want 0600", fi.Mode().Perm())
	}
}

func TestUpdateProfileRejectsBadName(t *testing.T) {
	path := writeProfiles(t, sampleProfiles, 0o600)
	p := dbq.Profile{Type: "sqlite", Path: "/x.db"}
	for _, bad := range []string{"", "has space", "a.b"} {
		if _, err := dbq.UpdateProfile(path, bad, p); err == nil {
			t.Errorf("name %q must be rejected", bad)
		}
	}
}
