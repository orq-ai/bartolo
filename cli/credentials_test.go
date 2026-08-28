package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
)

type stubAuthHandler struct{}

func (stubAuthHandler) ProfileKeys() []string                              { return []string{"api-key"} }
func (stubAuthHandler) OnRequest(_ *zerolog.Logger, _ *http.Request) error { return nil }

// stubOptionalKeyHandler declares a required credential plus an optional
// extra field (e.g. a region), narrowing what must be present via
// RequiredKeysHandler the same way apikey.Handler does.
type stubOptionalKeyHandler struct{}

func (stubOptionalKeyHandler) ProfileKeys() []string { return []string{"api-key", "region"} }
func (stubOptionalKeyHandler) RequiredProfileKeys() []string {
	return []string{"api-key"}
}
func (stubOptionalKeyHandler) OnRequest(_ *zerolog.Logger, _ *http.Request) error { return nil }

// stubTwoKeyHandler declares two profile keys but does not implement
// RequiredKeysHandler, so both must stay required.
type stubTwoKeyHandler struct{}

func (stubTwoKeyHandler) ProfileKeys() []string                              { return []string{"api-key", "region"} }
func (stubTwoKeyHandler) OnRequest(_ *zerolog.Logger, _ *http.Request) error { return nil }

// stubMismatchedSpellingHandler spells its required key `api_key` and its
// declared key `api-key`, as apikey and oauth do in-tree. A raw comparison
// would not match them, silently making the credential optional.
type stubMismatchedSpellingHandler struct{}

func (stubMismatchedSpellingHandler) ProfileKeys() []string { return []string{"api-key"} }
func (stubMismatchedSpellingHandler) RequiredProfileKeys() []string {
	return []string{"api_key"}
}
func (stubMismatchedSpellingHandler) OnRequest(_ *zerolog.Logger, _ *http.Request) error { return nil }

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
	authAddDeprecationNotices = map[*cobra.Command]string{}
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

// `auth add-profile` is a deprecated alias for `auth profile add`, but it
// shipped on origin/main with an `add` alias of its own (`auth add`). This
// branch dropped that alias without listing the break; it must be restored.
func TestAuthAddAliasStillResolves(t *testing.T) {
	resetAuthState(t)
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("", stubAuthHandler{})

	execute("auth add acme secret")

	viper.Set("profile", "acme")
	if got := GetProfile()["api_key"]; got != "secret" {
		t.Fatalf("expected saved api key via `auth add` alias, got %q", got)
	}
}

// The notice must land on stderr so stdout stays parseable. Cobra's own
// Deprecated field prints through OutOrStderr, which corrupts `--json`.
func TestAddProfileDeprecatedAliasWarnsOnStderr(t *testing.T) {
	resetAuthState(t)
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("", stubAuthHandler{})

	stdout, stderr := executeStreams("auth add-profile acme secret")

	assert.Contains(t, stderr, `"add-profile" is deprecated`)
	assert.Contains(t, stderr, "auth profile add")
	assert.NotContains(t, stdout, "deprecated")
}

