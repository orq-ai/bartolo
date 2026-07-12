package cli

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"
	"github.com/spf13/viper"
)

type stubAuthHandler struct{}

func (stubAuthHandler) ProfileKeys() []string                              { return []string{"api-key"} }
func (stubAuthHandler) OnRequest(_ *zerolog.Logger, _ *http.Request) error { return nil }

func TestSaveAuthProfile(t *testing.T) {
	viper.Reset()
	Cache = nil
	Client = nil
	Root = nil
	Creds = nil
	authInitialized = false
	AuthHandlers = make(map[string]AuthHandler)

	home := t.TempDir()
	oldHome := os.Getenv("HOME")
	if err := os.Setenv("HOME", home); err != nil {
		t.Fatalf("set HOME: %v", err)
	}
	defer os.Setenv("HOME", oldHome)

	Init(&Config{
		AppName:   "test-auth",
		EnvPrefix: "TEST_AUTH",
	})
	UseAuth("", stubAuthHandler{})

	if err := saveAuthProfile("", "default", []string{"api-key"}, []string{"secret"}); err != nil {
		t.Fatalf("saveAuthProfile: %v", err)
	}

	if got := GetProfile()["api_key"]; got != "secret" {
		t.Fatalf("expected saved api key, got %q", got)
	}

	if _, err := os.Stat(filepath.Join(home, ".test-auth", "credentials.json")); err != nil {
		t.Fatalf("credentials.json not written: %v", err)
	}
}

func TestResolveAuthHandlerDefault(t *testing.T) {
	AuthHandlers = make(map[string]AuthHandler)
	defaultAuthType = ""
	t.Cleanup(func() {
		AuthHandlers = make(map[string]AuthHandler)
		defaultAuthType = ""
	})

	AuthHandlers["alpha"] = stubAuthHandler{}
	AuthHandlers["beta"] = stubAuthHandler{}

	// Two typed handlers, typeless profile, no default → nil (error preserved).
	if name, handler := resolveAuthHandler(map[string]string{}); handler != nil || name != "" {
		t.Fatalf("expected nil handler without default, got %q/%v", name, handler)
	}

	// Explicit profile type wins.
	if name, handler := resolveAuthHandler(map[string]string{"type": "beta"}); name != "beta" || handler == nil {
		t.Fatalf("expected beta handler for explicit type, got %q/%v", name, handler)
	}

	// Default handler used for a typeless profile.
	SetDefaultAuthType("alpha")
	if name, handler := resolveAuthHandler(map[string]string{}); name != "alpha" || handler == nil {
		t.Fatalf("expected alpha handler from default, got %q/%v", name, handler)
	}

	// Explicit type still wins over a configured default.
	if name, handler := resolveAuthHandler(map[string]string{"type": "beta"}); name != "beta" || handler == nil {
		t.Fatalf("expected explicit type to win over default, got %q/%v", name, handler)
	}
}
