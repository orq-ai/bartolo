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
