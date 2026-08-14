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
	origInteractive, origAsk := isInteractive, askConfirm
	t.Cleanup(func() { isInteractive, askConfirm = origInteractive, origAsk })

	// --force set: proceeds, and the prompt is never consulted.
	isInteractive = func() bool { return true }
	askConfirm = func(string) (bool, error) { t.Fatal("prompt ran despite --force"); return false, nil }
	if !ConfirmDestructive(cmdWithForce(true), "delete widget") {
		t.Error("--force should proceed")
	}

	// Non-interactive without --force: refuses without prompting.
	isInteractive = func() bool { return false }
	askConfirm = func(string) (bool, error) { t.Fatal("prompt ran in a non-interactive shell"); return false, nil }
	if ConfirmDestructive(cmdWithForce(false), "delete widget") {
		t.Error("non-interactive without --force should refuse")
	}

	// Interactive, answered No: refuses. Also refuses if the prompt errors.
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
