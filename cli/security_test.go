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

// hasInteractiveInput must reject /dev/null on stdin: it is a character device, so
// the old os.ModeCharDevice test passed for exactly the non-interactive shapes CI,
// docker (without -i), systemd and cron actually use (RES-1134).
func TestHasInteractiveInputRejectsDevNull(t *testing.T) {
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Skipf("cannot open %s: %v", os.DevNull, err)
	}
	t.Cleanup(func() { devnull.Close() })

	orig := os.Stdin
	os.Stdin = devnull
	t.Cleanup(func() { os.Stdin = orig })

	if hasInteractiveInput() {
		t.Error("/dev/null on stdin must not read as interactive")
	}
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

// The credentials file holds long-lived API keys and must be written 0600 (RES-1134).
func TestWriteCredentialsIsNotWorldReadable(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "credentials.json")
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
