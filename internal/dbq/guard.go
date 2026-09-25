package dbq

import (
	"fmt"
	"regexp"
	"strings"
)

type Dialect string

const (
	DialectMySQL     Dialect = "mysql"
	DialectPostgres  Dialect = "postgres"
	DialectSQLServer Dialect = "sqlserver"
	DialectSQLite    Dialect = "sqlite"
	DialectOracle    Dialect = "oracle"
)

// GuardError marks statement-level rejections (exit code 2 in the CLI).
type GuardError struct{ Reason string }

func (e *GuardError) Error() string { return "guard: " + e.Reason }

// Keywords accepted from read-only profiles. Mirrors what each engine's
// read-only transaction actually permits.
var readOnlyKeywords = map[string]bool{
	"select": true, "with": true, "explain": true, "show": true,
	"describe": true, "desc": true, "pragma": true, "values": true,
	"table": true,
}

// writeKeyword finds DML/DDL keywords in a comment/string-stripped statement.
// Used on read-only profiles for SELECT/WITH (CTE writes) - on every engine.
var writeKeyword = regexp.MustCompile(`(?i)\b(insert|update|delete|merge|drop|alter|create|truncate|exec|execute|grant|revoke|kill|shutdown|sp_configure|openrowset|bulk)\b`)

// Validate enforces the statement policy for a profile:
//  1. exactly one statement (comments/strings stripped before checking)
//  2. read-only profiles: first keyword must be in the read-only allowlist
//  3. read-only SQL Server: no DML/DDL keywords anywhere in the statement
//
// Engine-level read-only transactions are a separate layer (see OpenReadonly).
func Validate(d Dialect, statement string, readOnly bool) error {
	stripped := stripLiteralsAndComments(d, statement)
	trimmed := strings.TrimSpace(stripped)
	if trimmed == "" {
		return &GuardError{Reason: "empty statement"}
	}

	// Allow at most one trailing semicolon; anything else is multi-statement.
	body := strings.TrimRight(trimmed, " \t\r\n")
	body = strings.TrimSuffix(body, ";")
	if strings.Contains(body, ";") {
		return &GuardError{Reason: "multiple statements are not allowed"}
	}

	if readOnly {
		first := firstKeyword(body)
		if !readOnlyKeywords[first] {
			return &GuardError{Reason: fmt.Sprintf("statement type '%s' is not allowed on a read-only profile", first)}
		}
		// SELECT/WITH can hide writes (data-modifying CTEs: "WITH x AS (...)
		// INSERT ..."). Scan the whole statement - literals/comments are
		// already stripped. SHOW/EXPLAIN/DESCRIBE/PRAGMA are metadata forms
		// and exempt (e.g. "SHOW CREATE TABLE").
		if (first == "select" || first == "with") && writeKeyword.MatchString(body) {
			return &GuardError{Reason: "DML/DDL keyword found in statement on a read-only profile"}
		}
	}
	return nil
}

func firstKeyword(s string) string {
	if i := strings.IndexAny(s, " \t\r\n("); i >= 0 {
		s = s[:i]
	}
	return strings.ToLower(s)
}

// IsQueryKeyword reports whether the statement returns rows (vs affecting them).
func IsQueryKeyword(statement string) bool {
	return readOnlyKeywords[firstKeyword(strings.TrimSpace(statement))]
}

// stripLiteralsAndComments replaces string/identifier literals with ” and
// comments with a space, so hidden ';' or keywords cannot hide inside them.
func stripLiteralsAndComments(d Dialect, s string) string {
	backslash := d == DialectMySQL // MySQL default: backslash escapes in strings
	hashComment := d == DialectMySQL

	var b strings.Builder
	b.Grow(len(s))
	i, n := 0, len(s)
	for i < n {
		c := s[i]
		switch {
		case c == '\'' || c == '"':
			quote := c
			b.WriteString("''")
			i++
			for i < n {
				if backslash && s[i] == '\\' && i+1 < n {
					i += 2
					continue
				}
				if s[i] == quote {
					if i+1 < n && s[i+1] == quote { // doubled quote escape
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
		case c == '`' && d == DialectMySQL:
			b.WriteString("``")
			i++
			for i < n {
				if s[i] == '`' {
					if i+1 < n && s[i+1] == '`' {
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
		case c == '-' && i+1 < n && s[i+1] == '-':
			for i < n && s[i] != '\n' {
				i++
			}
			b.WriteByte(' ')
		case c == '/' && i+1 < n && s[i+1] == '*':
			i += 2
			for i+1 < n && !(s[i] == '*' && s[i+1] == '/') {
				i++
			}
			i += 2
			if i > n {
				i = n
			}
			b.WriteByte(' ')
		case c == '#' && hashComment:
			for i < n && s[i] != '\n' {
				i++
			}
			b.WriteByte(' ')
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}
