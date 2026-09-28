package main

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rioliu/dbq/internal/dbq"
)

// TestInteractiveWizardsQuitOnSIGINT verifies that Ctrl+C (SIGINT) terminates
// the interactive add/edit wizards. main() registers a signal handler for
// query-type commands; if that handler also covers the wizards, SIGINT is
// swallowed while a prompt blocks on stdin and the user cannot quit.
func TestInteractiveWizardsQuitOnSIGINT(t *testing.T) {
	bin := buildBinary(t)

	profiles := filepath.Join(t.TempDir(), "profiles.toml")
	fix := "[profiles.x]\ntype = 'sqlite'\npath = '/tmp/x.db'\nreadonly = true\n"
	if err := os.WriteFile(profiles, []byte(fix), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		args []string
	}{
		{"add", []string{"add"}},
		{"edit", []string{"edit", "x"}},
		{"rm", []string{"rm", "x"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// stdin: pipe we keep open, so the prompt blocks on read (no EOF).
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			defer w.Close()

			cmd := exec.Command(bin, c.args...)
			cmd.Stdin = r
			cmd.Stdout = io.Discard
			cmd.Stderr = io.Discard
			cmd.Env = append(os.Environ(), "DBQ_PROFILES="+profiles)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			// Keep w open for the whole test: closing it would deliver EOF
			// to the prompt and the wizard would exit without SIGINT.

			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()

			// The wizard must still be running (blocked at a prompt)...
			select {
			case err := <-done:
				t.Fatalf("wizard exited before SIGINT (err=%v)", err)
			case <-time.After(500 * time.Millisecond):
			}

			// ...and must exit promptly once SIGINT arrives.
			if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
				// exited on SIGINT as expected
			case <-time.After(3 * time.Second):
				cmd.Process.Kill()
				<-done
				t.Fatal("wizard did not exit on SIGINT (Ctrl+C swallowed)")
			}
		})
	}
}

// TestRemoveProfileCommand covers the confirmation flow: without an answer
// nothing must be removed; with 'y' the entry is deleted.
func TestRemoveProfileCommand(t *testing.T) {
	bin := buildBinary(t)
	fixture := "[profiles.keep]\ntype = 'sqlite'\npath = '/keep.db'\nreadonly = true\n\n[profiles.gone]\ntype = 'sqlite'\npath = '/gone.db'\nreadonly = true\n"

	writeFixture := func(t *testing.T) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "profiles.toml")
		if err := os.WriteFile(p, []byte(fixture), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	run := func(profiles, stdin string) error {
		cmd := exec.Command(bin, "rm", "gone")
		cmd.Stdin = strings.NewReader(stdin)
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		cmd.Env = append(os.Environ(), "DBQ_PROFILES="+profiles)
		return cmd.Run()
	}

	t.Run("refused without confirmation", func(t *testing.T) {
		p := writeFixture(t)
		if err := run(p, ""); err == nil { // EOF -> default No
			t.Fatal("want non-zero exit when confirmation is declined")
		}
		b, _ := os.ReadFile(p)
		if !strings.Contains(string(b), "[profiles.gone]") {
			t.Fatalf("profile removed without confirmation:\n%s", b)
		}
	})

	t.Run("confirmed with y", func(t *testing.T) {
		p := writeFixture(t)
		if err := run(p, "y\n"); err != nil {
			t.Fatalf("rm: %v", err)
		}
		b, _ := os.ReadFile(p)
		if strings.Contains(string(b), "[profiles.gone]") {
			t.Fatalf("profile not removed:\n%s", b)
		}
		if !strings.Contains(string(b), "[profiles.keep]") {
			t.Fatalf("other profile lost:\n%s", b)
		}
	})
}

// promptFromScript builds a prompter reading scripted stdin lines.
func promptFromScript(script string) *prompter {
	return &prompter{in: bufio.NewReader(strings.NewReader(script)), errW: io.Discard}
}

func TestPromptFieldsSQLServerTLSMinVersion(t *testing.T) {
	// add flow: type, host, port, database, user, tls min version
	p, err := promptFields(promptFromScript("sqlserver\n192.168.21.102\n1433\njotmanager\nu\n1.0\n"), dbq.Profile{})
	if err != nil {
		t.Fatalf("promptFields: %v", err)
	}
	if p.TLSMinVersion != "1.0" {
		t.Fatalf("TLSMinVersion = %q, want 1.0", p.TLSMinVersion)
	}

	// invalid value must fail
	if _, err := promptFields(promptFromScript("sqlserver\nh\n1433\ndb\nu\n2.0\n"), dbq.Profile{}); err == nil {
		t.Fatal("invalid tls_min_version must be rejected")
	}

	// edit flow: Enter keeps the current value
	cur := dbq.Profile{Type: "sqlserver", Host: "h", Port: 1433, Database: "db",
		User: "u", TLSMinVersion: "1.2"}
	p, err = promptFields(promptFromScript("\n\n\n\n\n\n"), cur)
	if err != nil {
		t.Fatalf("promptFields: %v", err)
	}
	if p.TLSMinVersion != "1.2" {
		t.Fatalf("TLSMinVersion = %q, want kept 1.2", p.TLSMinVersion)
	}
}

func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "dbq-test-bin")
	out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

func TestPromptFieldsSQLServerEncrypt(t *testing.T) {
	// type, host, port, database, user, tls(min), encrypt
	p, err := promptFields(promptFromScript("sqlserver\nh\n1433\ndb\nu\n\ndisable\n"), dbq.Profile{})
	if err != nil {
		t.Fatalf("promptFields: %v", err)
	}
	if p.Encrypt != "disable" {
		t.Fatalf("Encrypt = %q, want disable", p.Encrypt)
	}
	if _, err := promptFields(promptFromScript("sqlserver\nh\n1433\ndb\nu\n\nplaintext\n"), dbq.Profile{}); err == nil {
		t.Fatal("invalid encrypt value must be rejected")
	}
}
