// dbq - minimal multi-database CLI with cred profiles and read-only guardrails.
//
//	dbq list                     show configured profiles
//	dbq add                      interactive wizard to add a profile
//	dbq edit <profile>           interactively update an existing profile
//	dbq ping <profile>           test connectivity
//	dbq query <profile> [flags] <sql...> | -    run a query ('-' = stdin)
//	dbq schema <profile> [flags] [table]        list tables / columns
//
// Exit codes: 0 ok, 1 usage/config, 2 statement guard, 3 connection/query error.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/rioliu/dbq/internal/dbq"
	"golang.org/x/term"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

const usageText = `dbq - multi-database query CLI with guarded cred profiles

Usage:
  dbq add                          Interactive wizard to add a profile
  dbq edit <profile>               Interactively update an existing profile
  dbq list                          List profiles (never shows passwords)
  dbq ping <profile>                Test connectivity
  dbq query <profile> [--format table|json|csv] [--limit N] <sql...>
                                    Run one statement; use '-' to read SQL from stdin
  dbq schema <profile> [--format ...] [table]
                                    List tables, or columns of one table
  dbq help                          Show this help

Profiles: see SPEC.md. Default location: ~/.config/dbq/profiles.toml
          ($DBQ_PROFILES overrides). File must be chmod 600.

Exit codes: 0 ok | 1 usage/config | 2 statement blocked by guard | 3 connection/query error
`

func run(ctx context.Context, argv []string, out, errW io.Writer) int {
	if len(argv) == 0 {
		fmt.Fprint(errW, usageText)
		return 1
	}
	switch argv[0] {
	case "help", "-h", "--help":
		fmt.Fprint(out, usageText)
		return 0
	case "list":
		return cmdList(out, errW)
	case "add":
		return cmdAdd(errW)
	case "edit":
		return cmdEdit(argv[1:], errW)
	case "ping":
		return withProfile(argv[1:], errW, func(p dbq.Profile, name string) int {
			start := time.Now()
			if err := dbq.Ping(ctx, p); err != nil {
				fmt.Fprintf(errW, "dbq: ERROR: ping %s: %v\n", name, err)
				return 3
			}
			fmt.Fprintf(out, "OK %s (%s) in %s\n", name, p.Address(), time.Since(start).Round(time.Millisecond))
			return 0
		})
	case "query":
		return cmdQuery(ctx, argv[1:], out, errW)
	case "schema":
		return cmdSchema(ctx, argv[1:], out, errW)
	default:
		fmt.Fprintf(errW, "dbq: ERROR: unknown command %q\n\n", argv[0])
		fmt.Fprint(errW, usageText)
		return 1
	}
}

func loadConfig(errW io.Writer) (*dbq.Config, string, int) {
	path, err := dbq.DefaultPath()
	if err != nil {
		fmt.Fprintf(errW, "dbq: ERROR: %v\n", err)
		return nil, "", 1
	}
	cfg, err := dbq.Load(path)
	if err != nil {
		fmt.Fprintf(errW, "dbq: ERROR: %v\n", err)
		return nil, path, 1
	}
	return cfg, path, 0
}

func lookupProfile(errW io.Writer, name string) (dbq.Profile, int) {
	cfg, _, code := loadConfig(errW)
	if code != 0 {
		return dbq.Profile{}, code
	}
	p, ok := cfg.Profiles[name]
	if !ok {
		names := make([]string, 0, len(cfg.Profiles))
		for n := range cfg.Profiles {
			names = append(names, n)
		}
		fmt.Fprintf(errW, "dbq: ERROR: unknown profile %q (available: %s)\n", name, strings.Join(names, ", "))
		return dbq.Profile{}, 1
	}
	return p, 0
}

// withProfile resolves <profile> as the first argument then invokes fn.
func withProfile(args []string, errW io.Writer, fn func(dbq.Profile, string) int) int {
	if len(args) < 1 {
		fmt.Fprintln(errW, "dbq: ERROR: missing <profile> argument")
		return 1
	}
	p, code := lookupProfile(errW, args[0])
	if code != 0 {
		return code
	}
	return fn(p, args[0])
}

