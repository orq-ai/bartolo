package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
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

// resetAuthSingletons clears the package-level auth state so a fresh Init can
// run without tripping over flags or commands registered by a prior test (or,
// within one test, a prior simulated process).
func resetAuthSingletons() {
	viper.Reset()
	Cache = nil
	Client = nil
	Root = nil
	Creds = nil
	authInitialized = false
	authCommand = nil
	profileCommand = nil
	authAddCommands = nil
	AuthHandlers = make(map[string]AuthHandler)
	registeredServers = nil
}

// resetAuthState clears the auth singletons and points HOME at a temp dir.
func resetAuthState(t *testing.T) string {
	t.Helper()

	resetAuthSingletons()

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

func TestResolveServerPrefersProfileOverEnv(t *testing.T) {
	t.Setenv("TEST_AUTH_SERVER", "https://env.example.com")
	serverFixture(t, "https://orq.acme.internal")

	if got := ResolveServer(); got != "https://orq.acme.internal" {
		t.Fatalf("expected profile server over env, got %q", got)
	}
}

func TestResolveServerFallsBackToEnvWithoutProfileServer(t *testing.T) {
	t.Setenv("TEST_AUTH_SERVER", "https://env.example.com")
	serverFixture(t, "")

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

// orq-cli's OAuth bridge sets the server via viper.Set. It ranks with the
// environment now: above the generated defaults, below the chosen profile.
func TestResolveServerPrefersViperSetOverServerIndex(t *testing.T) {
	serverFixture(t, "")

	viper.Set("server", "https://session.example.com")

	if got := ResolveServer(); got != "https://session.example.com" {
		t.Fatalf("expected programmatic override, got %q", got)
	}
}

// With no env prefix, viper's mergeWithEnvPrefix reads a bare SERVER.
func TestResolveServerReadsEnvWithoutEnvPrefix(t *testing.T) {
	t.Setenv("SERVER", "https://bare-env.example.com")
	resetAuthState(t)
	Init(&Config{AppName: "test-auth"})
	UseAuth("", stubAuthHandler{})
	RegisterServers([]map[string]string{{"description": "Prod", "url": "https://prod.example.com"}})
	if err := saveAuthProfile("", "acme", []string{"api-key"}, []string{"secret"}, ""); err != nil {
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

	out := execute("auth list-profiles")

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

	out := execute("auth list-profiles")

	if !strings.Contains(out, "acme") {
		t.Fatalf("expected profile row, got %q", out)
	}
}

func TestMaskIfSecret(t *testing.T) {
	for _, field := range []string{"api_key", "API-Key", "access_token", "client_secret", "password"} {
		assert.Equal(t, "sk-o********mnop", maskIfSecret(field, "sk-orq-abcdefghijklmnop"), field)
	}

	// A short secret shows nothing at all, and the mask width never tracks the
	// real length.
	assert.Equal(t, "********", maskIfSecret("api_key", "sk-orq-abcd"))

	// Multi-byte secrets are cut on rune boundaries, not bytes.
	assert.Equal(t, "日本語で********密ですね", maskIfSecret("password", "日本語ですごく長い秘密ですね"))

	// Non-secret fields pass through untouched.
	assert.Equal(t, "https://api.orq.ai", maskIfSecret("base_url", "https://api.orq.ai"))
	assert.Equal(t, "prod", maskIfSecret("type", "prod"))
}

func TestListProfilesMasksSecretsAndHonorsJSON(t *testing.T) {
	resetAuthState(t)
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("", stubAuthHandler{})

	const secret = "sk-orq-abcdefghijklmnop"
	if err := saveAuthProfile("", "acme", []string{"api-key"}, []string{secret}, "https://acme.example.com"); err != nil {
		t.Fatalf("saveAuthProfile: %v", err)
	}

	viper.Set("output-format", "json")
	out := execute("auth profile list --json")

	assert.NotContains(t, out, secret)

	var decoded struct {
		Profiles []map[string]interface{} `json:"profiles"`
	}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	assert.Len(t, decoded.Profiles, 1)
	assert.Equal(t, "acme", decoded.Profiles[0]["name"])
	assert.Equal(t, "sk-o********mnop", decoded.Profiles[0]["api_key"])
	assert.Equal(t, "https://acme.example.com", decoded.Profiles[0]["server"])
}

func TestAuthUseSetsActiveProfile(t *testing.T) {
	serverFixture(t, "")
	viper.Set("profile", "")

	execute("auth use acme")

	assert.Equal(t, "acme", ActiveProfileName())
	assert.Equal(t, "secret", GetProfile()["api_key"])

	data, err := os.ReadFile(filepath.Join(viper.GetString("config-directory"), "config.json"))
	assert.NoError(t, err)
	assert.Contains(t, string(data), `"profile-selected": "acme"`)
}

// `auth use` persists off the flag's viper key. Writing it to `profile` would
// land on viper's override layer, which outranks a bound flag, so `--profile`
// would stop working for the rest of the process.
func TestExplicitProfileBeatsAuthUse(t *testing.T) {
	serverFixture(t, "")
	execute("auth use acme")

	assert.NoError(t, Root.PersistentFlags().Set("profile", "other"))
	assert.Equal(t, "other", ActiveProfileName())
}

func TestAuthUseRejectsUnknownProfile(t *testing.T) {
	serverFixture(t, "")

	assert.Contains(t, execute("auth use nope"), `unknown profile "nope"`)
	assert.Equal(t, "acme", ActiveProfileName(), "a rejected profile must not change the selection")
}

func TestActiveProfileNameAndListingAreCaseInsensitive(t *testing.T) {
	serverFixture(t, "")
	assert.NoError(t, Root.PersistentFlags().Set("profile", "ACME"))

	assert.Equal(t, "acme", ActiveProfileName())
	assert.Contains(t, execute("auth profile list --json"), `"active": true`)
}

func TestAuthProfileClearDropsPersistedSelection(t *testing.T) {
	serverFixture(t, "")
	viper.Set("profile", "")
	execute("auth profile use acme")

	execute("auth profile clear")

	assert.Empty(t, ActiveProfileName())
	assert.False(t, ProfileSelected())
}

func TestEmptyProfileFlagDisablesProfiles(t *testing.T) {
	serverFixture(t, "https://orq.acme.internal")
	execute("auth profile use acme")

	assert.NoError(t, Root.PersistentFlags().Set("profile", ""))

	assert.Empty(t, GetProfile())
	assert.Equal(t, "https://prod.example.com", ResolveServer())
}

func TestAddProfileRejectsEmptyKey(t *testing.T) {
	resetAuthState(t)
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("", stubAuthHandler{})

	err := saveAuthProfile("", "acme", []string{"api-key"}, []string{"  "}, "")

	assert.ErrorContains(t, err, "api-key cannot be empty")
	assert.False(t, ProfileExists("acme"))
}

// An install that predates the removal of the implicit `default` profile keeps
// resolving it, without the name being special at resolution time.
func TestLegacyDefaultProfileIsAdoptedOnce(t *testing.T) {
	home := resetAuthState(t)
	dir := filepath.Join(home, ".test-auth")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"profiles":{"default":{"type":"","api_key":"secret"}}}`
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("", stubAuthHandler{})

	assert.Equal(t, "default", ActiveProfileName())
	assert.Equal(t, "secret", GetProfile()["api_key"])
}

func TestFirstProfileBecomesActive(t *testing.T) {
	resetAuthState(t)
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("", stubAuthHandler{})

	execute("auth profile add acme secret")

	assert.Equal(t, "acme", ActiveProfileName())
}

// TestClearedLegacyProfileStaysClearedAcrossRestart is the regression test for
// the Critical defect where `auth profile clear` was silently undone. Adoption
// used to guard only on `profile-selected` being empty, so deleting that key
// looked identical to never having adopted anything, and the very next process
// re-adopted `default` and rewrote config.json.
func TestClearedLegacyProfileStaysClearedAcrossRestart(t *testing.T) {
	home := resetAuthState(t)
	dir := filepath.Join(home, ".test-auth")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"profiles":{"default":{"type":"","api_key":"secret"}}}`
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	// First process: adoption fires on init, then the user turns profiles off.
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("", stubAuthHandler{})
	if got := ActiveProfileName(); got != "default" {
		t.Fatalf("expected adoption to select %q, got %q", "default", got)
	}

	execute("auth profile clear")
	if got := ActiveProfileName(); got != "" {
		t.Fatalf("expected clear to empty the active profile, got %q", got)
	}

	// Second process against the same HOME: re-run init from scratch.
	resetAuthSingletons()
	oldHome := os.Getenv("HOME")
	if err := os.Setenv("HOME", home); err != nil {
		t.Fatalf("set HOME: %v", err)
	}
	t.Cleanup(func() { os.Setenv("HOME", oldHome) })

	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("", stubAuthHandler{})

	assert.Empty(t, ActiveProfileName(), "clearing an adopted legacy profile must survive a restart")
}

// TestAdoptLegacyDefaultProfile tables the first-run adoption behaviour across
// the states that matter: no profiles, only `default`, a non-default profile,
// and a selection already persisted. Each entry runs init twice and asserts
// config.json after both runs.
func TestAdoptLegacyDefaultProfile(t *testing.T) {
	tests := []struct {
		name              string
		credentialsBody   string
		seedConfig        string
		wantSelectedAfter string
		wantAdoptedAfter  bool
	}{
		{
			name:              "no profiles: nothing adopted",
			credentialsBody:   `{}`,
			wantSelectedAfter: "",
			wantAdoptedAfter:  false,
		},
		{
			name:              "profiles.default: adopted once",
			credentialsBody:   `{"profiles":{"default":{"type":"","api_key":"secret"}}}`,
			wantSelectedAfter: "default",
			wantAdoptedAfter:  true,
		},
		{
			name:              "profiles.work only: nothing adopted",
			credentialsBody:   `{"profiles":{"work":{"type":"","api_key":"secret"}}}`,
			wantSelectedAfter: "",
			wantAdoptedAfter:  false,
		},
		{
			name:              "profiles.default with a selection already persisted: not overwritten",
			credentialsBody:   `{"profiles":{"default":{"type":"","api_key":"secret"},"work":{"type":"","api_key":"secret2"}}}`,
			seedConfig:        `{"profile-selected":"work"}`,
			wantSelectedAfter: "work",
			wantAdoptedAfter:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := resetAuthState(t)
			dir := filepath.Join(home, ".test-auth")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "credentials.json"), []byte(tt.credentialsBody), 0o600); err != nil {
				t.Fatal(err)
			}
			if tt.seedConfig != "" {
				writeConfig(t, home, tt.seedConfig)
			}

			Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
			UseAuth("", stubAuthHandler{})

			assert.Equal(t, tt.wantSelectedAfter, sanitizeProfileName(viper.GetString("profile-selected")))
			assert.Equal(t, tt.wantAdoptedAfter, viper.GetBool("profile-adopted"))

			firstRun, err := os.ReadFile(filepath.Join(dir, "config.json"))
			if tt.wantAdoptedAfter || tt.seedConfig != "" {
				if err != nil {
					t.Fatalf("read config.json after first init: %v", err)
				}
			}

			// Re-run init against the same HOME: the second run must not rewrite
			// what the first run already settled.
			resetAuthSingletons()
			oldHome := os.Getenv("HOME")
			if setErr := os.Setenv("HOME", home); setErr != nil {
				t.Fatalf("set HOME: %v", setErr)
			}
			t.Cleanup(func() { os.Setenv("HOME", oldHome) })

			Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
			UseAuth("", stubAuthHandler{})

			assert.Equal(t, tt.wantSelectedAfter, sanitizeProfileName(viper.GetString("profile-selected")))
			assert.Equal(t, tt.wantAdoptedAfter, viper.GetBool("profile-adopted"))

			secondRun, err := os.ReadFile(filepath.Join(dir, "config.json"))
			if err == nil && firstRun != nil {
				assert.Equal(t, string(firstRun), string(secondRun), "second init must not rewrite a settled config.json")
			}
		})
	}
}

// TestSaveJSONConfigRejectsMalformedExistingFile is the regression test for
// the Critical defect where a corrupt config.json got silently replaced by an
// empty one, destroying every other key it held.
func TestSaveJSONConfigRejectsMalformedExistingFile(t *testing.T) {
	home := resetAuthState(t)
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})

	dir := filepath.Join(home, ".test-auth")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(dir, "config.json")
	const malformed = `{"server-default": "https://acme.example.com",`
	if err := os.WriteFile(filename, []byte(malformed), 0o600); err != nil {
		t.Fatal(err)
	}

	err := saveJSONConfig(map[string]interface{}{"profile-selected": "acme"})

	assert.Error(t, err)

	data, readErr := os.ReadFile(filename)
	if readErr != nil {
		t.Fatalf("read config.json: %v", readErr)
	}
	assert.Equal(t, malformed, string(data), "a malformed config.json must be left untouched")
}
