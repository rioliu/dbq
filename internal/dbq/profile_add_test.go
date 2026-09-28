package dbq_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/rioliu/dbq/internal/dbq"
)

func TestBuildProfileBlockRoundTrip(t *testing.T) {
	p := dbq.Profile{
		Type: "mysql", Host: "db1.example.com", Port: 3306,
		User: "agent_ro", Password: `p"a\ss'word` + "\n",
		Database: "appdb",
	}
	block := dbq.BuildProfileBlock("prod-mysql", p)
	if !strings.Contains(block, "[profiles.prod-mysql]") {
		t.Fatalf("missing section header:\n%s", block)
	}

	var cfg dbq.Config
	if _, err := toml.Decode(block, &cfg); err != nil {
		t.Fatalf("generated block does not parse: %v\n%s", err, block)
	}
	got := cfg.Profiles["prod-mysql"]
	if got.Host != p.Host || got.User != p.User || got.Password != p.Password ||
		got.Database != p.Database || got.Port != p.Port {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestBuildProfileBlockVariants(t *testing.T) {
	cases := []struct {
		name    string
		p       dbq.Profile
		want    []string
		notWant []string
	}{
		{
			name: "sqlite",
			p:    dbq.Profile{Type: "sqlite", Path: "/data/x.db", ReadOnly: boolPtr(false)},
			want: []string{`path = '/data/x.db'`, "readonly = false"},
			// no host/user/password lines for sqlite
			notWant: []string{"host =", "user =", "password"},
		},
		{
			name:    "password_env over inline",
			p:       dbq.Profile{Type: "postgres", Host: "h", Database: "d", User: "u", PasswordEnv: "DBPASS_X", SSLMode: "require"},
			want:    []string{`password_env = 'DBPASS_X'`, `sslmode = 'require'`, "readonly = true"},
			notWant: []string{"password ="},
		},
		{
			name:    "oracle sid",
			p:       dbq.Profile{Type: "oracle", Host: "h", SID: "ORCL", User: "u", Password: "p"},
			want:    []string{`sid = 'ORCL'`, "readonly = true"},
			notWant: []string{"database ="},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			block := dbq.BuildProfileBlock("x", c.p)
			for _, w := range c.want {
				if !strings.Contains(block, w) {
					t.Errorf("missing %q in:\n%s", w, block)
				}
			}
			for _, w := range c.notWant {
				if strings.Contains(block, w) {
					t.Errorf("unexpected %q in:\n%s", w, block)
				}
			}
			var cfg dbq.Config
			if _, err := toml.Decode(block, &cfg); err != nil {
				t.Errorf("block does not parse: %v", err)
			}
		})
	}
}

func TestTOMLStringQuoting(t *testing.T) {
	p := dbq.Profile{Type: "mysql", Host: "h", Database: "d", User: "u",
		Password: "simple"} // no specials -> literal string
	block := dbq.BuildProfileBlock("x", p)
	if !strings.Contains(block, "password = 'simple'") {
		t.Fatalf("expected literal string quoting:\n%s", block)
	}
}

func TestAppendProfileCreatesSecureFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "profiles.toml")
	p := dbq.Profile{Type: "mysql", Host: "h", Database: "d", User: "u", Password: "pw"}
	if err := dbq.AppendProfile(path, "new-one", p); err != nil {
		t.Fatalf("append: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %04o, want 0600", fi.Mode().Perm())
	}
	di, _ := os.Stat(filepath.Dir(path))
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %04o, want 0700", di.Mode().Perm())
	}

	cfg, err := dbq.Load(path)
	if err != nil {
		t.Fatalf("load after append: %v", err)
	}
	if cfg.Profiles["new-one"].Host != "h" {
		t.Fatalf("profile not loadable: %+v", cfg.Profiles)
	}
}

func TestAppendProfilePreservesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.toml")
	orig := "# my comment\n[defaults]\nmax_rows = 42\n"
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	p := dbq.Profile{Type: "sqlite", Path: "/tmp/a.db"}
	if err := dbq.AppendProfile(path, "second", p); err != nil {
		t.Fatalf("append: %v", err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "# my comment") || !strings.Contains(string(b), "max_rows = 42") {
		t.Fatalf("existing content lost:\n%s", b)
	}
	cfg, err := dbq.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Defaults.MaxRows != 42 || cfg.Profiles["second"].Path != "/tmp/a.db" {
		t.Fatalf("merge broken: %+v", cfg)
	}
}

func TestAppendProfileRejectsDuplicate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.toml")
	p := dbq.Profile{Type: "sqlite", Path: "/tmp/a.db"}
	if err := dbq.AppendProfile(path, "dup", p); err != nil {
		t.Fatal(err)
	}
	if err := dbq.AppendProfile(path, "dup", p); err == nil {
		t.Fatal("duplicate must be rejected")
	}
}

func TestAppendProfileRejectsInsecureExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.toml")
	if err := os.WriteFile(path, []byte("# x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := dbq.Profile{Type: "sqlite", Path: "/tmp/a.db"}
	if err := dbq.AppendProfile(path, "x", p); err == nil {
		t.Fatal("must reject insecure existing file")
	}
}

func TestAppendProfileRejectsBadName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.toml")
	p := dbq.Profile{Type: "sqlite", Path: "/tmp/a.db"}
	for _, bad := range []string{"", "has space", "a.b", `q"uote`} {
		if err := dbq.AppendProfile(path, bad, p); err == nil {
			t.Errorf("name %q must be rejected", bad)
		}
	}
}

func sqlserverTLSProfile(tlsMin string) dbq.Profile {
	return dbq.Profile{Type: "sqlserver", Host: "192.168.21.102", Port: 1433,
		User: "agent_ro", Password: "pw", Database: "jotmanager", TLSMinVersion: tlsMin}
}

func TestBuildProfileBlockTLSMinVersion(t *testing.T) {
	block := dbq.BuildProfileBlock("mssql", sqlserverTLSProfile("1.0"))
	if !strings.Contains(block, "tls_min_version = '1.0'") {
		t.Fatalf("missing tls_min_version line:\n%s", block)
	}
	var cfg dbq.Config
	if _, err := toml.Decode(block, &cfg); err != nil {
		t.Fatalf("block does not parse: %v", err)
	}
	if got := cfg.Profiles["mssql"].TLSMinVersion; got != "1.0" {
		t.Fatalf("round-trip TLSMinVersion = %q, want 1.0", got)
	}

	block = dbq.BuildProfileBlock("mssql", sqlserverTLSProfile(""))
	if strings.Contains(block, "tls_min_version") {
		t.Fatalf("unexpected tls_min_version line:\n%s", block)
	}
}

func TestAppendProfileTLSMinVersionValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.toml")
	if err := dbq.AppendProfile(path, "mssql", sqlserverTLSProfile("1.0")); err != nil {
		t.Fatalf("valid tls_min_version=1.0 rejected: %v", err)
	}
	for i, bad := range []string{"2.0", "1", "tls1.2", "TLSv1.2"} {
		name := "mssqld" + string(rune('a'+i))
		if err := dbq.AppendProfile(path, name, sqlserverTLSProfile(bad)); err == nil {
			t.Errorf("tls_min_version %q must be rejected", bad)
		}
	}
	// other engines do not use tls_min_version
	my := dbq.Profile{Type: "mysql", Host: "h", Database: "d", User: "u", Password: "p", TLSMinVersion: "1.0"}
	if err := dbq.AppendProfile(path, "my-tls", my); err == nil {
		t.Error("tls_min_version on mysql must be rejected")
	}

	cfg, err := dbq.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.Profiles["mssql"].TLSMinVersion; got != "1.0" {
		t.Fatalf("loaded TLSMinVersion = %q, want 1.0", got)
	}
}

func TestAppendProfileValidatesProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.toml")
	// mysql without host
	if err := dbq.AppendProfile(path, "broken", dbq.Profile{Type: "mysql"}); err == nil {
		t.Fatal("invalid profile must be rejected before writing")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("file must not be created for invalid profiles")
	}
}

func sqlserverEncryptProfile(enc string) dbq.Profile {
	p := sqlserverTLSProfile("")
	p.Encrypt = enc
	return p
}

func TestBuildProfileBlockEncrypt(t *testing.T) {
	block := dbq.BuildProfileBlock("mssql", sqlserverEncryptProfile("disable"))
	if !strings.Contains(block, "encrypt = 'disable'") {
		t.Fatalf("missing encrypt line:\n%s", block)
	}
	var cfg dbq.Config
	if _, err := toml.Decode(block, &cfg); err != nil {
		t.Fatalf("block does not parse: %v", err)
	}
	if got := cfg.Profiles["mssql"].Encrypt; got != "disable" {
		t.Fatalf("round-trip Encrypt = %q, want disable", got)
	}
	block = dbq.BuildProfileBlock("mssql", sqlserverEncryptProfile(""))
	if strings.Contains(block, "encrypt =") {
		t.Fatalf("unexpected encrypt line:\n%s", block)
	}
}

func TestAppendProfileEncryptValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.toml")
	if err := dbq.AppendProfile(path, "mssql", sqlserverEncryptProfile("disable")); err != nil {
		t.Fatalf("valid encrypt=disable rejected: %v", err)
	}
	for i, bad := range []string{"yes", "plaintext", "off", "true"} {
		name := "enc" + string(rune('a'+i))
		if err := dbq.AppendProfile(path, name, sqlserverEncryptProfile(bad)); err == nil {
			t.Errorf("encrypt %q must be rejected", bad)
		}
	}
	// other engines do not use encrypt
	my := dbq.Profile{Type: "mysql", Host: "h", Database: "d", User: "u", Password: "p", Encrypt: "disable"}
	if err := dbq.AppendProfile(path, "my-enc", my); err == nil {
		t.Error("encrypt on mysql must be rejected")
	}
	cfg, err := dbq.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.Profiles["mssql"].Encrypt; got != "disable" {
		t.Fatalf("loaded Encrypt = %q, want disable", got)
	}
}