// prompter wraps the interactive stdin helpers shared by the add and edit
// wizards. Secrets are read with echo disabled (TTY) or from stdin - never
// from argv, so they cannot leak into shell history or process lists.
type prompter struct {
	in   *bufio.Reader
	tty  bool
	errW io.Writer
}

func newPrompter(errW io.Writer) *prompter {
	tty := term.IsTerminal(int(os.Stdin.Fd()))
	if !tty {
		fmt.Fprintln(errW, "dbq: note: stdin is not a terminal - input will NOT be hidden")
	}
	return &prompter{in: bufio.NewReader(os.Stdin), tty: tty, errW: errW}
}

func (pr *prompter) ask(label, def string) string {
	if def != "" {
		fmt.Fprintf(pr.errW, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(pr.errW, "%s: ", label)
	}
	line, err := pr.in.ReadString('\n')
	if err != nil && line == "" {
		return ""
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return def
	}
	return line
}

func (pr *prompter) secret(label string) string {
	if pr.tty {
		fmt.Fprintf(pr.errW, "%s: ", label)
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(pr.errW)
		if err != nil {
			return ""
		}
		return string(b)
	}
	return pr.ask(label, "")
}

func (pr *prompter) askYesNo(label string, def bool) bool {
	hint := "Y/n"
	if !def {
		hint = "y/N"
	}
	v := pr.ask(label+" ["+hint+"]", "")
	switch strings.ToLower(v) {
	case "":
		return def
	case "y", "yes":
		return true
	default:
		return false
	}
}

// promptFields collects type and connection fields for both wizards. def
// supplies the current values as defaults (Enter keeps them); for add it is
// the zero Profile, so required fields have no default. Password and
// readonly prompts stay with the callers.
func promptFields(pr *prompter, def dbq.Profile) (dbq.Profile, error) {
	var p dbq.Profile
	typeDef := def.Type
	if typeDef == "" {
		typeDef = "mysql"
	}
	p.Type = pr.ask("Type (mysql|postgres|sqlserver|oracle|sqlite)", typeDef)

	if p.Type == "sqlite" {
		p.Path = pr.ask("Database file path", def.Path)
		if p.Path == "" {
			return p, errors.New("path is required for sqlite")
		}
		return p, nil
	}

	allowed := map[string]bool{"mysql": true, "postgres": true, "sqlserver": true, "oracle": true}
	if !allowed[p.Type] {
		return p, fmt.Errorf("unknown type %q", p.Type)
	}
	p.Host = pr.ask("Host", def.Host)
	if p.Host == "" {
		return p, errors.New("host is required")
	}
	defPort := map[string]string{"mysql": "3306", "postgres": "5432", "sqlserver": "1433", "oracle": "1521"}[p.Type]
	if def.Port > 0 {
		defPort = strconv.Itoa(def.Port)
	}
	portStr := pr.ask("Port", defPort)
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return p, fmt.Errorf("invalid port %q", portStr)
	}
	p.Port = port

	if p.Type == "oracle" {
		p.Database = pr.ask("Service name (empty = use SID)", def.Database)
		if p.Database == "" {
			p.SID = pr.ask("SID", def.SID)
			if p.SID == "" {
				return p, errors.New("oracle needs a service name or SID")
			}
		}
	} else {
		p.Database = pr.ask("Database", def.Database)
		if p.Database == "" {
			return p, errors.New("database is required")
		}
	}
	p.User = pr.ask("User", def.User)
	return p, nil
}

