package apikey

import (
	"os"
	"testing"

	"github.com/orq-ai/bartolo/cli"
	"github.com/orq-ai/bartolo/oauth"
	"github.com/spf13/viper"
)

func resetCLI(t *testing.T) {
	t.Helper()

	viper.Reset()
	cli.Cache = nil
	cli.Client = nil
	cli.Root = nil
	cli.Creds = nil
	cli.AuthHandlers = make(map[string]cli.AuthHandler)
	// Restore a clean handler map so other tests/examples that rely on the
	// single-handler fallback are not affected by our typed registrations.
	t.Cleanup(func() { cli.AuthHandlers = make(map[string]cli.AuthHandler) })

	home := t.TempDir()
	oldHome := os.Getenv("HOME")
	if err := os.Setenv("HOME", home); err != nil {
		t.Fatalf("set HOME: %v", err)
	}
	t.Cleanup(func() { os.Setenv("HOME", oldHome) })

	cli.Init(&cli.Config{AppName: "typed-auth", EnvPrefix: "TYPED_AUTH"})
}

func TestInitWithTypeRegistersTypedHandlers(t *testing.T) {
	resetCLI(t)

	InitWithType("apiKeyHeader", "X-API-Key", LocationHeader)
	InitBearerWithType("bearer", "Authorization")

	if _, ok := cli.AuthHandlers["apiKeyHeader"]; !ok {
		t.Fatalf("expected apiKeyHeader handler registered; have %v", handlerKeys(cli.AuthHandlers))
	}
	if _, ok := cli.AuthHandlers["bearer"]; !ok {
		t.Fatalf("expected bearer handler registered; have %v", handlerKeys(cli.AuthHandlers))
	}
	if _, ok := cli.AuthHandlers[""]; ok {
		t.Fatalf("did not expect an empty-type handler with typed inits")
	}
}

func TestOAuthInitWithTypeRegistersTypedHandlers(t *testing.T) {
	resetCLI(t)

	oauth.InitClientCredentialsWithType("cc", "https://example.com/token", oauth.Scopes("read", "write"))
	oauth.InitAuthCodeWithType("ac", "", "https://example.com/authorize", "https://example.com/token")

	if _, ok := cli.AuthHandlers["cc"]; !ok {
		t.Fatalf("expected cc handler registered; have %v", handlerKeys(cli.AuthHandlers))
	}
	if _, ok := cli.AuthHandlers["ac"]; !ok {
		t.Fatalf("expected ac handler registered; have %v", handlerKeys(cli.AuthHandlers))
	}

	// The auth-code handler must expose a client-id profile key so `auth setup`
	// can prompt for it when the constructor client ID is empty.
	found := false
	for _, key := range cli.AuthHandlers["ac"].ProfileKeys() {
		if key == "client-id" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected auth-code handler to expose a client-id profile key, got %v", cli.AuthHandlers["ac"].ProfileKeys())
	}
}

func handlerKeys(m map[string]cli.AuthHandler) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
