package dbq

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

const (
	defaultMaxRows     = 1000
	defaultTimeoutSecs = 30
)

// Defaults are baked into every profile at Load time.
type Defaults struct {
	MaxRows        int   `toml:"max_rows"`
	TimeoutSeconds int   `toml:"timeout_seconds"`
	ReadOnly       *bool `toml:"readonly"`
}

// Profile is one database connection. Password comes from Password (inline,
// file is chmod 600) or PasswordEnv (environment variable), never both.
type Profile struct {
	Type           string `toml:"type"` // mysql|postgres|sqlserver|sqlite|oracle
	Host           string `toml:"host"`
	Port           int    `toml:"port"`
	User           string `toml:"user"`
	Password       string `toml:"password"`
	PasswordEnv    string `toml:"password_env"`
	Database       string `toml:"database"`        // service name for oracle
	SID            string `toml:"sid"`             // alternative to database for oracle
	Path           string `toml:"path"`            // sqlite file
	SSLMode        string `toml:"sslmode"`         // postgres only
	TLSMinVersion  string `toml:"tls_min_version"` // sqlserver only: 1.0|1.1|1.2|1.3
	Encrypt        string `toml:"encrypt"`         // sqlserver only: disable|optional|mandatory|strict
	ReadOnly       *bool  `toml:"readonly"`
	MaxRows        int    `toml:"max_rows"`
	TimeoutSeconds int    `toml:"timeout_seconds"`
}

type Config struct {
	Defaults Defaults           `toml:"defaults"`
	Profiles map[string]Profile `toml:"profiles"`
}

var knownTypes = map[string]Dialect{
	"mysql": DialectMySQL, "postgres": DialectPostgres, "sqlserver": DialectSQLServer,
	"sqlite": DialectSQLite, "oracle": DialectOracle,
}

// DefaultPath returns the profile file location ($DBQ_PROFILES overrides).
// Fixed at ~/.config/dbq/profiles.toml on every platform for predictability
// (os.UserConfigDir() would be ~/Library/Application Support on macOS).
func DefaultPath() (string, error) {
	if p := os.Getenv("DBQ_PROFILES"); p != "" {
		return p, nil
	}
	home := os.Getenv("HOME")
	if home == "" {
		return "", fmt.Errorf("HOME is not set; use DBQ_PROFILES to point at the profile file")
	}
	return filepath.Join(home, ".config", "dbq", "profiles.toml"), nil
}

// Load reads the profile file. Refuses group/other-readable permissions.
func Load(path string) (*Config, error) {
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("profile file not found: %s (see SPEC.md to create it)", path)
		}
		return nil, err
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("insecure permissions on %s (%04o) - run: chmod 600 %s", path, fi.Mode().Perm(), path)
	}

	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(cfg.Profiles) == 0 {
		return nil, fmt.Errorf("no profiles defined in %s", path)
	}

	for name, p := range cfg.Profiles {
		var baked = p
		if err := validateAndBake(name, p, &cfg.Defaults, &baked); err != nil {
			return nil, err
		}
		cfg.Profiles[name] = baked
	}
	return &cfg, nil
}

