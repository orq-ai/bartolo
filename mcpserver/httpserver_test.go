package mcpserver

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// echoTools is a minimal tool set reused across HTTP transport tests.
func echoTools() []ToolDef {
	return []ToolDef{{
		Name:        "echo",
		Title:       "Echo",
		Description: "Echoes the name back",
		Group:       "misc",
		InputSchema: json.RawMessage(echoSchema),
		ReadOnly:    true,
		Idempotent:  true,
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			return map[string]any{"echoed": args["name"]}, nil
		},
	}}
}

// newHTTPTestServer builds the transport handler and wraps it in an httptest
// server. It fails the test on a build error.
func newHTTPTestServer(t *testing.T, opts httpServeOptions) *httptest.Server {
	t.Helper()
	server, err := buildServer("testapp", "1.0.0", "", echoTools())
	if err != nil {
		t.Fatalf("buildServer: %v", err)
	}
	handler, err := buildHTTPHandler(context.Background(), server, opts)
	if err != nil {
		t.Fatalf("buildHTTPHandler: %v", err)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

// headerRoundTripper injects a fixed header on every request.
type headerRoundTripper struct {
	base  http.RoundTripper
	key   string
	value string
}

func (h headerRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set(h.key, h.value)
	return h.base.RoundTrip(r)
}

func bearerHTTPClient(token string) *http.Client {
	return &http.Client{Transport: headerRoundTripper{
		base:  http.DefaultTransport,
		key:   "Authorization",
		value: "Bearer " + token,
	}}
}

// connectMCP establishes an MCP client session against a Streamable HTTP
// endpoint, optionally with a custom HTTP client (for auth headers).
func connectMCP(t *testing.T, endpoint string, client *http.Client) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	c := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	tr := &mcp.StreamableClientTransport{Endpoint: endpoint}
	if client != nil {
		tr.HTTPClient = client
	}
	cs, err := c.Connect(ctx, tr, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// assertEchoRoundTrip drives tools/list + tools/call over the session.
func assertEchoRoundTrip(t *testing.T, cs *mcp.ClientSession) {
	t.Helper()
	ctx := context.Background()

	lt, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(lt.Tools) != 1 || lt.Tools[0].Name != "echo" {
		t.Fatalf("unexpected tools: %+v", lt.Tools)
	}

	ct, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "echo",
		Arguments: map[string]any{"name": "world"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if ct.IsError {
		t.Fatalf("unexpected IsError")
	}
	sc, ok := ct.StructuredContent.(map[string]any)
	if !ok || sc["echoed"] != "world" {
		t.Fatalf("structured content = %v", ct.StructuredContent)
	}
}

func TestHTTPRoundTripAuthNone(t *testing.T) {
	srv := newHTTPTestServer(t, httpServeOptions{addr: "127.0.0.1:0", authMode: authModeNone})
	cs := connectMCP(t, srv.URL, nil)
	assertEchoRoundTrip(t, cs)
}

func TestHTTPBearerAuth(t *testing.T) {
	const token = "s3cr3t-token"
	srv := newHTTPTestServer(t, httpServeOptions{addr: "127.0.0.1:0", authMode: authModeBearer, token: token})

	t.Run("correct token round trips", func(t *testing.T) {
		cs := connectMCP(t, srv.URL, bearerHTTPClient(token))
		assertEchoRoundTrip(t, cs)
	})

	t.Run("missing token 401", func(t *testing.T) {
		resp, err := http.Post(srv.URL, "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", resp.StatusCode)
		}
		if wa := resp.Header.Get("WWW-Authenticate"); !strings.Contains(strings.ToLower(wa), "bearer") {
			t.Errorf("WWW-Authenticate = %q, want Bearer challenge", wa)
		}
	})

	t.Run("wrong token 401", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer nope")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", resp.StatusCode)
		}
	})
}