// Cobra runs only the leaf's hooks, so a notice on `add-profile` never fired
// for a typed subcommand nested under it. It must fire for that spelling too.
func TestAddProfileDeprecatedAliasWarnsOnStderrForTypedSubcommand(t *testing.T) {
	resetAuthState(t)
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("oauth", stubAuthHandler{})

	stdout, stderr := executeStreams("auth add-profile oauth acme secret")

	assert.Contains(t, stderr, `"oauth" is deprecated`)
	assert.Contains(t, stderr, "auth profile add")
	assert.NotContains(t, stdout, "deprecated")
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

// `auth profile list --json` must emit the same object shape either way: the
// "message" key used to appear only when no profiles existed.
func TestListProfilesJSONShapeIsStableAcrossEmptyAndNonEmpty(t *testing.T) {
	resetAuthState(t)
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("", stubAuthHandler{})

	viper.Set("output-format", "json")
	emptyOut := execute("auth profile list --json")

	var emptyDecoded map[string]interface{}
	if err := json.Unmarshal([]byte(emptyOut), &emptyDecoded); err != nil {
		t.Fatalf("empty output is not JSON: %v\n%s", err, emptyOut)
	}
	_, hasMessageWhenEmpty := emptyDecoded["message"]
	assert.True(t, hasMessageWhenEmpty, "expected a \"message\" key when no profiles are configured")
	assert.Empty(t, emptyDecoded["profiles"])

	if err := saveAuthProfile("", "acme", []string{"api-key"}, []string{"secret"}, ""); err != nil {
		t.Fatalf("saveAuthProfile: %v", err)
	}
	nonEmptyOut := execute("auth profile list --json")

	var nonEmptyDecoded map[string]interface{}
	if err := json.Unmarshal([]byte(nonEmptyOut), &nonEmptyDecoded); err != nil {
		t.Fatalf("non-empty output is not JSON: %v\n%s", err, nonEmptyOut)
	}
	_, hasMessageWhenNonEmpty := nonEmptyDecoded["message"]
	assert.Equal(t, hasMessageWhenEmpty, hasMessageWhenNonEmpty, "the \"message\" key must be present (or absent) consistently, regardless of profile count")
}

// Regression test for the Critical defect: cobra's `Deprecated` field prints
// its notice through the command's *out* writer once one is set, so it used
// to land on stdout ahead of the JSON payload and break
// `auth list-profiles --json | jq`. The notice must go to stderr instead,
// leaving stdout as valid JSON.
func TestListProfilesDeprecatedAliasKeepsJSONCleanOnStdout(t *testing.T) {
	resetAuthState(t)
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("", stubAuthHandler{})

	if err := saveAuthProfile("", "acme", []string{"api-key"}, []string{"secret"}, ""); err != nil {
		t.Fatalf("saveAuthProfile: %v", err)
	}

	viper.Set("output-format", "json")
	stdout, stderr := executeStreams("auth list-profiles --json")

	var decoded struct {
		Profiles []map[string]interface{} `json:"profiles"`
	}
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	assert.Len(t, decoded.Profiles, 1)

	assert.Contains(t, stderr, `"list-profiles" is deprecated`)
	assert.Contains(t, stderr, "auth profile list")
}

func TestAuthProfileUseSetsActiveProfile(t *testing.T) {
	serverFixture(t, "")
	viper.Set("profile", "")

	execute("auth profile use acme")

	assert.Equal(t, "acme", ActiveProfileName())
	assert.Equal(t, "secret", GetProfile()["api_key"])

	data, err := os.ReadFile(filepath.Join(viper.GetString("config-directory"), "config.json"))
	assert.NoError(t, err)
	assert.Contains(t, string(data), `"profile-selected": "acme"`)
}

// `auth profile use` persists off the flag's viper key. Writing it to
// `profile` would land on viper's override layer, which outranks a bound
// flag, so `--profile` would stop working for the rest of the process.
func TestExplicitProfileBeatsAuthProfileUse(t *testing.T) {
	serverFixture(t, "")
	execute("auth profile use acme")

	assert.NoError(t, Root.PersistentFlags().Set("profile", "other"))
	assert.Equal(t, "other", ActiveProfileName())
}

// Pins every pairing of the flag/env/persisted ranking. Without the full
// table, swapping the env and persisted rungs has no defended answer.
func TestActiveProfileNamePrecedenceAcrossAllRungs(t *testing.T) {
	tests := []struct {
		name         string
		setFlag      string
		setEnv       string
		setPersisted string
		want         string
	}{
		{name: "flag alone", setFlag: "flag-profile", want: "flag-profile"},
		{name: "env alone", setEnv: "env-profile", want: "env-profile"},
		{name: "persisted alone", setPersisted: "persisted-profile", want: "persisted-profile"},
		{name: "flag beats env", setFlag: "flag-profile", setEnv: "env-profile", want: "flag-profile"},
		{name: "flag beats persisted", setFlag: "flag-profile", setPersisted: "persisted-profile", want: "flag-profile"},
		{name: "env beats persisted", setEnv: "env-profile", setPersisted: "persisted-profile", want: "env-profile"},
		{name: "flag beats env and persisted together", setFlag: "flag-profile", setEnv: "env-profile", setPersisted: "persisted-profile", want: "flag-profile"},
		{name: "nothing set", want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resetAuthState(t)
			Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
			UseAuth("", stubAuthHandler{})

			if tc.setPersisted != "" {
				// The persisted rung must come from the real command, which
				// validates the profile exists before it writes the
				// selection, exactly like `auth profile use` does.
				Creds.Set("profiles."+tc.setPersisted+".api_key", "secret")
				execute("auth profile use " + tc.setPersisted)
			}
			if tc.setEnv != "" {
				t.Setenv("TEST_AUTH_PROFILE", tc.setEnv)
			}
			if tc.setFlag != "" {
				assert.NoError(t, Root.PersistentFlags().Set("profile", tc.setFlag))
			}

			assert.Equal(t, tc.want, ActiveProfileName())
		})
	}
}

