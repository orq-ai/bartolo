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

// resetAuthState clears the package-level auth singletons so each test can call
// Init/UseAuth against a fresh temporary HOME.
func resetAuthState(t *testing.T) string {
	t.Helper()

	viper.Reset()
	Cache = nil
	Client = nil
	Root = nil
	Creds = nil
	authInitialized = false
	authCommand = nil
	authAddCommand = nil
	AuthHandlers = make(map[string]AuthHandler)
	registeredServers = nil

	home := t.TempDir()
	oldHome := os.Getenv("HOME")
	if err := os.Setenv("HOME", home); err != nil {
		t.Fatalf("set HOME: %v", err)
	}
	t.Cleanup(func() { os.Setenv("HOME", oldHome) })

	return home
}

// serverFixture initialises a CLI with one registered spec server and an
// `acme` profile. Pass profileServer "" for a profile with no bound server.
func serverFixture(t *testing.T, profileServer string) {
	t.Helper()
	resetAuthState(t)
	serverFixtureNoReset(t, profileServer)
}

// serverFixtureNoReset is serverFixture for tests that seed HOME first.
func serverFixtureNoReset(t *testing.T, profileServer string) {
	t.Helper()
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("", stubAuthHandler{})
	RegisterServers([]map[string]string{{"description": "Prod", "url": "https://prod.example.com"}})

	keys, values := []string{"api-key"}, []string{"secret"}
	if profileServer != "" {
		keys, values = append(keys, "server"), append(values, profileServer)
	}
	if err := saveAuthProfile("", "acme", keys, values); err != nil {
		t.Fatalf("saveAuthProfile: %v", err)
	}
	viper.Set("profile", "acme")
}

// writeConfigServer persists a `server set` style override into config.json
// before Init reads it.
func writeConfigServer(t *testing.T, home, url string) {
	t.Helper()
	dir := filepath.Join(home, ".test-auth")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"server":"`+url+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAddProfileStoresServerFromGlobalFlag(t *testing.T) {
	resetAuthState(t)
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("", stubAuthHandler{})

	execute("auth add-profile --server https://orq.acme.internal acme secret")

	viper.Set("profile", "acme")
	profile := GetProfile()
	if got := profile["api_key"]; got != "secret" {
		t.Fatalf("expected saved api key, got %q", got)
	}
	if got := profile["server"]; got != "https://orq.acme.internal" {
		t.Fatalf("expected saved server, got %q", got)
	}
}

func TestAddProfileIgnoresEnvAndConfigServer(t *testing.T) {
	home := resetAuthState(t)
	writeConfigServer(t, home, "https://config.example.com")
	t.Setenv("TEST_AUTH_SERVER", "https://env.example.com")
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("", stubAuthHandler{})

	execute("auth add-profile acme secret")

	viper.Set("profile", "acme")
	if got, ok := GetProfile()["server"]; ok {
		t.Fatalf("expected no server on profile, got %q", got)
	}
}

func TestResolveServerPrefersProfileOverServerIndex(t *testing.T) {
	serverFixture(t, "https://orq.acme.internal")

	if got := ResolveServer(); got != "https://orq.acme.internal" {
		t.Fatalf("expected profile server, got %q", got)
	}
}

func TestResolveServerPrefersFlagOverProfile(t *testing.T) {
	serverFixture(t, "https://orq.acme.internal")

	if err := Root.PersistentFlags().Set("server", "https://override.example.com"); err != nil {
		t.Fatal(err)
	}
	if got := ResolveServer(); got != "https://override.example.com" {
		t.Fatalf("expected flag override, got %q", got)
	}
}

func TestResolveServerPrefersEnvOverProfile(t *testing.T) {
	t.Setenv("TEST_AUTH_SERVER", "https://env.example.com")
	serverFixture(t, "https://orq.acme.internal")

	if got := ResolveServer(); got != "https://env.example.com" {
		t.Fatalf("expected env override, got %q", got)
	}
}

func TestResolveServerPrefersProfileOverConfigServer(t *testing.T) {
	home := resetAuthState(t)
	writeConfigServer(t, home, "https://config.example.com")
	serverFixtureNoReset(t, "https://orq.acme.internal")

	if got := ResolveServer(); got != "https://orq.acme.internal" {
		t.Fatalf("expected profile server over config.json server, got %q", got)
	}
}

func TestResolveServerPrefersConfigServerOverServerIndex(t *testing.T) {
	home := resetAuthState(t)
	writeConfigServer(t, home, "https://config.example.com")
	serverFixtureNoReset(t, "")

	if got := ResolveServer(); got != "https://config.example.com" {
		t.Fatalf("expected config.json server, got %q", got)
	}
}

func TestResolveServerFallsBackWhenProfileHasNoServer(t *testing.T) {
	serverFixture(t, "")

	if got := ResolveServer(); got != "https://prod.example.com" {
		t.Fatalf("expected generated server default, got %q", got)
	}
}

func TestListProfilesToleratesMissingServer(t *testing.T) {
	serverFixture(t, "")

	// The table goes to os.Stdout; the assertion is that a profile without a
	// `server` field renders instead of panicking on a nil type assertion.
	execute("auth list-profiles")
}
