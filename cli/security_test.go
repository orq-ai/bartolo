package cli

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// ConfirmDestructive is what stops an accidental delete, so pin its three paths:
// --force proceeds, a non-interactive shell without --force refuses, and a No at
// the prompt refuses (RES-1134).
func TestConfirmDestructive(t *testing.T) {
	cmdWithForce := func(force bool) *cobra.Command {
		c := &cobra.Command{}
		c.Flags().Bool("force", force, "")
		return c
	}
	origInteractive, origAsk, origExit := isInteractive, askConfirm, exitFunc
	t.Cleanup(func() { isInteractive, askConfirm, exitFunc = origInteractive, origAsk, origExit })

	// --force set: proceeds, and the prompt is never consulted.
	isInteractive = func() bool { return true }
	askConfirm = func(string) (bool, error) { t.Fatal("prompt ran despite --force"); return false, nil }
	exitFunc = func(code int) { t.Fatalf("exitFunc(%d) called on the --force path", code) }
	if !ConfirmDestructive(cmdWithForce(true), "delete widget") {
		t.Error("--force should proceed")
	}

	// Non-interactive without --force: refuses without prompting, and exits non-zero
	// so a scripted caller can tell the skipped delete from a successful one.
	isInteractive = func() bool { return false }
	askConfirm = func(string) (bool, error) { t.Fatal("prompt ran in a non-interactive shell"); return false, nil }
	exitCode := -1
	exitFunc = func(code int) { exitCode = code }
	if ConfirmDestructive(cmdWithForce(false), "delete widget") {
		t.Error("non-interactive without --force should refuse")
	}
	if exitCode != ExitUsage {
		t.Errorf("non-interactive refusal exit code = %d, want %d (ExitUsage)", exitCode, ExitUsage)
	}

	// Interactive, answered No: refuses. Also refuses if the prompt errors. Neither
	// path touches the exit code — a human cancelling is not an error.
	exitFunc = func(code int) { t.Fatalf("exitFunc(%d) called on an interactive path", code) }
	isInteractive = func() bool { return true }
	askConfirm = func(string) (bool, error) { return false, nil }
	if ConfirmDestructive(cmdWithForce(false), "delete widget") {
		t.Error("answering No should refuse")
	}
	askConfirm = func(string) (bool, error) { return false, errors.New("prompt failed") }
	if ConfirmDestructive(cmdWithForce(false), "delete widget") {
		t.Error("a prompt error should refuse")
	}
}

// hasInteractiveInput must reject all three non-terminal shapes, not just the pipe:
// /dev/null (docker without -i, systemd, cron), a pipe (CI), and a closed stdin. The
// old os.ModeCharDevice test passed for /dev/null because it is a character device,
// which is exactly the shape most non-interactive runs use (RES-1134).
func TestHasInteractiveInputRejectsNonTerminals(t *testing.T) {
	orig := os.Stdin
	t.Cleanup(func() { os.Stdin = orig })

	t.Run("dev_null", func(t *testing.T) {
		f, err := os.Open(os.DevNull)
		if err != nil {
			t.Skipf("cannot open %s: %v", os.DevNull, err)
		}
		t.Cleanup(func() { f.Close() })
		os.Stdin = f
		if hasInteractiveInput() {
			t.Error("/dev/null on stdin must not read as interactive")
		}
	})

	t.Run("pipe", func(t *testing.T) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("os.Pipe: %v", err)
		}
		t.Cleanup(func() { r.Close(); w.Close() })
		os.Stdin = r
		if hasInteractiveInput() {
			t.Error("a pipe on stdin must not read as interactive")
		}
	})

	t.Run("closed_stdin", func(t *testing.T) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("os.Pipe: %v", err)
		}
		w.Close()
		r.Close() // stdin is now a closed descriptor
		os.Stdin = r
		if hasInteractiveInput() {
			t.Error("a closed stdin must not read as interactive")
		}
	})
}

// A verbose HTTP log must never print an API key, bearer token, or cookie (RES-1134).
func TestRedactHeaderValue(t *testing.T) {
	secret := "Bearer sk-live-supersecret"
	redacted := map[string]string{
		"Authorization":       secret,
		"AUTHORIZATION":       secret, // case-insensitive
		"Proxy-Authorization": secret,
		"Cookie":              "session=abc",
		"Set-Cookie":          "session=abc",
		"X-Api-Key":           "sk-123",
		"X-My-Token":          "t-123",
		"X-Client-Secret":     "s-123",
		// Headers the credentials layer (looksSensitiveKey) already treats as secret
		// but the narrower list used to print. A generated CLI against our own API
		// sends X-Orq-Key (RES-1134 review).
		"X-Orq-Key":       "sk-123",
		"Private-Key":     "----",
		"X-Password":      "hunter2",
		"X-Session-Key":   "sess-123",
		"X-Access-Key-Id": "AKIA123",
	}
	for key, val := range redacted {
		if got := redactHeaderValue(key, val); got != "[REDACTED]" {
			t.Errorf("redactHeaderValue(%q) = %q, want [REDACTED]", key, got)
		}
	}

	kept := map[string]string{
		"Content-Type": "application/json",
		"User-Agent":   "orq-cli/1.0",
		"Accept":       "*/*",
	}
	for key, val := range kept {
		if got := redactHeaderValue(key, val); got != val {
			t.Errorf("redactHeaderValue(%q) = %q, want %q (not sensitive)", key, got, val)
		}
	}
}