func TestAuthProfileUseRejectsUnknownProfile(t *testing.T) {
	serverFixture(t, "")

	_, stderr := executeStreams("auth profile use nope")

	assert.Contains(t, stderr, `unknown profile "nope"`)
	assert.Equal(t, "acme", ActiveProfileName(), "a rejected profile must not change the selection")
}

// `auth use` never shipped and must not resolve to `auth profile use`. It
// surfaces as help text rather than an error, since `auth` has no RunE.
func TestAuthUseCommandIsGone(t *testing.T) {
	serverFixture(t, "")
	viper.Set("profile", "")

	configPath := filepath.Join(viper.GetString("config-directory"), "config.json")
	before, _ := os.ReadFile(configPath)

	stdout, _ := executeStreams("auth use acme")

	assert.Contains(t, stdout, "Available Commands", "expected auth's help text, not a resolved `use` command")
	assert.NotContains(t, stdout, "active_profile", "must not have run a profile switch")

	after, err := os.ReadFile(configPath)
	assert.NoError(t, err)
	assert.Equal(t, string(before), string(after), "must not have persisted a selection")
}

func TestActiveProfileNameAndListingAreCaseInsensitive(t *testing.T) {
	serverFixture(t, "")
	assert.NoError(t, Root.PersistentFlags().Set("profile", "ACME"))

	assert.Equal(t, "acme", ActiveProfileName())
	assert.Contains(t, execute("auth profile list --json"), `"active": true`)
}

