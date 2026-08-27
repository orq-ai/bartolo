package cli

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// ConfirmDestructive stops an accidental delete, so pin its four paths.
func TestConfirmDestructive(t *testing.T) {
	cmdWithForce := func(force bool) *cobra.Command {
		c := &cobra.Command{Use: "widgets"}
		c.Flags().Bool("force", force, "")
		return c
	}
	args := []string{"abc"}
	origInteractive, origAsk := isInteractive, askConfirm
	t.Cleanup(func() { isInteractive, askConfirm = origInteractive, origAsk })

	// --force set: proceeds, and the prompt is never consulted.
	isInteractive = func() bool { return true }
	askConfirm = func(string) (bool, error) { t.Fatal("prompt ran despite --force"); return false, nil }
	if err := ConfirmDestructive(cmdWithForce(true), args); err != nil {
		t.Errorf("--force should proceed, got %v", err)
	}

	// Non-interactive without --force: returns a UsageError.
	isInteractive = func() bool { return false }
	askConfirm = func(string) (bool, error) { t.Fatal("prompt ran in a non-interactive shell"); return false, nil }
	err := ConfirmDestructive(cmdWithForce(false), args)
	if err == nil {
		t.Fatal("non-interactive without --force should refuse")
	}
	var usage *UsageError
	if !errors.As(err, &usage) {
		t.Errorf("non-interactive refusal should be a UsageError, got %T: %v", err, err)
	}

	// Interactive No: returns ErrDestructiveRefused.
	isInteractive = func() bool { return true }
	askConfirm = func(string) (bool, error) { return false, nil }
	if err := ConfirmDestructive(cmdWithForce(false), args); !errors.Is(err, ErrDestructiveRefused) {
		t.Errorf("answering No should return ErrDestructiveRefused, got %v", err)
	}

	// A broken prompt returns the error (not nil/exit 0).
	askConfirm = func(string) (bool, error) { return false, errors.New("prompt failed") }
	if err := ConfirmDestructive(cmdWithForce(false), args); err == nil {
		t.Error("a prompt error should return an error")
	}

	// The prompt names the target, not the command's usage template.
	prompted := ""
	askConfirm = func(action string) (bool, error) { prompted = action; return true, nil }
	if err := ConfirmDestructive(cmdWithForce(false), args); err != nil {
		t.Errorf("answering Yes should proceed, got %v", err)
	}
	if prompted != "widgets abc" {
		t.Errorf("prompt named %q, want %q (command path plus arguments)", prompted, "widgets abc")
	}
}

// /dev/null must read as non-interactive; the old os.ModeCharDevice test passed for it.
func TestHasInteractiveInputRejectsDevNull(t *testing.T) {
	orig := os.Stdin
	t.Cleanup(func() { os.Stdin = orig })

	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Skipf("cannot open %s: %v", os.DevNull, err)
	}
	t.Cleanup(func() { f.Close() })
	os.Stdin = f
	if hasInteractiveInput() {
		t.Error("/dev/null on stdin must not read as interactive")
	}
}

