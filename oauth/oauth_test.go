package oauth

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/orq-ai/bartolo/cli"
	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"golang.org/x/oauth2"
)

// stubTokenSource always returns the same token, mimicking a source that has
// nothing new to fetch.
type stubTokenSource struct {
	token *oauth2.Token
}

func (s stubTokenSource) Token() (*oauth2.Token, error) {
	return s.token, nil
}

// resetOAuthState clears viper's global state and points HOME at a fresh
// temp dir, following the pattern `resetAuthState` uses in the `cli` package
// tests, then brings up a minimal CLI so cli.Cache and cli.CredentialScope
// are usable.
func resetOAuthState(t *testing.T) {
	t.Helper()

	viper.Reset()
	t.Setenv("HOME", t.TempDir())
	cli.Init(&cli.Config{AppName: "test-oauth", EnvPrefix: "TEST_OAUTH"})
}

// TokenHandler must key the cache off cli.CredentialScope(), not off
// cli.ActiveProfileName() directly, so an unnamed credential set (no profile
// in force) still gets its own cache bucket per resolved server rather than
// sharing "profiles.." with every other unnamed invocation.
func TestTokenHandlerCachesUnderCredentialScope(t *testing.T) {
	resetOAuthState(t)

	logger := zerolog.Nop()
	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	assert.NoError(t, err)

	token := &oauth2.Token{
		AccessToken: "abc123",
		TokenType:   "Bearer",
		Expiry:      time.Now().Add(time.Hour),
	}

	assert.NoError(t, TokenHandler(stubTokenSource{token: token}, cli.CredentialScope(), &logger, req))

	scope := cli.CredentialScope()
	assert.Equal(t, "abc123", cli.Cache.GetString("profiles."+scope+".token"))
	assert.Equal(t, "Bearer abc123", req.Header.Get("Authorization"))
}

// Two unnamed credential sets (no profile in force) that resolve to
// different servers must land in different cache buckets, or TokenHandler
// would hand one deployment's cached token to a request meant for another.
func TestTokenHandlerScopesDifferByResolvedServerWhenNoProfileSelected(t *testing.T) {
	resetOAuthState(t)

	logger := zerolog.Nop()
	tokenOne := &oauth2.Token{AccessToken: "token-one", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}

	t.Setenv("TEST_OAUTH_SERVER", "https://one.example.com")
	reqOne, err := http.NewRequest(http.MethodGet, "https://one.example.com", nil)
	assert.NoError(t, err)
	assert.NoError(t, TokenHandler(stubTokenSource{token: tokenOne}, cli.CredentialScope(), &logger, reqOne))
	scopeOne := cli.CredentialScope()

	t.Setenv("TEST_OAUTH_SERVER", "https://two.example.com")
	tokenTwo := &oauth2.Token{AccessToken: "token-two", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}
	reqTwo, err := http.NewRequest(http.MethodGet, "https://two.example.com", nil)
	assert.NoError(t, err)
	assert.NoError(t, TokenHandler(stubTokenSource{token: tokenTwo}, cli.CredentialScope(), &logger, reqTwo))
	scopeTwo := cli.CredentialScope()

	assert.NotEqual(t, scopeOne, scopeTwo)
	assert.Equal(t, "token-one", cli.Cache.GetString("profiles."+scopeOne+".token"))
	assert.Equal(t, "token-two", cli.Cache.GetString("profiles."+scopeTwo+".token"))

	// The second deployment's request must carry its own token, not the
	// first deployment's cached one.
	assert.Equal(t, "Bearer token-two", reqTwo.Header.Get("Authorization"))
}

// Two OAuth configurations can share a server and still be different
// credentials: the client id, token endpoint, scopes and endpoint params each
// identify one, so each must move the cache bucket on its own.
func TestCredentialScopeDiffersByCredentialIdentity(t *testing.T) {
	resetOAuthState(t)
	t.Setenv("TEST_OAUTH_SERVER", "https://one.example.com")

	base := credentialScope("clientcredentials", "https://auth.example.com/token", "client", []string{"read"}, url.Values{})

	assert.NotEqual(t, base, credentialScope("clientcredentials", "https://auth.example.com/token", "other-client", []string{"read"}, url.Values{}))
	assert.NotEqual(t, base, credentialScope("clientcredentials", "https://other.example.com/token", "client", []string{"read"}, url.Values{}))
	assert.NotEqual(t, base, credentialScope("clientcredentials", "https://auth.example.com/token", "client", []string{"read", "write"}, url.Values{}))
	assert.NotEqual(t, base, credentialScope("clientcredentials", "https://auth.example.com/token", "client", []string{"read"}, url.Values{"audience": {"internal"}}))
	assert.NotEqual(t, base, credentialScope("authcode", "https://auth.example.com/token", "client", []string{"read"}, url.Values{}))
}

// A cached token must not reach a request built from a different client id,
// even though both resolve to the same server.
func TestTokenHandlerDoesNotShareTokensAcrossClientIDs(t *testing.T) {
	resetOAuthState(t)
	t.Setenv("TEST_OAUTH_SERVER", "https://one.example.com")

	logger := zerolog.Nop()
	scopeOne := credentialScope("clientcredentials", "https://auth.example.com/token", "client-one", nil, url.Values{})
	scopeTwo := credentialScope("clientcredentials", "https://auth.example.com/token", "client-two", nil, url.Values{})

	reqOne, err := http.NewRequest(http.MethodGet, "https://one.example.com", nil)
	assert.NoError(t, err)
	tokenOne := &oauth2.Token{AccessToken: "token-one", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}
	assert.NoError(t, TokenHandler(stubTokenSource{token: tokenOne}, scopeOne, &logger, reqOne))

	reqTwo, err := http.NewRequest(http.MethodGet, "https://one.example.com", nil)
	assert.NoError(t, err)
	tokenTwo := &oauth2.Token{AccessToken: "token-two", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}
	assert.NoError(t, TokenHandler(stubTokenSource{token: tokenTwo}, scopeTwo, &logger, reqTwo))

	assert.Equal(t, "token-one", cli.Cache.GetString("profiles."+scopeOne+".token"))
	assert.Equal(t, "token-two", cli.Cache.GetString("profiles."+scopeTwo+".token"))
	assert.Equal(t, "Bearer token-two", reqTwo.Header.Get("Authorization"))
}
