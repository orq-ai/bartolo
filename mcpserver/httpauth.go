package mcpserver

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	keyfunc "github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// Inbound-auth design note (confused-deputy prevention): in the none, bearer
// and oauth modes the bearer/JWT the MCP *client* presents is used ONLY to
// authorize the incoming request. It is never stored in viper, a profile, or the
// token cache, and is never forwarded to the upstream API — outbound API auth is
// a separate credential plane handled by the cli middleware. The forward mode is
// the one deliberate exception: there the inbound token IS the caller's API
// credential, and every upstream call is made with it and nothing else.

// Inbound HTTP auth modes for the `mcp --http` transport.
const (
	authModeNone   = "none"
	authModeBearer = "bearer"
	authModeOAuth  = "oauth"
	// authModeForward requires each request to carry the caller's own API
	// credential and calls the upstream API with it. It is for a server that
	// fronts its own API, where the inbound token is already a credential for
	// that API (an API key), never for tokens issued to the MCP server itself.
	authModeForward = "forward"
)

// resourceMetadataPath is the RFC 9728 protected-resource-metadata endpoint.
const resourceMetadataPath = "/.well-known/oauth-protected-resource"

// oauthConfig holds the OAuth 2.1 resource-server settings for --http-auth oauth.
type oauthConfig struct {
	issuer   string   // authorization server issuer (required)
	audience string   // canonical resource URI = expected JWT "aud" (required)
	jwksURL  string   // explicit JWKS URL; empty → discover via AS metadata
	scopes   []string // required scopes (all must be present)
}

