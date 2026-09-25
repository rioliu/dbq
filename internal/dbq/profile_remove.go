package dbq

import (
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"
)

// RemoveProfile deletes the [profiles.<name>] block from the profile file.
// Comment lines attached directly above the block are removed with it;
// comments, [defaults], other profiles and unmanaged keys elsewhere are
// preserved. The rewrite is atomic and keeps mode 600.
func RemoveProfile(path string, name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	b, err := readSecureProfileFile(path)
	if err != nil {
		return err
	}
	existing := string(b)
	var cfg Config
	if _, err := toml.Decode(existing, &cfg); err != nil {
		return fmt.Errorf("existing file does not parse (%s) - fix it manually first", path)
	}
	if _, ok := cfg.Profiles[name]; !ok {
		return fmt.Errorf("profile %q not found in %s", name, path)
	}

	updated, err := deleteProfileBlock(existing, name)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, []byte(updated))
}

// deleteProfileBlock splices the [profiles.<name>] section out of content.
// The region spans the comment lines directly attached above the header
// through the last line before the next section header (comments attached
// to that next header stay with it), and keeps the surrounding sections
// separated by exactly one blank line.
func deleteProfileBlock(content, name string) (string, error) {
	lines := strings.Split(content, "\n")

	hdr := -1
	for i, ln := range lines {
		if sectionMatchesProfiles(ln, name) {
			hdr = i
			break
		}
	}
	if hdr < 0 {
		return "", fmt.Errorf("profile %q: section header not found - the file uses a layout dbq cannot delete safely; remove it manually", name)
	}

	// Comment lines directly above the header belong to this profile
	// (e.g. "# added by dbq add ...") - remove them with the block.
	start := hdr
	for start > 0 && isCommentLine(lines[start-1]) {
		start--
	}

	// End of the block: next section header, minus the comment lines
	// attached directly above that header (they belong to the next block).
	end := len(lines)
	for i := hdr + 1; i < len(lines); i++ {
		if isSectionHeader(lines[i]) {
			end = i
			break
		}
	}
	for end-1 > hdr && isCommentLine(lines[end-1]) {
		end--
	}

	result := make([]string, 0, len(lines))
	result = append(result, lines[:start]...)
	if start > 0 && end < len(lines) &&
		strings.TrimSpace(lines[start-1]) != "" && strings.TrimSpace(lines[end]) != "" {
		// Sections were adjacent without a blank line - keep exactly one.
		result = append(result, "")
	}
	result = append(result, lines[end:]...)

	// Keep the file ending with exactly one newline.
	if len(result) > 0 && result[len(result)-1] != "" {
		result = append(result, "")
	}
	return strings.Join(result, "\n"), nil
}

func isCommentLine(ln string) bool {
	return strings.HasPrefix(strings.TrimSpace(ln), "#")
}