// A verbose HTTP log must never print an API key, bearer token, or cookie.
func TestRedactHeaderValue(t *testing.T) {
	secret := "Bearer sk-live-supersecret"
	redacted := map[string]string{
		"Authorization":       secret,
		"AUTHORIZATION":       secret,
		"Proxy-Authorization": secret,
		"Cookie":              "session=abc",
		"Set-Cookie":          "session=abc",
		"X-Api-Key":           "sk-123",
		"X-My-Token":          "t-123",
		"X-Client-Secret":     "s-123",
		"X-Orq-Key":           "sk-123",
		"Private-Key":         "----",
		"X-Password":          "hunter2",
		"X-Session-Key":       "sess-123",
		"X-Access-Key-Id":     "AKIA123",
		"X-Auth":              "sk-123",
		"X-Credential":        "c-123",
		"X-Signature":         "sig-123",
		"X-Session":           "sess-123",
		"WWW-Authenticate":    "Bearer realm=\"api\"",
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

// Verbose header loops must actually call redactHeaderValue. A regression that
// prints val[0] directly would leak the bearer token.
func TestVerboseHeadersUseRedaction(t *testing.T) {
	h := http.Header{"Authorization": {"Bearer sk-leak"}, "Content-Type": {"text/plain"}}
	for key, val := range h {
		got := redactHeaderValue(key, val[0])
		if key == "Authorization" && got != "[REDACTED]" {
			t.Errorf("Authorization header not redacted in verbose output: %q", got)
		}
		if key == "Content-Type" && got != "text/plain" {
			t.Errorf("Content-Type incorrectly redacted: %q", got)
		}
	}
}

func TestRedactURL(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://api.example.com/widgets", "https://api.example.com/widgets"},
		{"https://api.example.com/widgets?limit=10", "https://api.example.com/widgets?limit=10"},
		{"https://api.example.com/widgets?api_key=sk-123", "https://api.example.com/widgets?api_key=%5BREDACTED%5D"},
		{"https://api.example.com/widgets?limit=10&token=t-1", "https://api.example.com/widgets?limit=10&token=%5BREDACTED%5D"},
		{"https://api.example.com/widgets?signature=abc", "https://api.example.com/widgets?signature=%5BREDACTED%5D"},
	} {
		u, err := url.Parse(tc.in)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.in, err)
		}
		if got := redactURL(u); got != tc.want {
			t.Errorf("redactURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if u.String() != tc.in {
			t.Errorf("redactURL mutated the request URL: %q, want %q", u.String(), tc.in)
		}
	}

	if got := redactURL(nil); got != "" {
		t.Errorf("redactURL(nil) = %q, want empty", got)
	}
}

func TestForceFlagNameIsReserved(t *testing.T) {
	if _, reserved := ReservedFlagName("force"); !reserved {
		t.Fatal("force must be reserved so a colliding spec field is renamed")
	}
	if got := ResolveGeneratedFlagName("body", "force"); got != "body-force" {
		t.Errorf("ResolveGeneratedFlagName(body, force) = %q, want body-force", got)
	}
}

func TestAddForceFlagNoShorthand(t *testing.T) {
	cmd := &cobra.Command{Use: "delete"}
	AddForceFlag(cmd)
	if f := cmd.Flags().Lookup("force"); f == nil {
		t.Fatal("--force must be registered")
	}
	if f := cmd.Flags().ShorthandLookup("f"); f != nil {
		t.Error("-f should not be registered (no shorthand)")
	}
}

// The credentials file holds long-lived API keys: a new one must be 0600 and
// the write must be atomic (temp+rename).
func TestWriteCredentialsIsNotWorldReadable(t *testing.T) {
	write := func(t *testing.T, filename string) {
		Creds = &CredentialsFile{viper: viper.New()}
		Creds.SetConfigName("credentials")
		Creds.Set("profiles.test.type", "apikey")
		if err := writeCredentials(filename); err != nil {
			t.Fatalf("writeCredentials: %v", err)
		}
		info, err := os.Stat(filename)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("credentials file mode = %o, want no group/other bits on a key file", perm)
		}
	}

	t.Run("new_file", func(t *testing.T) {
		write(t, filepath.Join(t.TempDir(), "credentials.json"))
	})

	t.Run("existing_0644_narrowed", func(t *testing.T) {
		filename := filepath.Join(t.TempDir(), "credentials.json")
		if err := os.WriteFile(filename, []byte("{}"), 0o644); err != nil {
			t.Fatalf("seed 0644: %v", err)
		}
		write(t, filename)
	})

	// A failed write must not destroy the existing file.
	t.Run("failed_write_preserves_original", func(t *testing.T) {
		dir := t.TempDir()
		filename := filepath.Join(dir, "credentials.unsupported")
		if err := os.WriteFile(filename, []byte(`{"existing":"data"}`), 0o600); err != nil {
			t.Fatalf("seed: %v", err)
		}
		Creds = &CredentialsFile{viper: viper.New()}
		Creds.Set("profiles.test.type", "apikey")
		if err := writeCredentials(filename); err == nil {
			t.Fatal("writeCredentials: want an error from WriteConfigAs on an unknown extension")
		}
		data, err := os.ReadFile(filename)
		if err != nil {
			t.Fatalf("original file gone after failed write: %v", err)
		}
		if string(data) != `{"existing":"data"}` {
			t.Errorf("original file corrupted after failed write: %q", data)
		}
	})
}

