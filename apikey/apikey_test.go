package apikey

import (
	"bytes"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orq-ai/bartolo/cli"
	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
)

// resetCLI isolates a test from the profile, config and credentials another
// test in this package left behind in viper's global state.
func resetCLI(t *testing.T) {
	t.Helper()

	viper.Reset()
	t.Setenv("HOME", t.TempDir())
}

func ExampleInit_header() {
	// Use a custom header for authentication.
	Init("X-API-Key", LocationHeader)
}

func ExampleInit_query() {
	// Use a query parameter for authentication.
	Init("apikey", LocationHeader)
}

func TestHeaderAuth(t *testing.T) {
	resetCLI(t)
	cli.Init(&cli.Config{
		AppName:   "test",
		EnvPrefix: "TEST",
	})
	Init("x-auth", LocationHeader)
	cli.Creds.Set("profiles.default.api_key", "test")
	cli.SelectProfile("default")

	r := cli.Client.Get()
	r.Do()

	assert.Equal(t, "test", r.Context.Request.Header.Get("x-auth"))
}

func TestQueryAuth(t *testing.T) {
	resetCLI(t)
	cli.Init(&cli.Config{
		AppName:   "test",
		EnvPrefix: "TEST",
	})
	Init("key", LocationQuery)
	cli.Creds.Set("profiles.default.api_key", "test")
	cli.SelectProfile("default")

	r := cli.Client.Get()
	r.Do()

	assert.Equal(t, "test", r.Context.Request.URL.Query().Get("key"))
}

func TestCookieAuth(t *testing.T) {
	resetCLI(t)
	cli.Init(&cli.Config{
		AppName:   "test",
		EnvPrefix: "TEST",
	})
	Init("key", LocationCookie)
	cli.Creds.Set("profiles.default.api_key", "test")
	cli.SelectProfile("default")

	r := cli.Client.Get()
	r.Do()

	cookie, err := r.Context.Request.Cookie("key")
	assert.NoError(t, err)
	assert.Equal(t, "test", cookie.Value)
}

func TestBearerAuthFromEnv(t *testing.T) {
	resetCLI(t)
	t.Setenv("TEST_TOKEN", "secret-token")

	cli.Init(&cli.Config{
		AppName:   "test",
		EnvPrefix: "TEST",
	})
	InitBearer("Authorization")

	r := cli.Client.Get()
	r.Do()

	assert.Equal(t, "Bearer secret-token", r.Context.Request.Header.Get("Authorization"))
}

func TestCustomAPIKeyEnvVarTakesPrecedence(t *testing.T) {
	resetCLI(t)
	t.Setenv("CUSTOM_ORQ_KEY", "custom-secret")

	cli.Init(&cli.Config{
		AppName:      "test",
		EnvPrefix:    "TEST",
		APIKeyEnvVar: "CUSTOM_ORQ_KEY",
	})
	Init("x-auth", LocationHeader)

	r := cli.Client.Get()
	r.Do()

	assert.Equal(t, "custom-secret", r.Context.Request.Header.Get("x-auth"))
}

// runCLI executes a command against the real cli.Root command tree, the same
// way a user would invoke it, so a fixture that needs a persisted profile
// selection goes through `auth profile use` rather than poking viper.
func runCLI(t *testing.T, cmd string) (stdout, stderr string) {
	t.Helper()

	outBuf := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}
	cli.Root.SetArgs(strings.Split(cmd, " "))
	cli.Root.SetOut(outBuf)
	cli.Root.SetErr(errBuf)
	cli.Stdout = outBuf
	cli.Stderr = errBuf
	_ = cli.Root.Execute()
	return outBuf.String(), errBuf.String()
}

// putProfileInForce puts "acme" in force each of the three real ways: the
// flag, PREFIX_PROFILE, or a persisted selection. A missing profile is
// written straight to the config file, since `auth profile use` refuses one.
func putProfileInForce(t *testing.T, via string, name string, missing bool) {
	t.Helper()

	switch via {
	case "flag":
		assert.NoError(t, cli.Root.PersistentFlags().Set("profile", name))
	case "env":
		t.Setenv("TEST_PROFILE", name)
	case "persisted":
		if missing {
			dir := viper.GetString("config-directory")
			body := fmt.Sprintf(`{"profile-selected": %q}`, name)
			assert.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600))
			assert.NoError(t, viper.ReadInConfig())
			return
		}

		stdout, stderr := runCLI(t, "auth profile use "+name+" -o json")
		if !strings.Contains(stdout, `"active_profile"`) {
			t.Fatalf("auth profile use %s did not persist a selection: stdout=%q stderr=%q", name, stdout, stderr)
		}
	default:
		t.Fatalf("unknown entry mode %q", via)
	}
}

