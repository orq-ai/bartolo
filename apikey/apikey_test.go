package apikey

import (
	"testing"

	"github.com/orq-ai/bartolo/cli"
	"github.com/stretchr/testify/assert"
)

func ExampleInit_header() {
	// Use a custom header for authentication.
	Init("X-API-Key", LocationHeader)
}

func ExampleInit_query() {
	// Use a query parameter for authentication.
	Init("apikey", LocationHeader)
}

func TestHeaderAuth(t *testing.T) {
	cli.Init(&cli.Config{
		AppName:   "test",
		EnvPrefix: "TEST",
	})
	Init("x-auth", LocationHeader)
	cli.Creds.Set("profiles.default.api_key", "test")

	r := cli.Client.Get()
	r.Do()

	assert.Equal(t, "test", r.Context.Request.Header.Get("x-auth"))
}

func TestQueryAuth(t *testing.T) {
	cli.Init(&cli.Config{
		AppName:   "test",
		EnvPrefix: "TEST",
	})
	Init("key", LocationQuery)
	cli.Creds.Set("profiles.default.api_key", "test")

	r := cli.Client.Get()
	r.Do()

	assert.Equal(t, "test", r.Context.Request.URL.Query().Get("key"))
}

func TestCookieAuth(t *testing.T) {
	cli.Init(&cli.Config{
		AppName:   "test",
		EnvPrefix: "TEST",
	})
	Init("key", LocationCookie)
	cli.Creds.Set("profiles.default.api_key", "test")

	r := cli.Client.Get()
	r.Do()

	cookie, err := r.Context.Request.Cookie("key")
	assert.NoError(t, err)
	assert.Equal(t, "test", cookie.Value)
}

func TestBearerAuthFromEnv(t *testing.T) {
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
	t.Setenv("HOME", t.TempDir())
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
	t.Setenv("HOME", t.TempDir())
	cli.Init(&cli.Config{
		AppName:   "test",
		EnvPrefix: "TEST",
	})
	Init("x-auth", LocationHeader)
	cli.Creds.Set("profiles.default.api_key", "from-profile")

	r := cli.Client.Get()
	r.Do()

	assert.Equal(t, "from-profile", r.Context.Request.Header.Get("x-auth"))
}

func TestSelectedProfileWithoutKeyIsAnError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
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
	t.Setenv("HOME", t.TempDir())
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