func TestHTTPOriginCheck(t *testing.T) {
	srv := newHTTPTestServer(t, httpServeOptions{
		addr:           "127.0.0.1:0",
		authMode:       authModeNone,
		allowedOrigins: []string{"https://good.example"},
	})

	post := func(origin string) int {
		req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{}`))
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if code := post("https://evil.example"); code != http.StatusForbidden {
		t.Errorf("disallowed origin status = %d, want 403", code)
	}
	if code := post("https://good.example"); code == http.StatusForbidden {
		t.Errorf("allowed origin was forbidden (%d)", code)
	}
	if code := post(""); code == http.StatusForbidden {
		t.Errorf("no-Origin request was forbidden (%d)", code)
	}
}

// --- OAuth resource-server tests ---

// oauthFixture holds an RSA signing key + JWKS endpoint for minting test JWTs.
type oauthFixture struct {
	key      *rsa.PrivateKey
	kid      string
	jwksSrv  *httptest.Server
	issuer   string
	audience string
}

func newOAuthFixture(t *testing.T) *oauthFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const kid = "test-key-1"
	pub := key.Public().(*rsa.PublicKey)
	n := base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes())
	jwks := fmt.Sprintf(`{"keys":[{"kty":"RSA","kid":%q,"use":"sig","alg":"RS256","n":%q,"e":%q}]}`, kid, n, e)

	jwksSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(jwks))
	}))
	t.Cleanup(jwksSrv.Close)

	return &oauthFixture{
		key:      key,
		kid:      kid,
		jwksSrv:  jwksSrv,
		issuer:   "https://issuer.example",
		audience: "https://api.example/mcp",
	}
}

func (f *oauthFixture) mint(t *testing.T, audience string, exp time.Time, scope string) string {
	t.Helper()
	claims := jwt.MapClaims{
		"iss": f.issuer,
		"aud": audience,
		"iat": time.Now().Add(-time.Minute).Unix(),
		"exp": exp.Unix(),
	}
	if scope != "" {
		claims["scope"] = scope
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = f.kid
	s, err := tok.SignedString(f.key)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return s
}

func (f *oauthFixture) opts() httpServeOptions {
	return httpServeOptions{
		addr:     "127.0.0.1:0",
		authMode: authModeOAuth,
		oauth: oauthConfig{
			issuer:   f.issuer,
			audience: f.audience,
			jwksURL:  f.jwksSrv.URL,
			scopes:   []string{"mcp:use"},
		},
	}
}

func postWithToken(t *testing.T, url, token string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(`{}`))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestHTTPOAuthValidTokenRoundTrips(t *testing.T) {
	f := newOAuthFixture(t)
	srv := newHTTPTestServer(t, f.opts())

	token := f.mint(t, f.audience, time.Now().Add(time.Hour), "mcp:use")
	cs := connectMCP(t, srv.URL, bearerHTTPClient(token))
	assertEchoRoundTrip(t, cs)
}

func TestHTTPOAuthRejections(t *testing.T) {
	f := newOAuthFixture(t)
	srv := newHTTPTestServer(t, f.opts())

	t.Run("expired 401", func(t *testing.T) {
		token := f.mint(t, f.audience, time.Now().Add(-time.Hour), "mcp:use")
		resp := postWithToken(t, srv.URL, token)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", resp.StatusCode)
		}
	})

	t.Run("wrong audience 401", func(t *testing.T) {
		token := f.mint(t, "https://other.example", time.Now().Add(time.Hour), "mcp:use")
		resp := postWithToken(t, srv.URL, token)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", resp.StatusCode)
		}
	})

	t.Run("missing scope 403", func(t *testing.T) {
		token := f.mint(t, f.audience, time.Now().Add(time.Hour), "other:scope")
		resp := postWithToken(t, srv.URL, token)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("status = %d, want 403", resp.StatusCode)
		}
	})

	t.Run("missing token 401 with resource_metadata", func(t *testing.T) {
		resp := postWithToken(t, srv.URL, "")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", resp.StatusCode)
		}
		wa := resp.Header.Get("WWW-Authenticate")
		if !strings.Contains(wa, "resource_metadata") {
			t.Errorf("WWW-Authenticate = %q, want resource_metadata", wa)
		}
	})
}

func TestHTTPOAuthMetadataEndpoint(t *testing.T) {
	f := newOAuthFixture(t)
	srv := newHTTPTestServer(t, f.opts())

	resp, err := http.Get(srv.URL + resourceMetadataPath)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("content-type = %q, want json", ct)
	}
	var meta struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
		BearerMethods        []string `json:"bearer_methods_supported"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if meta.Resource != f.audience {
		t.Errorf("resource = %q, want %q", meta.Resource, f.audience)
	}
	if len(meta.AuthorizationServers) != 1 || meta.AuthorizationServers[0] != f.issuer {
		t.Errorf("authorization_servers = %v, want [%q]", meta.AuthorizationServers, f.issuer)
	}
}

func TestResolveHTTPOptions(t *testing.T) {
	t.Run("bearer requires env token", func(t *testing.T) {
		t.Setenv("env-does-not-matter", "")
		_, err := resolveHTTPOptions("TESTPREFIX", "127.0.0.1:8484", authModeBearer, nil, false, "", "", "", nil)
		if err == nil {
			t.Fatal("expected error when token env var unset")
		}
	})

	t.Run("bearer with env token ok", func(t *testing.T) {
		t.Setenv("TESTPREFIX_MCP_TOKEN", "abc")
		opts, err := resolveHTTPOptions("TESTPREFIX", "127.0.0.1:8484", authModeBearer, nil, false, "", "", "", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if opts.token != "abc" {
			t.Errorf("token = %q, want abc", opts.token)
		}
	})

	t.Run("oauth requires issuer and audience", func(t *testing.T) {
		if _, err := resolveHTTPOptions("", ":8484", authModeOAuth, nil, false, "", "aud", "", nil); err == nil {
			t.Error("expected error when issuer missing")
		}
		if _, err := resolveHTTPOptions("", ":8484", authModeOAuth, nil, false, "iss", "", "", nil); err == nil {
			t.Error("expected error when audience missing")
		}
		opts, err := resolveHTTPOptions("", ":8484", authModeOAuth, nil, false, "iss", "aud", "jwks", []string{"s"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if opts.oauth.issuer != "iss" || opts.oauth.audience != "aud" || opts.oauth.jwksURL != "jwks" {
			t.Errorf("oauth config = %+v", opts.oauth)
		}
	})

	t.Run("invalid auth mode", func(t *testing.T) {
		if _, err := resolveHTTPOptions("", ":8484", "bogus", nil, false, "", "", "", nil); err == nil {
			t.Error("expected error for invalid auth mode")
		}
	})
}