// A generated Delete command registers --force itself, so a spec parameter or
// body field named "force" must be renamed rather than register the flag twice
// and panic pflag at startup (RES-1134).
func TestForceFlagNameIsReserved(t *testing.T) {
	if _, reserved := ReservedFlagName("force"); !reserved {
		t.Fatal("force must be reserved so a colliding spec field is renamed")
	}
	if got := ResolveGeneratedFlagName("body", "force"); got != "body-force" {
		t.Errorf("ResolveGeneratedFlagName(body, force) = %q, want body-force", got)
	}
}

// The credentials file holds long-lived API keys and must be 0600 (RES-1134). Two paths:
// a new file must be 0600 from creation (no 0644 window), and a pre-existing 0644 file
// must be narrowed. Because writeCredentials no longer chmods after WriteConfigAs, removing
// the pre-create makes the new-file case fail here rather than pass silently.
func TestWriteCredentialsIsNotWorldReadable(t *testing.T) {
	write := func(t *testing.T, filename string) {
		Creds = &CredentialsFile{viper.New(), []string{}, []string{}}
		Creds.SetConfigName("credentials")
		Creds.Set("profiles.test.type", "apikey")
		if err := writeCredentials(filename); err != nil {
			t.Fatalf("writeCredentials: %v", err)
		}
		info, err := os.Stat(filename)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("credentials file mode = %o, want 600 (group/other must not read a key file)", perm)
		}
	}

	t.Run("new_file", func(t *testing.T) {
		write(t, filepath.Join(t.TempDir(), "credentials.json"))
	})

	// Creation-time mode, not just the end state: with WriteConfigAs failing (viper
	// rejects an unknown extension before it writes), the only thing that set the mode
	// is the pre-create. Weakening that mode to 0644 fails here even though the end-state
	// cases still pass (RES-1134 review).
	t.Run("mode_at_creation", func(t *testing.T) {
		filename := filepath.Join(t.TempDir(), "credentials.unsupported")
		Creds = &CredentialsFile{viper.New(), []string{}, []string{}}
		Creds.Set("profiles.test.type", "apikey")
		if err := writeCredentials(filename); err == nil {
			t.Fatal("writeCredentials: want an error from WriteConfigAs on an unknown extension")
		}
		info, err := os.Stat(filename)
		if err != nil {
			t.Fatalf("stat: %v (the file must be pre-created before the key is written)", err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("credentials file mode at creation = %o, want 600", perm)
		}
	})

	t.Run("existing_0644_narrowed", func(t *testing.T) {
		filename := filepath.Join(t.TempDir(), "credentials.json")
		if err := os.WriteFile(filename, []byte("{}"), 0o644); err != nil {
			t.Fatalf("seed 0644: %v", err)
		}
		write(t, filename)
	})
}

// resolveProfileValue is what both add-profile paths use to get each key's value. The
// promptProfileValue seam lets us test all three branches without a terminal (RES-1134).
func TestResolveProfileValue(t *testing.T) {
	orig := promptProfileValue
	t.Cleanup(func() { promptProfileValue = orig })

	// Positional value present: used verbatim, and the prompt never runs.
	promptProfileValue = func(string) (string, error) {
		t.Fatal("prompt ran when a positional value was given")
		return "", nil
	}
	if v, ok := resolveProfileValue("api_key", "api-key", []string{"prod", "sk-123"}, 0); !ok || v != "sk-123" {
		t.Errorf("positional: got (%q, %v), want (sk-123, true)", v, ok)
	}

	// No positional value, prompt succeeds: the prompted value is used.
	promptProfileValue = func(string) (string, error) { return "typed-secret", nil }
	if v, ok := resolveProfileValue("api_key", "api-key", []string{"prod"}, 0); !ok || v != "typed-secret" {
		t.Errorf("prompted: got (%q, %v), want (typed-secret, true)", v, ok)
	}

	// No positional value, prompt fails (no TTY): ok is false, so the caller exits usage.
	promptProfileValue = func(string) (string, error) { return "", errors.New("no terminal") }
	if v, ok := resolveProfileValue("api_key", "api-key", []string{"prod"}, 0); ok || v != "" {
		t.Errorf("prompt failure: got (%q, %v), want (empty, false)", v, ok)
	}
}