// ProfileExists must agree with SelectProfile and ActiveProfileName, which
// both run their input through sanitizeProfileName: a caller that resolves a
// name through one of those and checks it through ProfileExists must see the
// same profile either way.
func TestProfileExistsSanitizesName(t *testing.T) {
	serverFixture(t, "")

	assert.True(t, ProfileExists("ACME"), "expected a differently-cased spelling of an existing profile to be found")
	assert.True(t, ProfileExists("  acme  "), "expected surrounding whitespace to be trimmed")
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

// A handler that narrows its required keys via RequiredKeysHandler must let
// a profile save with its non-credential field left empty.
func TestSaveAuthProfileAllowsOptionalKeyEmpty(t *testing.T) {
	resetAuthState(t)
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("optional", stubOptionalKeyHandler{})

	err := saveAuthProfile("optional", "acme", []string{"api-key", "region"}, []string{"secret", ""}, "")

	assert.NoError(t, err)
	profile := Creds.GetStringMapString("profiles.acme")
	assert.Equal(t, "secret", profile["api_key"])
	_, hasRegion := profile["region"]
	assert.False(t, hasRegion, "expected omitted optional region to be absent from the profile, not stored as \"\"")
}

// The same handler still rejects an empty credential even though it narrows
// its other keys.
func TestSaveAuthProfileStillRejectsEmptyCredential(t *testing.T) {
	resetAuthState(t)
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("optional", stubOptionalKeyHandler{})

	err := saveAuthProfile("optional", "acme", []string{"api-key", "region"}, []string{"  ", "eu-west"}, "")

	assert.ErrorContains(t, err, "api-key cannot be empty")
	assert.False(t, ProfileExists("acme"))
}

// A handler that does not implement RequiredKeysHandler keeps every declared
// key required, so nothing outside this repo breaks.
func TestSaveAuthProfileRequiresAllKeysWithoutInterface(t *testing.T) {
	resetAuthState(t)
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("twokey", stubTwoKeyHandler{})

	err := saveAuthProfile("twokey", "acme", []string{"api-key", "region"}, []string{"secret", ""}, "")

	assert.ErrorContains(t, err, "region cannot be empty")
	assert.False(t, ProfileExists("acme"))
}

// TestSaveAuthProfileRequiredKeyMatchingIgnoresSeparatorSpelling is the
// regression test for the Minor defect where required-key matching was an
// exact-string comparison across a codebase that spells profile keys two
// ways (`-` in ProfileKeys/flags, `_` once normalised and stored). A handler
// declaring `api-key` in ProfileKeys but `api_key` in RequiredProfileKeys
// must still reject an empty value for that key.
func TestSaveAuthProfileRequiredKeyMatchingIgnoresSeparatorSpelling(t *testing.T) {
	resetAuthState(t)
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("mismatched", stubMismatchedSpellingHandler{})

	err := saveAuthProfile("mismatched", "acme", []string{"api-key"}, []string{"  "}, "")

	assert.ErrorContains(t, err, "api-key cannot be empty")
	assert.False(t, ProfileExists("acme"))
}

// End-to-end through resolveProfileValue: an optional key may resolve to an
// empty positional while the required one arrives via --<key>-file. The
// trailing "" is its own vector element, as a shell sends a quoted empty string.
func TestAuthProfileAddOptionalKeyThroughRealCommandPath(t *testing.T) {
	resetAuthState(t)
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("optional", stubOptionalKeyHandler{})

	keyFile := filepath.Join(t.TempDir(), "api-key")
	if err := os.WriteFile(keyFile, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// `api-key` is resolved via the --api-key-file route; `acme` is the
	// profile name; the placeholder positional is the api-key positional
	// (ignored, since the file flag wins); the final "" is the optional
	// `region` positional, exercising the positional route with an empty
	// value.
	stdout, stderr, err := executeArgsStreams([]string{
		"auth", "profile", "add", "optional",
		"--api-key-file", keyFile, "acme", "placeholder", "",
	})

	assert.NoError(t, err)
	assert.Empty(t, stderr)
	_ = stdout
	profile := Creds.GetStringMapString("profiles.acme")
	assert.Equal(t, "secret", profile["api_key"])
	_, hasRegion := profile["region"]
	assert.False(t, hasRegion, "expected optional region left out of the profile via empty positional arg")
}

// The required key still fails through the real command path when resolved
// via an empty positional, passed as an explicit vector element -- a double
// space would collapse in a real shell -- and would
// therefore route "eu-west" into the required position instead.
func TestAuthProfileAddRequiredKeyEmptyThroughRealCommandPath(t *testing.T) {
	resetAuthState(t)
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("optional", stubOptionalKeyHandler{})

	_, stderr, err := executeArgsStreams([]string{
		"auth", "profile", "add", "optional", "acme", "", "eu-west",
	})

	assert.Error(t, err)
	assert.Contains(t, stderr, "api-key cannot be empty")
	assert.False(t, ProfileExists("acme"))
}

// resolveProfileValue used to demand a TTY before consulting `required`, so
// simply omitting an optional trailing positional failed non-interactively.
func TestAuthProfileAddOmittedOptionalPositionalSucceedsNonInteractively(t *testing.T) {
	resetAuthState(t)
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("optional", stubOptionalKeyHandler{})

	keyFile := filepath.Join(t.TempDir(), "api-key")
	if err := os.WriteFile(keyFile, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, stderr, err := executeArgsStreams([]string{
		"auth", "profile", "add", "optional", "--api-key-file", keyFile, "acme",
	})

	assert.NoError(t, err)
	assert.Empty(t, stderr)
	profile := Creds.GetStringMapString("profiles.acme")
	assert.Equal(t, "secret", profile["api_key"])
	_, hasRegion := profile["region"]
	assert.False(t, hasRegion, "expected the omitted optional region left out of the profile")
}

// Adoption reverses which credential goes on the wire — the environment key
// used to win, the profile now does — so it must say so once on stderr, and
// not again on a later run.
func TestLegacyAdoptionPrintsOneTimeNotice(t *testing.T) {
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
	errBuf := new(bytes.Buffer)
	Stderr = errBuf
	UseAuth("", stubAuthHandler{})

	assert.Equal(t, "default", ActiveProfileName())
	assert.Contains(t, errBuf.String(), `"default"`)
	assert.Contains(t, errBuf.String(), "auth profile clear")

	// A later process, once the decision is recorded, must stay silent.
	resetAuthSingletons()
	oldHome := os.Getenv("HOME")
	if err := os.Setenv("HOME", home); err != nil {
		t.Fatalf("set HOME: %v", err)
	}
	t.Cleanup(func() { os.Setenv("HOME", oldHome) })

	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	errBuf2 := new(bytes.Buffer)
	Stderr = errBuf2
	UseAuth("", stubAuthHandler{})

	assert.Empty(t, errBuf2.String(), "adoption must warn only once")
}

func TestFirstProfileBecomesActive(t *testing.T) {
	resetAuthState(t)
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("", stubAuthHandler{})

	execute("auth profile add acme secret")

	assert.Equal(t, "acme", ActiveProfileName())
}

// TestSecondProfileDoesNotStealSelectionFromFirst guards saveAuthProfile's
// `if !ProfileSelected()` auto-select: it must fire only for the first
// profile ever saved. TestFirstProfileBecomesActive only ever saves one
// profile, so it cannot catch the auto-select firing unconditionally
// (which would silently repoint every later command onto whatever scratch
// profile was added last, while a real profile like `prod` was in force).
func TestSecondProfileDoesNotStealSelectionFromFirst(t *testing.T) {
	resetAuthState(t)
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("", stubAuthHandler{})

	execute("auth profile add prod secret-prod")
	assert.Equal(t, "prod", ActiveProfileName(), "the first saved profile must become active")

	execute("auth profile add scratch secret-scratch")
	assert.Equal(t, "prod", ActiveProfileName(), "a later profile save must not repoint an already-active selection")

	data, err := os.ReadFile(filepath.Join(viper.GetString("config-directory"), "config.json"))
	assert.NoError(t, err)
	assert.Contains(t, string(data), `"profile-selected": "prod"`)
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

// Same defect as TestClearedLegacyProfileStaysClearedAcrossRestart, but for
// an install where adoption never fires: `default` is created fresh by
// `auth profile add`, so the first-profile auto-select is what must record
// the marker, or a later `clear` looks identical to "never decided".
func TestOnBranchDefaultProfileStaysClearedAcrossRestart(t *testing.T) {
	home := resetAuthState(t)
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("", stubAuthHandler{})

	// No `profiles.default` exists yet, so adoption is a no-op here.
	assert.Empty(t, ActiveProfileName())

	// The user creates a profile named `default` themselves, on this branch;
	// the first-profile auto-select puts it in force.
	execute("auth profile add default secret")
	if got := ActiveProfileName(); got != "default" {
		t.Fatalf("expected the first saved profile to become active, got %q", got)
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

	assert.Empty(t, ActiveProfileName(), "clearing an on-branch `default` profile must survive a restart")
}

// Seeding a selection of `work` leaves `clear` as the only command that can
// write the marker: neither adoption nor the first-profile auto-select runs,
// so neither can mask a missing marker write the way they would otherwise.
func TestClearOverridingPersistedSelectionStaysClearedAcrossRestart(t *testing.T) {
	home := resetAuthState(t)
	dir := filepath.Join(home, ".test-auth")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"profiles":{"default":{"type":"","api_key":"secret"},"work":{"type":"","api_key":"secret2"}}}`
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, home, `{"profile-selected":"work"}`)

	// First process: `work` is already selected, so adoption is a no-op and
	// nothing but `clear` ever touches the marker.
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("", stubAuthHandler{})
	if got := ActiveProfileName(); got != "work" {
		t.Fatalf("expected the pre-existing selection %q, got %q", "work", got)
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

	assert.Empty(t, ActiveProfileName(), "clearing a persisted selection over a legacy `default` must survive a restart, not silently re-adopt `default`")
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
			assert.Equal(t, tt.wantAdoptedAfter, viper.GetBool("profile-decided"))
			assert.Equal(t, tt.wantSelectedAfter, ActiveProfileName())

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
			assert.Equal(t, tt.wantAdoptedAfter, viper.GetBool("profile-decided"))

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

// `auth setup` used to default its `--profile` flag to the literal string
// "default", reinventing the implicit profile this branch removed.
func TestAuthSetupWithoutProfileFlagTargetsTheActiveProfile(t *testing.T) {
	resetAuthState(t)
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("", stubAuthHandler{})

	withFakeInteractiveSetup(t, func(key string, required bool) (string, error) {
		return "rotated-secret", nil
	})
	viper.Set("profile", "staging")

	_, _, err := executeArgsStreams([]string{"auth", "setup"})
	assert.NoError(t, err)

	viper.Set("profile", "staging")
	assert.Equal(t, "rotated-secret", GetProfile()["api_key"], "expected `auth setup` to rotate the active profile")
	assert.False(t, ProfileExists("default"), "a --profile default would invent a profile nobody asked for")
}

// `auth setup` registers a local `--profile` that shadows the persistent root
// one; pflag silently picks a winner, and only a run through the real command
// tree shows which.
func TestAuthSetupCommandTargetsProfileFlagThroughCobra(t *testing.T) {
	resetAuthState(t)
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("", stubAuthHandler{})

	withFakeInteractiveSetup(t, func(key string, required bool) (string, error) {
		return "secret", nil
	})

	_, stderr, err := executeArgsStreams([]string{"auth", "setup", "--profile", "named"})

	assert.NoError(t, err)
	assert.Empty(t, stderr)
	assert.True(t, ProfileExists("named"), "expected `--profile named` to target the `named` profile")
}

// withFakeInteractiveSetup swaps isInteractive and promptProfileValue for the
// duration of the test so RunAuthSetup can be exercised without a TTY.
func withFakeInteractiveSetup(t *testing.T, prompt func(key string, required bool) (string, error)) {
	t.Helper()
	origInteractive, origPrompt := isInteractive, promptProfileValue
	t.Cleanup(func() { isInteractive, promptProfileValue = origInteractive, origPrompt })
	isInteractive = func() bool { return true }
	promptProfileValue = prompt
}

// A user who ran `auth profile use staging` and then `auth setup` to rotate
// the key must have `staging` updated, not a freshly-invented `default`.
func TestRunAuthSetupTargetsActiveProfileNotDefault(t *testing.T) {
	resetAuthState(t)
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("", stubAuthHandler{})

	withFakeInteractiveSetup(t, func(key string, required bool) (string, error) {
		return "rotated-secret", nil
	})

	viper.Set("profile", "staging")

	if err := RunAuthSetup("", "", ""); err != nil {
		t.Fatalf("RunAuthSetup: %v", err)
	}

	viper.Set("profile", "staging")
	if got := GetProfile()["api_key"]; got != "rotated-secret" {
		t.Fatalf("expected staging profile updated, got %q", got)
	}
	if ProfileExists("default") {
		t.Fatal("expected no `default` profile to be invented")
	}
}

// With no profile in force and no --profile given, RunAuthSetup must prompt
// for a name instead of falling back to "default".
func TestRunAuthSetupPromptsForNameWhenNoneActive(t *testing.T) {
	resetAuthState(t)
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("", stubAuthHandler{})

	var prompted []string
	withFakeInteractiveSetup(t, func(key string, required bool) (string, error) {
		prompted = append(prompted, key)
		if key == "profile_name" {
			return "acme", nil
		}
		return "secret", nil
	})

	if err := RunAuthSetup("", "", ""); err != nil {
		t.Fatalf("RunAuthSetup: %v", err)
	}

	if !ProfileExists("acme") {
		t.Fatal("expected the prompted profile name to be used")
	}
	if ProfileExists("default") {
		t.Fatal("expected no `default` profile to be invented")
	}
	if len(prompted) == 0 || prompted[0] != "profile_name" {
		t.Fatalf("expected the profile name prompt first, got %v", prompted)
	}
}

// An empty answer to the profile-name prompt must be rejected rather than
// silently producing a profile named "".
func TestRunAuthSetupRejectsEmptyProfileNamePrompt(t *testing.T) {
	resetAuthState(t)
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("", stubAuthHandler{})

	withFakeInteractiveSetup(t, func(key string, required bool) (string, error) {
		if key == "profile_name" {
			return "   ", nil
		}
		return "secret", nil
	})

	if err := RunAuthSetup("", "", ""); err == nil {
		t.Fatal("expected an error for an empty profile name")
	}
	if ProfileExists("") {
		t.Fatal("expected no profile to be created for an empty name")
	}
}

// `auth setup` only prompts, so survey.Required must apply to the required
// key alone: Enter on the optional `region` saves without it, Enter on
// `api-key` still fails.
func TestRunAuthSetupPromptSkipsOptionalKey(t *testing.T) {
	resetAuthState(t)
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("optional", stubOptionalKeyHandler{})

	var sawRequired = map[string]bool{}
	withFakeInteractiveSetup(t, func(key string, required bool) (string, error) {
		sawRequired[key] = required
		switch key {
		case "profile_name":
			return "acme", nil
		case "api-key":
			return "secret", nil
		case "region":
			return "", nil
		default:
			t.Fatalf("unexpected prompt for %q", key)
			return "", nil
		}
	})

	if err := RunAuthSetup("", "optional", ""); err != nil {
		t.Fatalf("RunAuthSetup: %v", err)
	}

	if !sawRequired["api-key"] {
		t.Error("expected api-key to be threaded through as required")
	}
	if sawRequired["region"] {
		t.Error("expected region to be threaded through as optional")
	}

	profile := Creds.GetStringMapString("profiles.acme")
	assert.Equal(t, "secret", profile["api_key"])
	_, hasRegion := profile["region"]
	assert.False(t, hasRegion, "expected optional region left out of the profile")
}

// The credential field must still be rejected through the same prompt route
// that now lets the optional field through.
func TestRunAuthSetupPromptRejectsEmptyCredential(t *testing.T) {
	resetAuthState(t)
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("optional", stubOptionalKeyHandler{})

	withFakeInteractiveSetup(t, func(key string, required bool) (string, error) {
		switch key {
		case "profile_name":
			return "acme", nil
		case "api-key":
			return "", nil
		case "region":
			return "eu-west", nil
		default:
			t.Fatalf("unexpected prompt for %q", key)
			return "", nil
		}
	})

	err := RunAuthSetup("", "optional", "")

	assert.ErrorContains(t, err, "api-key cannot be empty")
	assert.False(t, ProfileExists("acme"))
}

// `auth profile add`'s prompt fallback (resolveProfileValue's third route,
// used when neither --<key>-file nor a positional argument supplies a key)
// must also let the optional field through and still reject the credential.
func TestAuthProfileAddPromptSkipsOptionalKey(t *testing.T) {
	resetAuthState(t)
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("optional", stubOptionalKeyHandler{})

	origPrompt, origInteractive := promptProfileValue, isInteractive
	t.Cleanup(func() { promptProfileValue, isInteractive = origPrompt, origInteractive })
	isInteractive = func() bool { return true }

	var sawRequired = map[string]bool{}
	promptProfileValue = func(key string, required bool) (string, error) {
		sawRequired[key] = required
		switch key {
		case "api-key":
			return "secret", nil
		case "region":
			return "", nil
		default:
			t.Fatalf("unexpected prompt for %q", key)
			return "", nil
		}
	}

	// Only the profile name is given positionally; both declared keys fall
	// through to the prompt route.
	_, stderr := executeStreams("auth profile add optional acme")

	assert.Empty(t, stderr)
	if !sawRequired["api-key"] {
		t.Error("expected api-key to be threaded through as required")
	}
	// Asserting the prompt was reached, not just that it reported the key as
	// optional: skipping an optional prompt outright would leave the profile
	// looking identical while silently removing the operator's chance to fill
	// the field in.
	promptedRegion, ok := sawRequired["region"]
	if !ok {
		t.Error("expected region to be prompted for")
	}
	if promptedRegion {
		t.Error("expected region to be threaded through as optional")
	}

	profile := Creds.GetStringMapString("profiles.acme")
	assert.Equal(t, "secret", profile["api_key"])
	_, hasRegion := profile["region"]
	assert.False(t, hasRegion, "expected optional region left out of the profile")
}

// The same prompt fallback still rejects an empty credential.
func TestAuthProfileAddPromptRejectsEmptyCredential(t *testing.T) {
	resetAuthState(t)
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("optional", stubOptionalKeyHandler{})

	origPrompt, origInteractive := promptProfileValue, isInteractive
	t.Cleanup(func() { promptProfileValue, isInteractive = origPrompt, origInteractive })
	isInteractive = func() bool { return true }

	promptProfileValue = func(key string, required bool) (string, error) {
		switch key {
		case "api-key":
			return "", nil
		case "region":
			return "eu-west", nil
		default:
			t.Fatalf("unexpected prompt for %q", key)
			return "", nil
		}
	}

	_, stderr := executeStreams("auth profile add optional acme")

	assert.Contains(t, stderr, "api-key cannot be empty")
	assert.False(t, ProfileExists("acme"))
}

// `auth profile current` with nothing in force reports the "none" rung.
func TestAuthProfileCurrentNoneInForce(t *testing.T) {
	resetAuthState(t)
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("", stubAuthHandler{})

	out := execute("auth profile current --json")

	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("unmarshal: %v, out=%s", err, out)
	}
	assert.Equal(t, "", decoded["active_profile"])
	assert.Equal(t, "none", decoded["source"])
	assert.Equal(t, false, decoded["exists"])
}

// A profile named via --profile reports the "flag" rung, and one that does
// not exist reports exists:false rather than erroring.
func TestAuthProfileCurrentFromFlagReportsMissing(t *testing.T) {
	resetAuthState(t)
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("", stubAuthHandler{})

	out := execute("--profile ghost auth profile current --json")

	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("unmarshal: %v, out=%s", err, out)
	}
	assert.Equal(t, "ghost", decoded["active_profile"])
	assert.Equal(t, "flag", decoded["source"])
	assert.Equal(t, false, decoded["exists"])
}

// A profile chosen with `auth profile use` reports the "selected" rung.
func TestAuthProfileCurrentFromPersistedSelection(t *testing.T) {
	resetAuthState(t)
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("", stubAuthHandler{})

	if err := saveAuthProfile("", "acme", []string{"api-key"}, []string{"secret"}, ""); err != nil {
		t.Fatalf("saveAuthProfile: %v", err)
	}
	execute("auth profile use acme")

	out := execute("auth profile current --json")

	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("unmarshal: %v, out=%s", err, out)
	}
	assert.Equal(t, "acme", decoded["active_profile"])
	assert.Equal(t, "selected", decoded["source"])
	assert.Equal(t, true, decoded["exists"])
}

// When credentials.json is written but the profile-selected marker can't be
// persisted afterward, the error must say the key was saved and name the
// recovery command, rather than surfacing the raw underlying I/O error alone.
func TestSaveAuthProfilePartialSaveErrorNamesRecovery(t *testing.T) {
	home := resetAuthState(t)
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("", stubAuthHandler{})

	dir := filepath.Join(home, ".test-auth")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	const malformed = `{"server-default": "https://acme.example.com",`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(malformed), 0o600); err != nil {
		t.Fatal(err)
	}

	err := saveAuthProfile("", "acme", []string{"api-key"}, []string{"secret"}, "")

	assert.ErrorContains(t, err, `"acme" was saved`)
	assert.ErrorContains(t, err, "auth profile use acme")
	assert.ErrorContains(t, err, "--profile acme")

	// The profile itself must have made it to disk despite the second
	// write failing.
	resetAuthSingletons()
	if err := os.Setenv("HOME", home); err != nil {
		t.Fatal(err)
	}
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("", stubAuthHandler{})
	assert.True(t, ProfileExists("acme"))
}

// With a profile in force, CredentialScope must be the profile name itself,
// so existing `profiles.<name>.<field>` cache keys keep working.
func TestCredentialScopeIsProfileNameWhenSelected(t *testing.T) {
	resetAuthState(t)
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("", stubAuthHandler{})

	if err := saveAuthProfile("", "acme", []string{"api-key"}, []string{"secret"}, ""); err != nil {
		t.Fatalf("saveAuthProfile: %v", err)
	}
	SelectProfile("acme")

	assert.Equal(t, "acme", CredentialScope())
}

// With no profile in force, two environments differing only by the resolved
// server must land in different scopes, or a cached OAuth token for one
// deployment would be replayed against another that happens to share the
// unnamed bucket.
func TestCredentialScopeDiffersByServerWhenNoProfileSelected(t *testing.T) {
	resetAuthState(t)
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("", stubAuthHandler{})

	t.Setenv("TEST_AUTH_SERVER", "https://one.example.com")
	scopeOne := CredentialScope()

	t.Setenv("TEST_AUTH_SERVER", "https://two.example.com")
	scopeTwo := CredentialScope()

	assert.NotEmpty(t, scopeOne)
	assert.NotEmpty(t, scopeTwo)
	assert.NotEqual(t, scopeOne, scopeTwo)
	assert.True(t, strings.HasPrefix(scopeOne, "env-"))
	assert.True(t, strings.HasPrefix(scopeTwo, "env-"))
}

// Even when ResolveServer() itself is empty, CredentialScope must still
// return a stable, non-empty scope rather than collapsing to "".
func TestCredentialScopeIsStableWhenServerIsEmpty(t *testing.T) {
	resetAuthState(t)
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("", stubAuthHandler{})

	first := CredentialScope()
	second := CredentialScope()

	assert.NotEmpty(t, first)
	assert.Equal(t, first, second)
	assert.True(t, strings.HasPrefix(first, "env-"))
}

// Every other prompt test replaces promptProfileValue wholesale, so nothing
// reaches the validator it actually installs. This is the assertion that
// survives that stub: an optional key must run no validator at all, or
// survey.Required rejects the empty answer that leaves the field unset.
func TestProfilePromptValidatorRunsOnlyForRequiredKeys(t *testing.T) {
	assert.Nil(t, profilePromptValidator(false), "an optional key must accept an empty answer")

	validator := profilePromptValidator(true)
	if assert.NotNil(t, validator, "a required key must be validated") {
		assert.Error(t, validator(""), "a required key must reject an empty answer")
		assert.NoError(t, validator("secret"))
	}
}