// cmdAdd is the interactive profile wizard.
func cmdAdd(errW io.Writer) int {
	path, err := dbq.DefaultPath()
	if err != nil {
		fmt.Fprintf(errW, "dbq: ERROR: %v\n", err)
		return 1
	}
	pr := newPrompter(errW)

	name := pr.ask("Profile name (letters, digits, '_' '-')", "")
	if name == "" {
		fmt.Fprintln(errW, "dbq: ERROR: profile name is required")
		return 1
	}

	p, err := promptFields(pr, dbq.Profile{})
	if err != nil {
		fmt.Fprintf(errW, "dbq: ERROR: %v\n", err)
		return 1
	}

	if p.Type != "sqlite" {
		fmt.Fprintln(errW, "Password source: [1] inline (stored in profile file, hidden prompt)  [2] environment variable  [3] none")
		switch pr.ask("Choice", "1") {
		case "2":
			v := pr.ask("Environment variable name (e.g. DBPASS_PROD)", "")
			if v == "" || !envNameRe.MatchString(v) {
				fmt.Fprintf(errW, "dbq: ERROR: invalid env var name %q\n", v)
				return 1
			}
			p.PasswordEnv = v
		case "3":
			// no password
		default:
			p.Password = pr.secret("Password")
			if p.Password == "" {
				fmt.Fprintln(errW, "dbq: ERROR: password is required for choice 1 (or pick 2/3)")
				return 1
			}
		}
		if p.Type == "postgres" {
			p.SSLMode = pr.ask("sslmode (empty = driver default)", "")
		}
	}

	if !pr.askYesNo("Read-only profile", true) {
		ro := false
		if p.ReadOnly == nil {
			p.ReadOnly = &ro
		}
	}

	fmt.Fprintf(errW, "\nSummary: %s  type=%s  %s  password=%s  readonly=%v\n",
		name, p.Type, summaryAddr(p), passwordDesc(p), p.ResolvedReadOnly())
	if !pr.askYesNo("Append to "+path, true) {
		fmt.Fprintln(errW, "dbq: aborted, nothing written")
		return 1
	}
	if err := dbq.AppendProfile(path, name, p); err != nil {
		fmt.Fprintf(errW, "dbq: ERROR: %v\n", err)
		return 1
	}
	fmt.Fprintf(errW, "dbq: profile %q added (%s, mode 600)\n", name, path)
	fmt.Fprintf(errW, "next: dbq list && dbq ping %s && dbq query %s \"SELECT 1\"\n", name, name)
	return 0
}

