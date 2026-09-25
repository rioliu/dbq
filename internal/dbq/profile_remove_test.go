package dbq_test

import (
	"os"
	"strings"
	"testing"

	"github.com/rioliu/dbq/internal/dbq"
)

const removeFixture = `# dbq profiles
[defaults]
max_rows = 42

# primary database
[profiles.prod]
type = 'mysql'
host = 'db1.internal'
port = 3306
user = 'agent_ro'
password = 'oldpw'
database = 'appdb'
readonly = true

# audit trail
[profiles.audit]
type = 'sqlite'
path = '/var/lib/audit.db'
max_rows = 5000
readonly = false
`

func TestRemoveProfileMiddleBlock(t *testing.T) {
	path := writeProfiles(t, removeFixture, 0o600)
	if err := dbq.RemoveProfile(path, "prod"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	got := readFile(t, path)

	for _, gone := range []string{
		"[profiles.prod]",
		"# primary database", // comment attached to the removed block
		"oldpw",
	} {
		if strings.Contains(got, gone) {
			t.Errorf("%q must be removed:\n%s", gone, got)
		}
	}
	for _, keep := range []string{
		"# dbq profiles", // file header comment
		"[defaults]",
		"max_rows = 42",
		"# audit trail", // comment belongs to the NEXT profile - must survive
		"path = '/var/lib/audit.db'",
		"max_rows = 5000",
	} {
		if !strings.Contains(got, keep) {
			t.Errorf("%q must be preserved:\n%s", keep, got)
		}
	}

	cfg, err := dbq.Load(path)
	if err != nil {
		t.Fatalf("load after remove: %v", err)
	}
	if _, ok := cfg.Profiles["prod"]; ok {
		t.Error("prod still present after removal")
	}
	if _, ok := cfg.Profiles["audit"]; !ok {
		t.Error("audit profile lost")
	}
	if cfg.Defaults.MaxRows != 42 {
		t.Errorf("defaults lost: %+v", cfg.Defaults)
	}
}

func TestRemoveProfileFirstBlock(t *testing.T) {
	path := writeProfiles(t, removeFixture, 0o600)
	if err := dbq.RemoveProfile(path, "prod"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	got := readFile(t, path)
	if !strings.HasPrefix(got, "# dbq profiles\n") {
		t.Errorf("file header must stay first:\n%s", got)
	}
	if strings.Contains(got, "profiles.prod") {
		t.Errorf("prod still present:\n%s", got)
	}
}

func TestRemoveProfileLastBlock(t *testing.T) {
	fix := "[profiles.a]\ntype = 'sqlite'\npath = '/a.db'\nreadonly = true\n\n[profiles.b]\ntype = 'sqlite'\npath = '/b.db'\nreadonly = true\n"
	path := writeProfiles(t, fix, 0o600)
	if err := dbq.RemoveProfile(path, "b"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	got := readFile(t, path)
	if !strings.Contains(got, "[profiles.a]") || strings.Contains(got, "profiles.b") {
		t.Fatalf("unexpected content:\n%s", got)
	}
	if got != strings.TrimRight(got, "\n")+"\n" {
		t.Errorf("file must end with exactly one newline:\n%q", got)
	}
	cfg, err := dbq.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Profiles) != 1 {
		t.Errorf("want 1 profile, got %d", len(cfg.Profiles))
	}
}

func TestRemoveProfileWithoutBlankSeparator(t *testing.T) {
	fix := "[profiles.a]\ntype = 'sqlite'\npath = '/a.db'\nreadonly = true\n[profiles.b]\ntype = 'sqlite'\npath = '/b.db'\nreadonly = true\n"
	path := writeProfiles(t, fix, 0o600)
	if err := dbq.RemoveProfile(path, "a"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	got := readFile(t, path)
	if !strings.Contains(got, "path = '/b.db'\n\n[") && !strings.HasPrefix(got, "[profiles.b]") {
		t.Errorf("want remaining block to start cleanly:\n%q", got)
	}
	if got != strings.TrimRight(got, "\n")+"\n" {
		t.Errorf("file must end with exactly one newline:\n%q", got)
	}
	if _, err := dbq.Load(path); err != nil {
		t.Fatalf("load: %v", err)
	}
}

func TestRemoveProfileOnlyBlockLeavesEmptyFile(t *testing.T) {
	fix := "# only one\n[profiles.only]\ntype = 'sqlite'\npath = '/x.db'\nreadonly = true\n"
	path := writeProfiles(t, fix, 0o600)
	if err := dbq.RemoveProfile(path, "only"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if got := strings.TrimSpace(readFile(t, path)); got != "" {
		t.Errorf("want empty file, got:\n%s", got)
	}
}

func TestRemoveProfileQuotedHeader(t *testing.T) {
	path := writeProfiles(t, "[profiles.\"my-prod\"]\ntype = 'sqlite'\npath = '/a.db'\nreadonly = true\n\n[profiles.b]\ntype = 'sqlite'\npath = '/b.db'\nreadonly = true\n", 0o600)
	if err := dbq.RemoveProfile(path, "my-prod"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	got := readFile(t, path)
	if strings.Contains(got, "my-prod") || !strings.Contains(got, "[profiles.b]") {
		t.Errorf("unexpected content:\n%s", got)
	}
}

func TestRemoveProfileUnknown(t *testing.T) {
	path := writeProfiles(t, removeFixture, 0o600)
	before := readFile(t, path)
	err := dbq.RemoveProfile(path, "nope")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want not-found error, got %v", err)
	}
	if readFile(t, path) != before {
		t.Fatal("file must be untouched")
	}
}

func TestRemoveProfileRejectsInsecureFile(t *testing.T) {
	path := writeProfiles(t, removeFixture, 0o600)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	err := dbq.RemoveProfile(path, "prod")
	if err == nil || !strings.Contains(err.Error(), "insecure permissions") {
		t.Fatalf("want insecure-permissions error, got %v", err)
	}
}

func TestRemoveProfileRejectsUnparseableFile(t *testing.T) {
	path := writeProfiles(t, "not toml [[[\n", 0o600)
	err := dbq.RemoveProfile(path, "prod")
	if err == nil || !strings.Contains(err.Error(), "does not parse") {
		t.Fatalf("want parse error, got %v", err)
	}
}

func TestRemoveProfileRejectsBadName(t *testing.T) {
	path := writeProfiles(t, removeFixture, 0o600)
	for _, bad := range []string{"", "has space", "a.b"} {
		if err := dbq.RemoveProfile(path, bad); err == nil {
			t.Errorf("name %q must be rejected", bad)
		}
	}
}

func TestRemoveProfileKeepsFileMode(t *testing.T) {
	path := writeProfiles(t, removeFixture, 0o600)
	if err := dbq.RemoveProfile(path, "prod"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %04o, want 0600", fi.Mode().Perm())
	}
}
