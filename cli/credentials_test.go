package cli

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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

	if err := saveAuthProfile("", "default", []string{"api-key"}, []string{"secret"}, ""); err != nil {
		t.Fatalf("saveAuthProfile: %v", err)
	}

	if got := GetProfile()["api_key"]; got != "secret" {
		t.Fatalf("expected saved api key, got %q", got)
	}

	if _, err := os.Stat(filepath.Join(home, ".test-auth", "credentials.json")); err != nil {
		t.Fatalf("credentials.json not written: %v", err)
	}
}

// resetAuthState clears the auth singletons and points HOME at a temp dir.
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

// serverFixture builds a CLI with one spec server and an `acme` profile bound
// to profileServer, or to nothing when it is empty.
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
	if err := saveAuthProfile("", "acme", keys, values, profileServer); err != nil {
		t.Fatalf("saveAuthProfile: %v", err)
	}
	viper.Set("profile", "acme")
}

// writeConfig seeds config.json before Init reads it.
func writeConfig(t *testing.T, home, body string) {
	t.Helper()
	dir := filepath.Join(home, ".test-auth")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
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
	writeConfig(t, home, `{"server-default":"https://config.example.com"}`)
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
	writeConfig(t, home, `{"server-default":"https://config.example.com"}`)
	serverFixtureNoReset(t, "https://orq.acme.internal")

	if got := ResolveServer(); got != "https://orq.acme.internal" {
		t.Fatalf("expected profile server over config.json server, got %q", got)
	}
}

// orq-cli's OAuth bridge sets the server via viper.Set, which viper ranks highest.
func TestResolveServerPrefersViperSetOverProfile(t *testing.T) {
	serverFixture(t, "https://orq.acme.internal")

	viper.Set("server", "https://session.example.com")

	if got := ResolveServer(); got != "https://session.example.com" {
		t.Fatalf("expected programmatic override, got %q", got)
	}
}

// With no env prefix, viper's mergeWithEnvPrefix reads a bare SERVER.
func TestResolveServerPrefersEnvOverProfileWithoutEnvPrefix(t *testing.T) {
	t.Setenv("SERVER", "https://bare-env.example.com")
	resetAuthState(t)
	Init(&Config{AppName: "test-auth"})
	UseAuth("", stubAuthHandler{})
	RegisterServers([]map[string]string{{"description": "Prod", "url": "https://prod.example.com"}})
	if err := saveAuthProfile("", "acme", []string{"api-key", "server"}, []string{"secret", "https://orq.acme.internal"}, ""); err != nil {
		t.Fatalf("saveAuthProfile: %v", err)
	}
	viper.Set("profile", "acme")

	if got := ResolveServer(); got != "https://bare-env.example.com" {
		t.Fatalf("expected bare SERVER env override, got %q", got)
	}
}

// A config.json written before the persisted default moved keys still resolves.
func TestResolveServerReadsLegacyConfigServerKey(t *testing.T) {
	home := resetAuthState(t)
	writeConfig(t, home, `{"server":"https://legacy.example.com"}`)
	serverFixtureNoReset(t, "")

	if got := ResolveServer(); got != "https://legacy.example.com" {
		t.Fatalf("expected legacy config server, got %q", got)
	}
}

// The legacy key shares `server` with the flag, so it must be migrated out.
func TestResolveServerPrefersProfileOverLegacyConfigServerKey(t *testing.T) {
	home := resetAuthState(t)
	writeConfig(t, home, `{"server":"https://legacy.example.com"}`)
	serverFixtureNoReset(t, "https://orq.acme.internal")

	if got := ResolveServer(); got != "https://orq.acme.internal" {
		t.Fatalf("expected profile server over legacy config key, got %q", got)
	}

	migrated, err := os.ReadFile(filepath.Join(home, ".test-auth", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(migrated), `"server"`) {
		t.Fatalf("expected legacy key removed, got %s", migrated)
	}
	if !strings.Contains(string(migrated), "server-default") {
		t.Fatalf("expected value moved to server-default, got %s", migrated)
	}
}

func TestAuthSetupBindsServer(t *testing.T) {
	resetAuthState(t)
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("", stubAuthHandler{})

	if err := saveAuthProfile("", "acme", []string{"api-key"}, []string{"secret"}, "https://wizard.example.com"); err != nil {
		t.Fatalf("saveAuthProfile: %v", err)
	}
	viper.Set("profile", "acme")

	if got := GetProfile()["server"]; got != "https://wizard.example.com" {
		t.Fatalf("expected wizard-bound server, got %q", got)
	}
}

func TestListProfilesRendersServerColumn(t *testing.T) {
	serverFixture(t, "https://orq.acme.internal")

	out := captureStdout(t, func() { execute("auth list-profiles") })

	if !strings.Contains(out, "https://orq.acme.internal") {
		t.Fatalf("expected profile server in table, got %q", out)
	}
	if !strings.Contains(strings.ToUpper(out), "SERVER") {
		t.Fatalf("expected SERVER column header, got %q", out)
	}
}

func TestResolveServerPrefersConfigServerOverServerIndex(t *testing.T) {
	home := resetAuthState(t)
	writeConfig(t, home, `{"server-default":"https://config.example.com"}`)
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

	out := captureStdout(t, func() { execute("auth list-profiles") })

	if !strings.Contains(out, "acme") {
		t.Fatalf("expected profile row, got %q", out)
	}
}

// captureStdout collects os.Stdout, which is where tablewriter renders.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	original := os.Stdout
	os.Stdout = write
	defer func() { os.Stdout = original }()

	fn()
	write.Close()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(read); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}
