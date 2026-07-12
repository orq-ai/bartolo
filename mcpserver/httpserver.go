package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/orq-ai/bartolo/cli"
)

// httpShutdownTimeout bounds the graceful-shutdown drain on SIGINT/SIGTERM.
const httpShutdownTimeout = 10 * time.Second

// httpServeOptions captures the resolved --http* flag surface for the
// Streamable HTTP transport.
type httpServeOptions struct {
	addr           string   // bind address, e.g. "127.0.0.1:8484"
	authMode       string   // authModeNone | authModeBearer | authModeOAuth
	allowedOrigins []string // Origin allow-list; empty disables the check
	insecure       bool     // override the non-loopback + auth-none safety rail
	token          string   // static bearer token (authModeBearer)
	oauth          oauthConfig
}

// serveHTTP builds the shared MCP server and serves it over Streamable HTTP with
// the configured inbound auth, until the context is cancelled by SIGINT/SIGTERM.
//
// A single *mcp.Server is shared across all client sessions: the generated tools
// are stateless closures, so no per-session server state is required. The inbound
// bearer/JWT is used only to authorize the request and is never forwarded to the
// upstream API (confused-deputy prevention; see httpauth.go).
func serveHTTP(ctx context.Context, appName, version, instructions string, defs []ToolDef, opts httpServeOptions) error {
	// stdout hygiene: keep stdout clean even in HTTP mode (some formatter paths
	// still target cli.Stdout). Redirect stray output to stderr.
	cli.Stdout = cli.Stderr
	if cli.Root != nil {
		cli.Root.SetOut(cli.Stderr)
		cli.Root.SetErr(cli.Stderr)
	}

	if err := validateBindAddr(opts.addr, opts.authMode, opts.insecure); err != nil {
		return err
	}

	server, err := buildServer(appName, version, instructions, defs)
	if err != nil {
		return err
	}

	handler, err := buildHTTPHandler(ctx, server, opts)
	if err != nil {
		return err
	}

	httpServer := &http.Server{
		Addr:              opts.addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	fmt.Fprintf(cli.Stderr, "%s MCP server listening on http://%s (auth: %s)\n", appName, opts.addr, opts.authMode)

	errCh := make(chan error, 1)
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), httpShutdownTimeout)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	}
}

// buildHTTPHandler assembles the http.Handler for the Streamable HTTP transport:
// the go-sdk streamable handler wrapped with the configured inbound-auth and
// Origin middleware, plus (in oauth mode) the RFC 9728 metadata endpoint. It is
// separated from serveHTTP so tests can drive it via httptest without binding a
// socket.
func buildHTTPHandler(ctx context.Context, server *mcp.Server, opts httpServeOptions) (http.Handler, error) {
	streamable := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)

	mux := http.NewServeMux()

	var mcpHandler http.Handler = streamable
	switch opts.authMode {
	case authModeNone:
		// no inbound auth
	case authModeBearer:
		if opts.token == "" {
			return nil, errors.New("bearer auth requires a token; set the <PREFIX>_MCP_TOKEN environment variable")
		}
		mcpHandler = bearerAuthMiddleware(opts.token)(mcpHandler)
	case authModeOAuth:
		mw, meta, err := newOAuthMiddleware(ctx, opts.oauth)
		if err != nil {
			return nil, err
		}
		mcpHandler = mw(mcpHandler)
		// RFC 9728 metadata is public discovery data — served without auth or an
		// Origin restriction (the go-sdk handler adds permissive CORS).
		mux.Handle(resourceMetadataPath, auth.ProtectedResourceMetadataHandler(meta))
	default:
		return nil, fmt.Errorf("invalid --http-auth %q (want none|bearer|oauth)", opts.authMode)
	}

	// Origin check wraps the MCP endpoint outermost (browser DNS-rebinding guard).
	mcpHandler = originCheckMiddleware(opts.allowedOrigins)(mcpHandler)
	mux.Handle("/", mcpHandler)

	return mux, nil
}