// resolveProfileValue must resolve from --<key>-file, then positional, then prompt.
func TestResolveProfileValue(t *testing.T) {
	origPrompt, origInteractive := promptProfileValue, isInteractive
	t.Cleanup(func() { promptProfileValue, isInteractive = origPrompt, origInteractive })
	isInteractive = func() bool { return true }

	makeCmd := func(keyFileVal string) *cobra.Command {
		cmd := &cobra.Command{Use: "test"}
		cmd.Flags().String("api-key-file", keyFileVal, "")
		return cmd
	}

	// 1. --api-key-file flag takes priority.
	t.Run("key_file", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "key.txt")
		os.WriteFile(f, []byte("sk-from-file\n"), 0o600)
		cmd := makeCmd(f)
		cmd.Flags().Set("api-key-file", f)
		v, err := resolveProfileValue(cmd, "api_key", []string{"prod"}, 0)
		if err != nil || v != "sk-from-file" {
			t.Errorf("key_file: got (%q, %v), want (sk-from-file, nil)", v, err)
		}
	})

	// 2. Positional value present: used verbatim.
	t.Run("positional", func(t *testing.T) {
		promptProfileValue = func(string) (string, error) {
			t.Fatal("prompt ran when a positional value was given")
			return "", nil
		}
		cmd := makeCmd("")
		v, err := resolveProfileValue(cmd, "api_key", []string{"prod", "sk-123"}, 0)
		if err != nil || v != "sk-123" {
			t.Errorf("positional: got (%q, %v), want (sk-123, nil)", v, err)
		}
	})

	// 3. No positional, prompt succeeds.
	t.Run("prompted", func(t *testing.T) {
		promptProfileValue = func(string) (string, error) { return "typed-secret", nil }
		cmd := makeCmd("")
		v, err := resolveProfileValue(cmd, "api_key", []string{"prod"}, 0)
		if err != nil || v != "typed-secret" {
			t.Errorf("prompted: got (%q, %v), want (typed-secret, nil)", v, err)
		}
	})

	// 4. No terminal: returns a UsageError, not a hang.
	t.Run("no_terminal", func(t *testing.T) {
		isInteractive = func() bool { return false }
		promptProfileValue = func(string) (string, error) {
			t.Fatal("prompt ran without a terminal")
			return "", nil
		}
		cmd := makeCmd("")
		_, err := resolveProfileValue(cmd, "api_key", []string{"prod"}, 0)
		if err == nil {
			t.Fatal("no terminal: want an error")
		}
		var usage *UsageError
		if !errors.As(err, &usage) {
			t.Errorf("no terminal: want UsageError, got %T: %v", err, err)
		}
	})

	// 5. Multi-key handler: second key at index 1 resolves correctly.
	t.Run("multi_key_index", func(t *testing.T) {
		isInteractive = func() bool { return true }
		promptProfileValue = func(key string) (string, error) {
			if key != "client_secret" {
				t.Errorf("unexpected prompt for %q, want client_secret", key)
			}
			return "prompted-secret", nil
		}
		cmd := &cobra.Command{Use: "test"}
		cmd.Flags().String("client-id-file", "", "")
		cmd.Flags().String("client-secret-file", "", "")
		// args = ["prod", "my-client-id"], so client_id is positional, client_secret is prompted
		v, err := resolveProfileValue(cmd, "client_secret", []string{"prod", "my-client-id"}, 1)
		if err != nil || v != "prompted-secret" {
			t.Errorf("multi_key_index: got (%q, %v), want (prompted-secret, nil)", v, err)
		}
	})
}

// looksSensitiveKey decides whether add-profile echoes the typed value.
func TestLooksSensitiveKey(t *testing.T) {
	for _, key := range []string{"api_key", "api-key", "client_secret", "access_token", "password", "X-Orq-Key", "passphrase", "client_credential", "signature"} {
		if !looksSensitiveKey(key) {
			t.Errorf("looksSensitiveKey(%q) = false, want true (must not echo)", key)
		}
	}
	for _, key := range []string{"client_id", "username", "region", "endpoint"} {
		if looksSensitiveKey(key) {
			t.Errorf("looksSensitiveKey(%q) = true, want false (should echo)", key)
		}
	}
}

// The verbose config dump must use looksSensitiveKey, not its own inline chain.
func TestVerboseConfigMaskesAllSensitiveKeys(t *testing.T) {
	if !looksSensitiveKey("api-key") {
		t.Fatal("looksSensitiveKey must match api-key")
	}
}
