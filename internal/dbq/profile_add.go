package dbq

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/BurntSushi/toml"
)

// AppendProfile validates and appends a new [profiles.<name>] block to the
// profile file, creating it (and its directory) with secure modes if needed.
// Existing content is preserved as-is (comments, ordering).
func AppendProfile(path string, name string, p Profile) error {
	if err := validateName(name); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("chmod %s: %w", dir, err)
	}

	existing := ""
	if fi, err := os.Stat(path); err == nil {
		if fi.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("insecure permissions on %s (%04o) - run: chmod 600 %s", path, fi.Mode().Perm(), path)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		existing = string(b)
		if strings.TrimSpace(existing) != "" {
			var cfg Config
			if _, err := toml.Decode(existing, &cfg); err != nil {
				return fmt.Errorf("existing file does not parse (%s) - fix it manually first", path)
			}
			if _, dup := cfg.Profiles[name]; dup {
				return fmt.Errorf("profile %q already exists", name)
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	// Validate the new profile against the same rules Load applies.
	baked := p
	if err := validateAndBake(name, p, &Defaults{}, &baked); err != nil {
		return err
	}

	block := BuildProfileBlock(name, baked)
	var out strings.Builder
	// O_APPEND writes at end of current content: emit only the new block
	// (plus a separator if the existing tail lacks a newline).
	if existing != "" && !strings.HasSuffix(existing, "\n") {
		out.WriteString("\n")
	}
	out.WriteString("\n# added by dbq add ")
	out.WriteString(time.Now().Format("2006-01-02 15:04:05"))
	out.WriteString("\n")
	out.WriteString(block)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(out.String()); err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	return f.Sync()
}

func validateName(name string) error {
	if name == "" {
		return fmt.Errorf("profile name is empty")
	}
	if len(name) > 64 {
		return fmt.Errorf("profile name too long")
	}
	for _, r := range name {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-') {
			return fmt.Errorf("invalid profile name %q (letters, digits, '_' and '-' only)", name)
		}
	}
	return nil
}

// BuildProfileBlock renders a TOML block for one profile. Explicit fields
// only - zero values are omitted so profile-level defaults stay visible.
func BuildProfileBlock(name string, p Profile) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[profiles.%s]\n", name)
	fmt.Fprintf(&b, "type = %s\n", tomlString(p.Type))
	if p.Host != "" {
		fmt.Fprintf(&b, "host = %s\n", tomlString(p.Host))
	}
	if p.Port != 0 {
		fmt.Fprintf(&b, "port = %d\n", p.Port)
	}
	if p.User != "" {
		fmt.Fprintf(&b, "user = %s\n", tomlString(p.User))
	}
	if p.PasswordEnv != "" {
		fmt.Fprintf(&b, "password_env = %s\n", tomlString(p.PasswordEnv))
	} else if p.Password != "" {
		fmt.Fprintf(&b, "password = %s\n", tomlString(p.Password))
	}
	if p.Database != "" {
		fmt.Fprintf(&b, "database = %s\n", tomlString(p.Database))
	}
	if p.SID != "" {
		fmt.Fprintf(&b, "sid = %s\n", tomlString(p.SID))
	}
	if p.Path != "" {
		fmt.Fprintf(&b, "path = %s\n", tomlString(p.Path))
	}
	if p.SSLMode != "" {
		fmt.Fprintf(&b, "sslmode = %s\n", tomlString(p.SSLMode))
	}
	// Always explicit: readers (human or agent) must see the posture.
	fmt.Fprintf(&b, "readonly = %t\n", p.ResolvedReadOnly())
	return b.String()
}

// tomlString quotes s as a TOML string. Prefers a literal '...' string
// (no escaping - ideal for passwords with backslashes/quotes), falls back
// to a basic "..." string when the value contains single quotes or control
// characters.
func tomlString(s string) string {
	if s == "" {
		return `""`
	}
	if !strings.ContainsAny(s, "'\n\r\x00") && !hasOtherControl(s) {
		return "'" + s + "'"
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

func hasOtherControl(s string) bool {
	for _, r := range s {
		if r < 0x20 && r != '\t' {
			return true
		}
	}
	return false
}