// isLoopbackAddr reports whether a bind address targets only the loopback
// interface. An address with an empty host (e.g. ":8484") binds all interfaces
// and is therefore NOT loopback. "localhost", "127.0.0.1", and "::1" are.
func isLoopbackAddr(addr string) bool {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	if host == "" {
		return false // binds all interfaces
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

// validateBindAddr enforces the safety rail: binding a non-loopback address with
// no inbound auth is refused unless the operator explicitly opts in with
// --http-insecure.
func validateBindAddr(addr, authMode string, insecure bool) error {
	if insecure || authMode != authModeNone {
		return nil
	}
	if !isLoopbackAddr(addr) {
		return fmt.Errorf(
			"refusing to bind non-loopback address %q with --http-auth none: "+
				"set --http-auth bearer|oauth, bind a loopback address (e.g. 127.0.0.1:8484), "+
				"or pass --http-insecure to override",
			addr)
	}
	return nil
}

// bearerAuthMiddleware enforces a static bearer token using a constant-time
// comparison. Missing/wrong tokens get 401 with a WWW-Authenticate: Bearer
// challenge. The presented token is discarded after comparison (never stored,
// never forwarded upstream).
func bearerAuthMiddleware(token string) func(http.Handler) http.Handler {
	expected := []byte(token)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			provided := bearerToken(r.Header.Get("Authorization"))
			if provided == "" || subtle.ConstantTimeCompare([]byte(provided), expected) != 1 {
				w.Header().Set("WWW-Authenticate", "Bearer")
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// forwardAuthMiddleware admits only requests carrying a bearer credential, which
// makeHandler then forwards upstream. The upstream API validates it; this layer
// only refuses requests that carry none, so an anonymous request can never be
// served with some other credential.
func forwardAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if bearerToken(r.Header.Get("Authorization")) == "" {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized: send your API key as Authorization: Bearer <key>", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bearerToken returns the token of an "Authorization: Bearer <token>" value,
// or "".
func bearerToken(authorization string) string {
	fields := strings.Fields(authorization)
	if len(fields) != 2 || !strings.EqualFold(fields[0], "bearer") {
		return ""
	}
	return fields[1]
}

// resourceMetadataURL derives the RFC 9728 metadata URL for the resource server
// from the canonical resource URI (audience). It always points at the well-known
// path this server serves the metadata from.
func resourceMetadataURL(audience string) string {
	if u, err := url.Parse(audience); err == nil && u.Host != "" {
		return u.Scheme + "://" + u.Host + resourceMetadataPath
	}
	return strings.TrimRight(audience, "/") + resourceMetadataPath
}

// resolveJWKSURL returns the explicit JWKS URL when set, otherwise discovers it
// from the issuer's OAuth authorization-server metadata (RFC 8414) with a 10s
// timeout.
func resolveJWKSURL(ctx context.Context, issuer, explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	metaURL := strings.TrimRight(issuer, "/") + "/.well-known/oauth-authorization-server"

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metaURL, nil)
	if err != nil {
		return "", fmt.Errorf("build AS metadata request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch AS metadata from %s: %w (pass --http-oauth-jwks to skip discovery)", metaURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("AS metadata %s returned status %d (pass --http-oauth-jwks to skip discovery)", metaURL, resp.StatusCode)
	}
	var meta struct {
		JWKSURI string `json:"jwks_uri"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return "", fmt.Errorf("decode AS metadata: %w", err)
	}
	if meta.JWKSURI == "" {
		return "", fmt.Errorf("AS metadata %s has no jwks_uri (pass --http-oauth-jwks)", metaURL)
	}
	return meta.JWKSURI, nil
}

// extractScopes reads OAuth scopes from a JWT, accepting the space-separated
// "scope" string claim and the "scp" claim (string or array).
func extractScopes(claims jwt.MapClaims) []string {
	var scopes []string
	if s, ok := claims["scope"].(string); ok {
		scopes = append(scopes, strings.Fields(s)...)
	}
	switch v := claims["scp"].(type) {
	case string:
		scopes = append(scopes, strings.Fields(v)...)
	case []any:
		for _, e := range v {
			if s, ok := e.(string); ok {
				scopes = append(scopes, s)
			}
		}
	}
	return scopes
}

// newJWTVerifier builds an auth.TokenVerifier that validates the JWT signature
// against the JWKS, checks iss/aud/exp/nbf, and extracts the token scopes.
// Invalid signature, wrong issuer/audience, or expiry unwrap to
// auth.ErrInvalidToken → the middleware answers 401. Scope enforcement is left
// to the middleware (which answers 403 for missing scopes).
func newJWTVerifier(kf keyfunc.Keyfunc, issuer, audience string) auth.TokenVerifier {
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{"RS256", "RS384", "RS512", "ES256", "ES384", "ES512", "PS256", "PS384", "PS512"}),
		jwt.WithIssuer(issuer),
		jwt.WithAudience(audience),
		jwt.WithExpirationRequired(),
	)
	return func(ctx context.Context, tokenStr string, _ *http.Request) (*auth.TokenInfo, error) {
		claims := jwt.MapClaims{}
		tok, err := parser.ParseWithClaims(tokenStr, claims, kf.Keyfunc)
		if err != nil || !tok.Valid {
			return nil, fmt.Errorf("%w: %v", auth.ErrInvalidToken, err)
		}
		info := &auth.TokenInfo{Scopes: extractScopes(claims)}
		if exp, _ := claims.GetExpirationTime(); exp != nil {
			info.Expiration = exp.Time
		}
		return info, nil
	}
}

// protectedResourceMetadata builds the RFC 9728 metadata document advertised at
// resourceMetadataPath.
func protectedResourceMetadata(cfg oauthConfig, jwksURL string) *oauthex.ProtectedResourceMetadata {
	return &oauthex.ProtectedResourceMetadata{
		Resource:               cfg.audience,
		AuthorizationServers:   []string{cfg.issuer},
		BearerMethodsSupported: []string{"header"},
		ScopesSupported:        cfg.scopes,
		JWKSURI:                jwksURL,
	}
}

// newOAuthMiddleware wires an OAuth 2.1 resource server: it resolves the JWKS,
// builds a JWT verifier, and returns go-sdk bearer middleware plus the RFC 9728
// metadata document to serve at resourceMetadataPath.
func newOAuthMiddleware(ctx context.Context, cfg oauthConfig) (func(http.Handler) http.Handler, *oauthex.ProtectedResourceMetadata, error) {
	jwksURL, err := resolveJWKSURL(ctx, cfg.issuer, cfg.jwksURL)
	if err != nil {
		return nil, nil, err
	}
	kf, err := keyfunc.NewDefaultCtx(ctx, []string{jwksURL})
	if err != nil {
		return nil, nil, fmt.Errorf("initialize JWKS from %s: %w", jwksURL, err)
	}
	verifier := newJWTVerifier(kf, cfg.issuer, cfg.audience)
	mw := auth.RequireBearerToken(verifier, &auth.RequireBearerTokenOptions{
		ResourceMetadataURL: resourceMetadataURL(cfg.audience),
		Scopes:              cfg.scopes,
	})
	return mw, protectedResourceMetadata(cfg, jwksURL), nil
}