// cmdEdit updates an existing profile through the same wizard UX as add.
// Every prompt is prefilled with the current value; Enter keeps it. Secrets
// are never accepted via argv - only through the hidden prompt.
func cmdEdit(args []string, errW io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(errW, "dbq: ERROR: missing <profile> argument")
		return 1
	}
	name := args[0]
	path, err := dbq.DefaultPath()
	if err != nil {
		fmt.Fprintf(errW, "dbq: ERROR: %v\n", err)
		return 1
	}
	cfg, err := dbq.Load(path)
	if err != nil {
		fmt.Fprintf(errW, "dbq: ERROR: %v\n", err)
		return 1
	}
	cur, ok := cfg.Profiles[name]
	if !ok {
		names := make([]string, 0, len(cfg.Profiles))
		for n := range cfg.Profiles {
			names = append(names, n)
		}
		sort.Strings(names)
		fmt.Fprintf(errW, "dbq: ERROR: unknown profile %q (available: %s)\n", name, strings.Join(names, ", "))
		return 1
	}

	fmt.Fprintf(errW, "Editing profile %q (Enter keeps the current value)\n", name)
	pr := newPrompter(errW)

	p, err := promptFields(pr, cur)
	if err != nil {
		fmt.Fprintf(errW, "dbq: ERROR: %v\n", err)
		return 1
	}

	if p.Type != "sqlite" {
		curPw := "none"
		switch {
		case cur.PasswordEnv != "":
			curPw = "env:" + cur.PasswordEnv
		case cur.Password != "":
			curPw = "inline (hidden)"
		}
		fmt.Fprintf(errW, "Password: [1] keep current (%s)  [2] change (hidden prompt)  [3] environment variable  [4] none\n", curPw)
		switch pr.ask("Choice", "1") {
		case "2":
			p.Password = pr.secret("New password")
			if p.Password == "" {
				fmt.Fprintln(errW, "dbq: ERROR: password is required for choice 2")
				return 1
			}
		case "3":
			v := pr.ask("Environment variable name (e.g. DBPASS_PROD)", cur.PasswordEnv)
			if v == "" || !envNameRe.MatchString(v) {
				fmt.Fprintf(errW, "dbq: ERROR: invalid env var name %q\n", v)
				return 1
			}
			p.PasswordEnv = v
		case "4":
			// credentials explicitly cleared
		default: // keep current credentials
			p.Password = cur.Password
			p.PasswordEnv = cur.PasswordEnv
		}
		if p.Type == "postgres" {
			p.SSLMode = pr.ask("sslmode (empty = driver default)", cur.SSLMode)
		}
	}

	ro := pr.askYesNo("Read-only profile", cur.ResolvedReadOnly())
	p.ReadOnly = &ro

	fmt.Fprintf(errW, "\nSummary: %s  type=%s  %s  password=%s  readonly=%v\n",
		name, p.Type, summaryAddr(p), passwordDesc(p), p.ResolvedReadOnly())
	if !pr.askYesNo("Update "+path, true) {
		fmt.Fprintln(errW, "dbq: aborted, nothing written")
		return 1
	}
	changed, err := dbq.UpdateProfile(path, name, p)
	if err != nil {
		fmt.Fprintf(errW, "dbq: ERROR: %v\n", err)
		return 1
	}
	if !changed {
		fmt.Fprintf(errW, "dbq: profile %q unchanged\n", name)
		return 0
	}
	fmt.Fprintf(errW, "dbq: profile %q updated (%s, mode 600)\n", name, path)
	fmt.Fprintf(errW, "next: dbq list && dbq ping %s\n", name)
	return 0
}

var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// passwordDesc renders the credential source for wizard summaries without
// revealing the secret itself.
func passwordDesc(p dbq.Profile) string {
	switch {
	case p.PasswordEnv != "":
		return "env:" + p.PasswordEnv
	case p.Password != "":
		return "inline (hidden)"
	default:
		return "none"
	}
}

func summaryAddr(p dbq.Profile) string {
	if p.Type == "sqlite" {
		return p.Path
	}
	return p.Host + ":" + strconv.Itoa(p.Port) + "/" + p.Database
}

func readStdin(errW io.Writer) (string, int) {
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(errW, "dbq: ERROR: read stdin: %v\n", err)
		return "", 3
	}
	return string(b), 0
}

func cmdList(out, errW io.Writer) int {
	cfg, path, code := loadConfig(errW)
	if code != 0 {
		return code
	}
	fmt.Fprintf(out, "# %s\n", path)
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tTYPE\tADDRESS\tREADONLY\tMAX_ROWS\tTIMEOUT\tPASSWORD")
	for name, p := range cfg.Profiles {
		ro := "yes"
		if !p.ResolvedReadOnly() {
			ro = "NO (writable)"
		}
		pw := "-"
		if p.Password != "" {
			pw = "inline"
		} else if p.PasswordEnv != "" {
			pw = "env:" + p.PasswordEnv
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%ds\t%s\n",
			name, p.Type, p.Address(), ro, p.ResolvedMaxRows(), p.ResolvedTimeoutSeconds(), pw)
	}
	tw.Flush()
	return 0
}