// A profile in force is authoritative for the outgoing key in every
// combination of how it entered force, its own state, and whether an ambient
// PREFIX_API_KEY is set. It must never fall back to that ambient key.
func TestProfileAuthorityAcrossEntryModesAndSources(t *testing.T) {
	entryModes := []string{"flag", "env", "persisted"}

	states := []struct {
		name  string
		setup func(name string)
	}{
		{"has key", func(name string) { cli.Creds.Set("profiles."+name+".api_key", "profile-key") }},
		{"no key", func(name string) { cli.Creds.Set("profiles."+name+".type", "") }},
		{"does not exist", func(name string) {}},
	}

	for _, via := range entryModes {
		for _, st := range states {
			for _, envKeySet := range []bool{true, false} {
				name := fmt.Sprintf("via=%s/state=%s/env=%v", via, st.name, envKeySet)
				t.Run(name, func(t *testing.T) {
					resetCLI(t)
					cli.Init(&cli.Config{
						AppName:   "test",
						EnvPrefix: "TEST",
					})
					Init("x-auth", LocationHeader)

					st.setup("acme")
					if envKeySet {
						t.Setenv("TEST_API_KEY", "from-env")
					}
					putProfileInForce(t, via, "acme", st.name == "does not exist")

					r := cli.Client.Get()
					r.Do()

					switch st.name {
					case "has key":
						assert.Equal(t, "profile-key", r.Context.Request.Header.Get("x-auth"),
							"a profile in force is authoritative even when an ambient key is present")
					case "no key":
						assert.ErrorContains(t, r.Context.Error, `profile "acme" has no API key`)
						assert.Empty(t, r.Context.Request.Header.Get("x-auth"),
							"an incomplete profile in force must not fall back to the ambient key")
					case "does not exist":
						assert.ErrorContains(t, r.Context.Error, `profile "acme" is not configured`)
						assert.Empty(t, r.Context.Request.Header.Get("x-auth"),
							"an unknown profile in force must not fall back to the ambient key")
					}
				})
			}
		}
	}
}

// A generated CLI with a blank EnvPrefix has no env vars to suggest, which is
// exactly the case with no profile name to anchor the message on either. The
// message must still read as a complete sentence and point at `auth setup`.
func TestMissingKeyErrorWithNoEnvVarsIsACompleteSentence(t *testing.T) {
	h := &Handler{Name: "x-auth", In: LocationHeader}

	err := h.missingKeyError("", "missing")

	assert.ErrorContains(t, err, "auth setup")
	assert.NotContains(t, err.Error(), "set one of")
	assert.False(t, strings.HasSuffix(err.Error(), ": "))
	assert.False(t, strings.HasSuffix(err.Error(), "or "))
}

// A key read out of a .env is otherwise indistinguishable from one the user
// exported on purpose, which is the confusion dotenv opt-in exists to remove.
// `doctor` has to name the file, and only when a file actually supplied it.
func TestAuthStatusNamesTheDotEnvFile(t *testing.T) {
	resetCLI(t)
	t.Setenv("TEST_API_KEY", "")
	os.Unsetenv("TEST_API_KEY")
	t.Setenv("TEST_DOTENV", "1")

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("TEST_API_KEY=from-dotenv\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	cli.Init(&cli.Config{AppName: "test", EnvPrefix: "TEST"})
	Init("x-auth", LocationHeader)

	handler := cli.AuthHandlers[""].(*Handler)
	status := handler.AuthStatus(nil)

	assert.Equal(t, true, status["configured"])
	assert.Equal(t, "dotenv", status["source"])
	assert.Equal(t, ".env", status["dotenv_file"])
}

// An exported key is not a dotenv key, and reporting it as one would be the
// same mislabelling in the other direction.
func TestAuthStatusOmitsTheDotEnvFileForAnExportedKey(t *testing.T) {
	resetCLI(t)
	t.Setenv("TEST_API_KEY", "from-shell")
	t.Chdir(t.TempDir())

	cli.Init(&cli.Config{AppName: "test", EnvPrefix: "TEST"})
	Init("x-auth", LocationHeader)

	status := cli.AuthHandlers[""].(*Handler).AuthStatus(nil)

	assert.Equal(t, "env", status["source"])
	assert.NotContains(t, status, "dotenv_file")
}

// The remedies in a missing-key error all read as already satisfied to someone
// looking at a .env that holds the key, so the error has to name the file and
// the switch that would read it.
func TestMissingKeyErrorNamesAnIgnoredDotEnvFile(t *testing.T) {
	resetCLI(t)
	t.Setenv("TEST_API_KEY", "")
	os.Unsetenv("TEST_API_KEY")
	t.Setenv("TEST_DOTENV", "")
	os.Unsetenv("TEST_DOTENV")

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("TEST_API_KEY=from-dotenv\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	cli.Init(&cli.Config{AppName: "test", EnvPrefix: "TEST"})
	Init("x-auth", LocationHeader)

	err := cli.AuthHandlers[""].(*Handler).OnRequest(&zerolog.Logger{}, httptest.NewRequest("GET", "/", nil))

	assert.ErrorContains(t, err, "missing API key")
	assert.ErrorContains(t, err, ".env defines TEST_API_KEY")
	assert.ErrorContains(t, err, "TEST_DOTENV=1")
}

// No .env, no hint: a remedy that fires everywhere trains people to ignore it.
func TestMissingKeyErrorWithoutADotEnvFile(t *testing.T) {
	resetCLI(t)
	t.Setenv("TEST_API_KEY", "")
	os.Unsetenv("TEST_API_KEY")
	t.Chdir(t.TempDir())

	cli.Init(&cli.Config{AppName: "test", EnvPrefix: "TEST"})
	Init("x-auth", LocationHeader)

	err := cli.AuthHandlers[""].(*Handler).OnRequest(&zerolog.Logger{}, httptest.NewRequest("GET", "/", nil))

	assert.ErrorContains(t, err, "missing API key")
	assert.NotContains(t, err.Error(), "dotenv loading is off")
}