func validateAndBake(name string, p Profile, d *Defaults, out *Profile) error {
	if p.Type == "" {
		return fmt.Errorf("profile %q: missing type", name)
	}
	if _, ok := knownTypes[p.Type]; !ok {
		return fmt.Errorf("profile %q: unknown type %q (want mysql|postgres|sqlserver|sqlite|oracle)", name, p.Type)
	}
	if p.Password != "" && p.PasswordEnv != "" {
		return fmt.Errorf("profile %q: set password or password_env, not both", name)
	}
	if p.TLSMinVersion != "" {
		if p.Type != "sqlserver" {
			return fmt.Errorf("profile %q: tls_min_version is only valid for sqlserver profiles", name)
		}
		if err := ValidateTLSMinVersion(p.TLSMinVersion); err != nil {
			return fmt.Errorf("profile %q: %w", name, err)
		}
	}
	if p.Encrypt != "" {
		if p.Type != "sqlserver" {
			return fmt.Errorf("profile %q: encrypt is only valid for sqlserver profiles", name)
		}
		if err := ValidateEncrypt(p.Encrypt); err != nil {
			return fmt.Errorf("profile %q: %w", name, err)
		}
	}

	switch p.Type {
	case "sqlite":
		if p.Path == "" {
			return fmt.Errorf("profile %q: sqlite requires path", name)
		}
	case "oracle":
		if p.Host == "" {
			return fmt.Errorf("profile %q: oracle requires host", name)
		}
		if p.Database == "" && p.SID == "" {
			return fmt.Errorf("profile %q: oracle requires database (service name) or sid", name)
		}
	default: // mysql, postgres, sqlserver
		if p.Host == "" {
			return fmt.Errorf("profile %q: %s requires host", name, p.Type)
		}
		if p.Database == "" {
			return fmt.Errorf("profile %q: %s requires database", name, p.Type)
		}
	}

	// Bake defaults so profiles are self-contained.
	out.ReadOnly = p.ReadOnly
	if out.ReadOnly == nil {
		if d.ReadOnly != nil {
			v := *d.ReadOnly
			out.ReadOnly = &v
		} // else leave nil -> ResolvedReadOnly() defaults to true
	}
	out.MaxRows = p.MaxRows
	if out.MaxRows <= 0 {
		out.MaxRows = d.MaxRows
	}
	out.TimeoutSeconds = p.TimeoutSeconds
	if out.TimeoutSeconds <= 0 {
		out.TimeoutSeconds = d.TimeoutSeconds
	}
	return nil
}

// ValidateTLSMinVersion checks an optional sqlserver minimum TLS version.
// Empty means the driver default. Values map to the driver's tlsmin DSN
// parameter; unknown values would silently fall back to the default, so we
// reject them instead.
func ValidateTLSMinVersion(v string) error {
	switch v {
	case "", "1.0", "1.1", "1.2", "1.3":
		return nil
	}
	return fmt.Errorf("invalid tls_min_version %q (want 1.0|1.1|1.2|1.3 or empty)", v)
}

// ValidateEncrypt checks an optional sqlserver encrypt mode (maps to the
// driver's encrypt DSN parameter). Empty means the driver default.
func ValidateEncrypt(v string) error {
	switch strings.ToLower(v) {
	case "", "disable", "optional", "mandatory", "strict":
		return nil
	}
	return fmt.Errorf("invalid encrypt %q (want disable|optional|mandatory|strict or empty)", v)
}

func (p Profile) ResolvedReadOnly() bool {
	return p.ReadOnly == nil || *p.ReadOnly
}

func (p Profile) ResolvedMaxRows() int {
	if p.MaxRows > 0 {
		return p.MaxRows
	}
	return defaultMaxRows
}

func (p Profile) ResolvedTimeoutSeconds() int {
	if p.TimeoutSeconds > 0 {
		return p.TimeoutSeconds
	}
	return defaultTimeoutSecs
}

func (p Profile) ResolvePassword() (string, error) {
	if p.PasswordEnv == "" {
		return p.Password, nil
	}
	v, ok := os.LookupEnv(p.PasswordEnv)
	if !ok || v == "" {
		return "", fmt.Errorf("environment variable %s (referenced by profile) is not set", p.PasswordEnv)
	}
	return v, nil
}

func (p Profile) Dialect() (Dialect, error) {
	if d, ok := knownTypes[p.Type]; ok {
		return d, nil
	}
	return "", fmt.Errorf("unknown profile type %q", p.Type)
}

func (p Profile) Address() string {
	switch p.Type {
	case "sqlite":
		return p.Path
	case "oracle":
		h := fmt.Sprintf("%s:%d", p.Host, orDefault(p.Port, 1521))
		if p.SID != "" {
			return h + " sid=" + p.SID
		}
		return h + "/" + p.Database
	default:
		return fmt.Sprintf("%s:%d/%s", p.Host, orDefault(p.Port, defaultPort(p.Type)), p.Database)
	}
}

func defaultPort(t string) int {
	switch t {
	case "mysql":
		return 3306
	case "postgres":
		return 5432
	case "sqlserver":
		return 1433
	}
	return 0
}

func orDefault(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}

// identPattern validates unquoted SQL identifiers (used where a bind
// parameter is not portable, e.g. Oracle's user_tab_columns).
var identPattern = regexp.MustCompile(`^[A-Za-z0-9_$#]+$`)

func validateIdent(name string) error {
	if !identPattern.MatchString(name) {
		return fmt.Errorf("invalid table name %q", name)
	}
	return nil
}