// parseArgs extracts --format/--limit anywhere in argv; returns remaining
// positional args (profile + SQL parts).
func parseArgs(args []string, errW io.Writer) (format string, limit int, pos []string, ok bool) {
	format, limit = "table", 0
	for i := 0; i < len(args); i++ {
		a := args[i]
		next := func() (string, bool) {
			if i+1 >= len(args) {
				fmt.Fprintf(errW, "dbq: ERROR: %s needs a value\n", a)
				return "", false
			}
			i++
			return args[i], true
		}
		switch {
		case a == "--format":
			v, ok := next()
			if !ok {
				return "", 0, nil, false
			}
			format = v
		case strings.HasPrefix(a, "--format="):
			format = strings.TrimPrefix(a, "--format=")
		case a == "--limit":
			v, ok := next()
			if !ok {
				return "", 0, nil, false
			}
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				fmt.Fprintf(errW, "dbq: ERROR: --limit must be a non-negative integer\n")
				return "", 0, nil, false
			}
			limit = n
		case strings.HasPrefix(a, "--limit="):
			n, err := strconv.Atoi(strings.TrimPrefix(a, "--limit="))
			if err != nil || n < 0 {
				fmt.Fprintf(errW, "dbq: ERROR: --limit must be a non-negative integer\n")
				return "", 0, nil, false
			}
			limit = n
		case a == "-h" || a == "--help":
			fmt.Fprint(errW, usageText)
			return "", 0, nil, false
		default:
			pos = append(pos, a)
		}
	}
	return format, limit, pos, true
}

func cmdQuery(ctx context.Context, args []string, out, errW io.Writer) int {
	format, limit, rest, ok := parseArgs(args, errW)
	if !ok {
		return 1
	}
	if len(rest) < 1 {
		fmt.Fprintln(errW, "dbq: ERROR: missing <profile> argument")
		return 1
	}

	var statement string
	var stdinCode int
	switch {
	case len(rest) >= 2 && rest[1] == "-":
		statement, stdinCode = readStdin(errW) // explicit stdin marker
	case len(rest) >= 2:
		statement = strings.Join(rest[1:], " ")
	default: // only the profile given: read SQL from stdin
		statement, stdinCode = readStdin(errW)
	}
	if stdinCode != 0 {
		return stdinCode
	}

	p, code := lookupProfile(errW, rest[0])
	if code != 0 {
		return code
	}

	res, err := dbq.Run(ctx, p, statement, limit)
	if err != nil {
		return reportRunError(err, errW)
	}
	printWarnings(res, errW)
	if err := dbq.Format(out, format, res); err != nil {
		fmt.Fprintf(errW, "dbq: ERROR: %v\n", err)
		return 3
	}
	if res.Truncated {
		fmt.Fprintf(errW, "dbq: note: result truncated at %d rows (use --limit to raise)\n", len(res.Rows))
	}
	return 0
}

func cmdSchema(ctx context.Context, args []string, out, errW io.Writer) int {
	format, _, rest, ok := parseArgs(args, errW)
	if !ok {
		return 1
	}
	if len(rest) < 1 {
		fmt.Fprintln(errW, "dbq: ERROR: missing <profile> argument")
		return 1
	}
	p, code := lookupProfile(errW, rest[0])
	if code != 0 {
		return code
	}
	table := ""
	if len(rest) > 1 {
		table = rest[1]
	}
	res, err := dbq.Schema(ctx, p, table)
	if err != nil {
		return reportRunError(err, errW)
	}
	printWarnings(res, errW)
	if err := dbq.Format(out, format, res); err != nil {
		fmt.Fprintf(errW, "dbq: ERROR: %v\n", err)
		return 3
	}
	return 0
}

func reportRunError(err error, errW io.Writer) int {
	var ge *dbq.GuardError
	if errors.As(err, &ge) {
		fmt.Fprintf(errW, "dbq: ERROR: %v\n", err)
		fmt.Fprintln(errW, "dbq: note: this is a hard guardrail. Do not retry variations to bypass it; ask a human if the write is genuinely required.")
		return 2
	}
	fmt.Fprintf(errW, "dbq: ERROR: %v\n", err)
	return 3
}

func printWarnings(res *dbq.Result, errW io.Writer) {
	for _, w := range res.Warnings {
		fmt.Fprintf(errW, "dbq: WARN: %s\n", w)
	}
}
