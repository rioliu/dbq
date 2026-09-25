package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
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

func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "dbq-test-bin")
	out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}
