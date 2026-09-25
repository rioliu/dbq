package dbq

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// managedKeys are the profile keys dbq renders itself (BuildProfileBlock).
// Keys outside this set - e.g. max_rows, timeout_seconds or hand-added
// extras - are preserved untouched by UpdateProfile.
var managedKeys = map[string]bool{
	"type": true, "host": true, "port": true, "user": true,
	"password": true, "password_env": true, "database": true,
	"sid": true, "path": true, "sslmode": true, "readonly": true,
}

// UpdateProfile rewrites the [profiles.<name>] block in place. Only lines
// whose value actually changed are touched; comments, ordering, other
// profiles and unmanaged keys are preserved. Returns changed=false when the
// rewrite would be a no-op (the file is then left untouched).
func UpdateProfile(path string, name string, p Profile) (bool, error) {
	if err := validateName(name); err != nil {
		return false, err
	}
	b, err := readSecureProfileFile(path)
	if err != nil {
		return false, err
	}
	existing := string(b)
	var cfg Config
	if _, err := toml.Decode(existing, &cfg); err != nil {
		return false, fmt.Errorf("existing file does not parse (%s) - fix it manually first", path)
	}
	if _, ok := cfg.Profiles[name]; !ok {
		return false, fmt.Errorf("profile %q not found in %s", name, path)
	}

	// Validate the new profile against the same rules Load applies.
	baked := p
	if err := validateAndBake(name, p, &cfg.Defaults, &baked); err != nil {
		return false, err
	}

	updated, err := replaceProfileBlock(existing, name, BuildProfileBlock(name, baked))
	if err != nil {
		return false, err
	}
	if updated == existing {
		return false, nil
	}
	if err := writeFileAtomic(path, []byte(updated)); err != nil {
		return false, err
	}
	return true, nil
}

// readSecureProfileFile reads the profile file after refusing group/other
// accessible permissions - shared by the update/remove paths.
func readSecureProfileFile(path string) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("insecure permissions on %s (%04o) - run: chmod 600 %s", path, fi.Mode().Perm(), path)
	}
	return os.ReadFile(path)
}

// replaceProfileBlock splices the rendered newBlock into content, replacing
// only the body of the matching [profiles.<name>] section.
func replaceProfileBlock(content, name, newBlock string) (string, error) {
	lines := strings.Split(content, "\n")

	hdr := -1
	for i, ln := range lines {
		if sectionMatchesProfiles(ln, name) {
			hdr = i
			break
		}
	}
	if hdr < 0 {
		return "", fmt.Errorf("profile %q: section header not found - the file uses a layout dbq cannot edit safely; update it manually", name)
	}
	end := len(lines)
	for i := hdr + 1; i < len(lines); i++ {
		if isSectionHeader(lines[i]) {
			end = i
			break
		}
	}
	// Trailing blank lines separate sections - keep them outside the block.
	for end > hdr+1 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}

	desired := parseKeyedLines(strings.Split(newBlock, "\n"))
	want := make(map[string]string, len(desired))
	for _, d := range desired {
		want[d.key] = d.line
	}

	body := lines[hdr+1 : end]
	out := make([]string, 0, len(body)+len(desired))
	seen := make(map[string]bool, len(desired))
	for _, ln := range body {
		kv, ok := parseKeyedLine(ln)
		if !ok {
			out = append(out, ln) // blank line or comment
			continue
		}
		if !managedKeys[kv.key] {
			out = append(out, ln) // not ours to manage (max_rows, ...)
			continue
		}
		if w, ok := want[kv.key]; ok {
			out = append(out, w)
			seen[kv.key] = true
		}
		// managed key absent from the new block (password -> password_env
		// or a type change): drop the stale line.
	}
	// Keys the block did not have yet, appended in render order.
	for _, d := range desired {
		if !seen[d.key] {
			out = append(out, d.line)
		}
	}

	res := make([]string, 0, len(lines)+len(desired))
	res = append(res, lines[:hdr+1]...)
	res = append(res, out...)
	res = append(res, lines[end:]...)
	return strings.Join(res, "\n"), nil
}

func isSectionHeader(ln string) bool {
	return strings.HasPrefix(strings.TrimSpace(ln), "[")
}

// sectionMatchesProfiles reports whether ln is the TOML table header for
// [profiles.<name>], tolerating whitespace and quoted keys.
func sectionMatchesProfiles(ln, name string) bool {
	t := strings.TrimSpace(ln)
	if len(t) < 2 || !strings.HasPrefix(t, "[") || !strings.HasSuffix(t, "]") {
		return false
	}
	inner := strings.TrimSpace(t[1 : len(t)-1])
	if strings.HasPrefix(inner, "[") {
		return false // [[array of tables]]
	}
	dot := strings.Index(inner, ".")
	if dot < 0 {
		return false
	}
	if strings.TrimSpace(inner[:dot]) != "profiles" {
		return false
	}
	return unquoteTOMLKey(strings.TrimSpace(inner[dot+1:])) == name
}

func unquoteTOMLKey(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

type keyedLine struct {
	key  string
	line string
}

// parseKeyedLine extracts the key from a `key = value` line. Comment and
// blank lines (and anything else that does not look like an assignment)
// return ok=false so callers can pass them through unchanged.
func parseKeyedLine(ln string) (keyedLine, bool) {
	t := strings.TrimSpace(ln)
	if t == "" || strings.HasPrefix(t, "#") {
		return keyedLine{}, false
	}
	eq := strings.Index(ln, "=")
	if eq <= 0 {
		return keyedLine{}, false
	}
	key := strings.Trim(strings.TrimSpace(ln[:eq]), `"'`)
	if key == "" {
		return keyedLine{}, false
	}
	return keyedLine{key: key, line: ln}, true
}

func parseKeyedLines(lines []string) []keyedLine {
	out := make([]keyedLine, 0, len(lines))
	for _, ln := range lines {
		if kv, ok := parseKeyedLine(ln); ok {
			out = append(out, kv)
		}
	}
	return out
}

// writeFileAtomic replaces path with data via a temp file in the same
// directory, keeping mode 600 on both the temp file and the result.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once rename succeeded
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
