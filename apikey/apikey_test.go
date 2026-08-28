package apikey

import (
	"testing"

	"github.com/orq-ai/bartolo/cli"
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

func TestSelectedProfileBeatsEnvKey(t *testing.T) {
	resetCLI(t)
	cli.Init(&cli.Config{
		AppName:   "test",
		EnvPrefix: "TEST",
	})
	Init("x-auth", LocationHeader)
	t.Setenv("TEST_API_KEY", "from-env")
	cli.Creds.Set("profiles.acme.api_key", "from-profile")

	r := cli.Client.Get()
	r.Do()
	assert.Equal(t, "from-env", r.Context.Request.Header.Get("x-auth"))

	assert.NoError(t, cli.Root.PersistentFlags().Set("profile", "acme"))
	r = cli.Client.Get()
	r.Do()
	assert.Equal(t, "from-profile", r.Context.Request.Header.Get("x-auth"))
}

func TestProfileKeyUsedWhenNoEnvVarIsSet(t *testing.T) {
	resetCLI(t)
	cli.Init(&cli.Config{
		AppName:   "test",
		EnvPrefix: "TEST",
	})
	Init("x-auth", LocationHeader)
	cli.Creds.Set("profiles.default.api_key", "from-profile")
	cli.SelectProfile("default")

	r := cli.Client.Get()
	r.Do()

	assert.Equal(t, "from-profile", r.Context.Request.Header.Get("x-auth"))
}

func TestSelectedProfileWithoutKeyIsAnError(t *testing.T) {
	resetCLI(t)
	cli.Init(&cli.Config{
		AppName:   "test",
		EnvPrefix: "TEST",
	})
	Init("x-auth", LocationHeader)
	t.Setenv("TEST_API_KEY", "from-env")
	cli.Creds.Set("profiles.acme.type", "")
	assert.NoError(t, cli.Root.PersistentFlags().Set("profile", "acme"))

	r := cli.Client.Get()
	r.Do()

	assert.ErrorContains(t, r.Context.Error, `profile "acme" has no API key`)
	assert.Empty(t, r.Context.Request.Header.Get("x-auth"))
}

func TestUnknownProfileIsAnError(t *testing.T) {
	resetCLI(t)
	cli.Init(&cli.Config{
		AppName:   "test",
		EnvPrefix: "TEST",
	})
	Init("x-auth", LocationHeader)
	t.Setenv("TEST_API_KEY", "from-env")
	assert.NoError(t, cli.Root.PersistentFlags().Set("profile", "typo"))

	r := cli.Client.Get()
	r.Do()

	assert.ErrorContains(t, r.Context.Error, `profile "typo" is not configured`)
}

// RequiredProfileKeys must narrow ProfileKeys down to just the credential,
// so a generator-supplied extra (a region, an organisation id, ...) does not
// block saving a profile that leaves it empty.
func TestRequiredProfileKeysIsJustTheAPIKey(t *testing.T) {
	h := &Handler{Name: "x-auth", In: LocationHeader, Keys: []string{"region", "org-id"}}

	assert.Equal(t, []string{apiKey, "region", "org-id"}, h.ProfileKeys())
	assert.Equal(t, []string{apiKey}, h.RequiredProfileKeys())
}
