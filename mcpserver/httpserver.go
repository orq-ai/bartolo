package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/orq-ai/bartolo/cli"
)

const (
	// httpShutdownTimeout bounds the graceful-shutdown drain on SIGINT/SIGTERM.
	httpShutdownTimeout = 10 * time.Second
	// httpReadTimeout bounds reading a whole request, body included; the SDK
	// caps bodies at mcp.DefaultMaxRequestBodyBytes. There is deliberately no
	// write timeout: a tool call's response is written when the upstream API
	// answers.
	httpReadTimeout = 30 * time.Second
	// httpIdleTimeout closes idle keep-alive connections.
	httpIdleTimeout = 2 * time.Minute
)

// httpServeOptions captures the resolved --http* flag surface for the
// Streamable HTTP transport.
type httpServeOptions struct {
	addr           string   // bind address, e.g. "127.0.0.1:8484"
	authMode       string   // authModeNone | authModeBearer | authModeOAuth
	allowedOrigins []string // origins trusted for cross-origin browser requests
	insecure       bool     // override the non-loopback + auth-none safety rail
	token          string   // static bearer token (authModeBearer)
	oauth          oauthConfig
	routes         []httpRoute // extra unauthenticated routes (HTTPRoute)
}

// serveHTTP builds the shared MCP server and serves it over Streamable HTTP with
// the configured inbound auth, until the context is cancelled by SIGINT/SIGTERM.
//
// A single *mcp.Server is shared across all requests: generated tools are
// stateless closures, and Streamable HTTP is served stateless (MCP 2026-07-28:
// no sessions, every request self-describing), so any number of replicas can
// sit behind a plain load balancer. The inbound bearer/JWT is used only to
// authorize the request and is never forwarded to the upstream API
// (confused-deputy prevention; see httpauth.go).
func serveHTTP(ctx context.Context, info serverInfo, defs []ToolDef, opts httpServeOptions) error {
	// Keep stdout clean in HTTP mode too: some formatter paths still target
	// cli.Stdout.
	redirectStdout()

	if err := validateBindAddr(opts.addr, opts.authMode, opts.insecure); err != nil {
		return err
	}

	server, err := buildServer(info, defs)
	if err != nil {
		return err
	}
	if info.settings.serverCard != nil {
		opts.routes = append(opts.routes, httpRoute{
			pattern: "GET " + info.settings.serverCard.endpoint() + "/server-card",
			handler: serverCardHandler(info, opts.authMode),
		})
	}
	handler, err := buildHTTPHandler(ctx, server, opts)
	if err != nil {
		return err
	}

	listener, err := net.Listen("tcp", opts.addr)
	if err != nil {
		return err
	}
	httpServer := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       httpReadTimeout,
		IdleTimeout:       httpIdleTimeout,
	}

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	fmt.Fprintf(cli.Stderr, "%s MCP server listening on http://%s (auth: %s)\n", info.name, listener.Addr(), opts.authMode)

	errCh := make(chan error, 1)
	go func() { errCh <- httpServer.Serve(listener) }()

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
// the go-sdk streamable handler wrapped with the configured inbound auth and
// cross-origin protection, plus (in oauth mode) the RFC 9728 metadata endpoint.
// It is separated from serveHTTP so tests can drive it via httptest without
// binding a socket.
//
// Stateless is what lets clients negotiate 2026-07-28 over Streamable HTTP
// (the SDK caps a stateful handler at 2025-11-25). PropagateRequestCancellation
// cancels a tool call when its HTTP request goes away, which in turn aborts the
// upstream API request (see cli.SetRequestContext). The SDK also rejects a
// request reaching a loopback listener with a non-loopback Host header, which
// is the DNS-rebinding guard.
func buildHTTPHandler(ctx context.Context, server *mcp.Server, opts httpServeOptions) (http.Handler, error) {
	streamable := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{
			Stateless:                    true,
			PropagateRequestCancellation: true,
		},
	)

	protection, err := crossOriginProtection(opts.allowedOrigins)
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthHandler)
	mux.HandleFunc("GET /readyz", healthHandler)
	if err := mountRoutes(mux, opts.routes); err != nil {
		return nil, err
	}

	mcpHandler, err := withInboundAuth(ctx, mux, streamable, opts)
	if err != nil {
		return nil, err
	}
	mux.Handle("/", protection.Handler(mcpHandler))
	return mux, nil
}

// healthHandler answers liveness and readiness probes. The server holds no
// connections of its own to check: it is ready as soon as it listens.
func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}

// mountRoutes registers the HTTPRoute handlers, reporting a pattern the mux
// rejects (a conflict, or "/", which serves MCP) as an error rather than a
// panic.
func mountRoutes(mux *http.ServeMux, routes []httpRoute) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("HTTPRoute: %v", r)
		}
	}()
	for _, route := range routes {
		if route.pattern == "/" {
			return errors.New(`HTTPRoute: "/" serves MCP and cannot be replaced`)
		}
		mux.Handle(route.pattern, route.handler)
	}
	return nil
}

// withInboundAuth wraps next with the configured inbound auth. In oauth mode it
// also mounts the RFC 9728 metadata endpoint on mux: public discovery data,
// served without auth or an origin restriction.
func withInboundAuth(ctx context.Context, mux *http.ServeMux, next http.Handler, opts httpServeOptions) (http.Handler, error) {
	switch opts.authMode {
	case authModeNone:
		return next, nil
	case authModeBearer:
		if opts.token == "" {
			return nil, errors.New("bearer auth requires a token; set the <PREFIX>_MCP_TOKEN environment variable")
		}
		return bearerAuthMiddleware(opts.token)(next), nil
	case authModeForward:
		return forwardAuthMiddleware(next), nil
	case authModeOAuth:
		mw, meta, err := newOAuthMiddleware(ctx, opts.oauth)
		if err != nil {
			return nil, err
		}
		mux.Handle(resourceMetadataPath, auth.ProtectedResourceMetadataHandler(meta))
		return mw(next), nil
	}
	return nil, fmt.Errorf("invalid --http-auth %q (want none|bearer|oauth|forward)", opts.authMode)
}

// crossOriginProtection rejects cross-origin browser requests with 403 unless
// their Origin is in allowed. Same-origin requests pass, as do non-browser
// clients, which send neither Origin nor Sec-Fetch-Site. This is the Origin
// validation the Streamable HTTP transport requires of every server.
func crossOriginProtection(allowed []string) (*http.CrossOriginProtection, error) {
	protection := http.NewCrossOriginProtection()
	for _, origin := range allowed {
		if origin = strings.TrimSpace(origin); origin == "" {
			continue
		}
		if err := protection.AddTrustedOrigin(origin); err != nil {
			return nil, fmt.Errorf("--http-allowed-origins: %w", err)
		}
	}
	return protection, nil
}
