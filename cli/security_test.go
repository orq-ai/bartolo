package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
)

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
