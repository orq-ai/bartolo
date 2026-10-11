package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// listTTL is how long clients may cache server/discover and tools/list results
// (ttlMs, SEP-2549). The tool set is fixed for the life of the process, so the
// only staleness is across a redeploy.
const listTTL = 5 * time.Minute

// validToolName is the MCP tool-name grammar.
var validToolName = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,128}$`)

// Option configures the mcp command. Options are applied in order.
type Option func(*config)

type config struct {
	instructions string
	envPrefix    string

	extra   []ToolDef
	remove  []string
	patches []toolPatch
	wraps   []func(ToolHandler) ToolHandler
}

// SetInstructions sets the MCP server instructions surfaced to connected
// clients (ServerOptions.Instructions). The generator passes app-specific
// guidance (auth env vars, `auth setup`, etc.).
func SetInstructions(s string) Option {
	return func(c *config) { c.instructions = s }
}

// SetEnvPrefix sets the environment-variable prefix used to derive HTTP-mode
// secrets (e.g. the static bearer token env var <PREFIX>_MCP_TOKEN). When unset,
// the prefix is read at runtime from viper's "env-prefix" key (populated by
// cli.Init), so existing generated register wiring keeps working without change.
func SetEnvPrefix(prefix string) Option {
	return func(c *config) { c.envPrefix = prefix }
}

// filterOptions capture the runtime tool-narrowing flags.
type filterOptions struct {
	tools    []string
	groups   []string
	readOnly bool
}

// httpFlags capture the --http* flags.
type httpFlags struct {
	addr           string
	authMode       string
	allowedOrigins []string
	insecure       bool
	oauthIssuer    string
	oauthAudience  string
	oauthJWKS      string
	oauthScopes    []string
}

// httpOnlyFlags are the flags that are only meaningful with --http.
var httpOnlyFlags = []string{
	"http-auth", "http-allowed-origins", "http-insecure",
	"http-oauth-issuer", "http-oauth-audience", "http-oauth-jwks", "http-oauth-scopes",
}

// serverInfo identifies the server to clients and sets the cache scope of its
// list results.
type serverInfo struct {
	name         string
	version      string
	instructions string
	// cacheScope is "private" when HTTP requests are authenticated, so a shared
	// intermediary never serves the tool list to a caller that could not have
	// fetched it; "public" otherwise.
	cacheScope string
}

// filterTools narrows the tool list per the --tools / --groups / --read-only
// flags. An empty filter returns every tool. When --tools is set it selects by
// exact name; --groups selects by group; --read-only keeps only ReadOnly
// tools. The filters compose (AND) so callers can e.g. take the read-only tools
// within a group.
func filterTools(defs []ToolDef, f filterOptions) []ToolDef {
	toolSet, groupSet := nameSet(f.tools), nameSet(f.groups)
	out := make([]ToolDef, 0, len(defs))
	for _, d := range defs {
		if matchesSet(toolSet, d.Name) && matchesSet(groupSet, d.Group) && (!f.readOnly || d.ReadOnly) {
			out = append(out, d)
		}
	}
	return out
}

// nameSet trims names and drops empty ones.
func nameSet(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			set[n] = true
		}
	}
	return set
}

// matchesSet reports whether name passes a filter set; an empty set passes all.
func matchesSet(set map[string]bool, name string) bool {
	return len(set) == 0 || set[name]
}

// Command builds the `mcp` cobra command for a generated CLI. appName/version
// identify the server to clients; tools is called lazily (so the potentially
// large generated tool table is only materialized when the command actually
// runs) and returns the full tool set.
func Command(appName, version string, tools func() []ToolDef, opts ...Option) *cobra.Command {
	cfg := &config{}
	for _, o := range opts {
		o(cfg)
	}

	var (
		filters filterOptions
		http    httpFlags
	)

	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Run an MCP (Model Context Protocol) server for this API",
		Long:  commandLong(appName, cfg.envPrefix),
		RunE: func(cmd *cobra.Command, args []string) error {
			runtime, selected, err := prepareTools(tools, cfg, filters)
			if err != nil {
				return err
			}
			info := serverInfo{name: appName, version: version, instructions: runtime.instructions, cacheScope: "public"}

			if http.addr == "" {
				if err := rejectHTTPOnlyFlags(cmd.Flags()); err != nil {
					return err
				}
				return serveStdio(cmd.Context(), info, selected)
			}

			opts, err := resolveHTTPOptions(runtime.envPrefix, http)
			if err != nil {
				return err
			}
			if opts.authMode != authModeNone {
				info.cacheScope = "private"
			}
			return serveHTTP(cmd.Context(), info, selected, opts)
		},
	}

	bindFilterFlags(cmd.PersistentFlags(), &filters)
	bindHTTPFlags(cmd.Flags(), &http)
	cmd.AddCommand(listCommand(tools, cfg, &filters))
	return cmd
}

func commandLong(appName, envPrefix string) string {
	return "Serve this API's operations as MCP tools for use with MCP clients such as\n" +
		"Claude Desktop, Claude Code, and Cursor.\n\n" +
		"Transports:\n" +
		"  (default)      serve over stdio — the transport MCP clients spawn directly.\n" +
		"  --http <addr>  serve stateless Streamable HTTP at <addr> (e.g. 127.0.0.1:8484).\n\n" +
		"HTTP inbound auth (--http only):\n" +
		"  --http-auth none     no auth (default; only allowed on a loopback address\n" +
		"                       unless --http-insecure is set).\n" +
		"  --http-auth bearer   static bearer token from the " + tokenEnvVar(envPrefix) + "\n" +
		"                       environment variable (constant-time compared).\n" +
		"  --http-auth oauth    OAuth 2.1 resource server: JWTs are validated against the\n" +
		"                       authorization server's JWKS (--http-oauth-issuer,\n" +
		"                       --http-oauth-audience, optional --http-oauth-jwks and\n" +
		"                       --http-oauth-scopes). RFC 9728 protected-resource metadata\n" +
		"                       is served at " + resourceMetadataPath + ".\n" +
		"  --http-allowed-origins  comma-separated origins (scheme://host[:port]) trusted\n" +
		"                       for cross-origin browser requests. Other cross-origin\n" +
		"                       browser requests are rejected (DNS-rebinding and CSRF\n" +
		"                       guard); same-origin and non-browser clients are allowed.\n\n" +
		"The inbound HTTP credential is used only to authorize the request; it is never\n" +
		"forwarded to the upstream API.\n\n" +
		"Use `" + appName + " mcp list` to print the tool manifest as JSON."
}

func bindFilterFlags(flags *pflag.FlagSet, f *filterOptions) {
	flags.StringSliceVar(&f.tools, "tools", nil, "Only expose these tools (comma-separated exact names)")
	flags.StringSliceVar(&f.groups, "groups", nil, "Only expose tools in these groups (comma-separated)")
	flags.BoolVar(&f.readOnly, "read-only", false, "Only expose read-only (safe) tools")
}

func bindHTTPFlags(flags *pflag.FlagSet, h *httpFlags) {
	flags.StringVar(&h.addr, "http", "", "Serve stateless Streamable HTTP at this address (e.g. 127.0.0.1:8484)")
	flags.StringVar(&h.authMode, "http-auth", authModeNone, "Inbound HTTP auth mode: none|bearer|oauth (requires --http)")
	flags.StringSliceVar(&h.allowedOrigins, "http-allowed-origins", nil, "Origins trusted for cross-origin browser requests, comma-separated (requires --http)")
	flags.BoolVar(&h.insecure, "http-insecure", false, "Allow binding a non-loopback address with --http-auth none (requires --http)")
	flags.StringVar(&h.oauthIssuer, "http-oauth-issuer", "", "OAuth authorization server issuer (required for --http-auth oauth)")
	flags.StringVar(&h.oauthAudience, "http-oauth-audience", "", "OAuth audience / canonical resource URI (required for --http-auth oauth)")
	flags.StringVar(&h.oauthJWKS, "http-oauth-jwks", "", "JWKS URL (default: discovered from the issuer's AS metadata)")
	flags.StringSliceVar(&h.oauthScopes, "http-oauth-scopes", nil, "Required OAuth scopes, comma-separated (all must be present)")
}

func rejectHTTPOnlyFlags(flags *pflag.FlagSet) error {
	for _, name := range httpOnlyFlags {
		if flags.Changed(name) {
			return fmt.Errorf("--%s requires --http", name)
		}
	}
	return nil
}

func listCommand(tools func() []ToolDef, cfg *config, filters *filterOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Print the MCP tool manifest as JSON",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, selected, err := prepareTools(tools, cfg, *filters)
			if err != nil {
				return err
			}
			return writeManifest(cmd.OutOrStdout(), selected)
		},
	}
}

// tokenEnvVar returns the environment-variable name that holds the static bearer
// token for --http-auth bearer. The prefix is the explicit SetEnvPrefix value,
// falling back to viper's runtime "env-prefix" (set by cli.Init).
func tokenEnvVar(envPrefix string) string {
	if envPrefix == "" {
		envPrefix = viper.GetString("env-prefix")
	}
	if envPrefix == "" {
		return "MCP_TOKEN"
	}
	return envPrefix + "_MCP_TOKEN"
}

// resolveHTTPOptions validates the --http* flag combination and materializes the
// httpServeOptions consumed by serveHTTP.
func resolveHTTPOptions(envPrefix string, f httpFlags) (httpServeOptions, error) {
	opts := httpServeOptions{
		addr:           f.addr,
		authMode:       f.authMode,
		allowedOrigins: f.allowedOrigins,
		insecure:       f.insecure,
	}

	switch f.authMode {
	case authModeNone:
		// nothing to resolve
	case authModeBearer:
		opts.token = os.Getenv(tokenEnvVar(envPrefix))
		if opts.token == "" {
			return httpServeOptions{}, fmt.Errorf("--http-auth bearer requires the %s environment variable to be set", tokenEnvVar(envPrefix))
		}
	case authModeOAuth:
		if f.oauthIssuer == "" || f.oauthAudience == "" {
			return httpServeOptions{}, errors.New("--http-auth oauth requires --http-oauth-issuer and --http-oauth-audience")
		}
		opts.oauth = oauthConfig{
			issuer:   f.oauthIssuer,
			audience: f.oauthAudience,
			jwksURL:  f.oauthJWKS,
			scopes:   f.oauthScopes,
		}
	default:
		return httpServeOptions{}, fmt.Errorf("invalid --http-auth %q (want none|bearer|oauth)", f.authMode)
	}

	return opts, nil
}

// manifestTool is the JSON shape emitted by `mcp list`.
type manifestTool struct {
	Name         string               `json:"name"`
	Title        string               `json:"title,omitempty"`
	Description  string               `json:"description,omitempty"`
	Annotations  *mcp.ToolAnnotations `json:"annotations,omitempty"`
	InputSchema  json.RawMessage      `json:"inputSchema,omitempty"`
	OutputSchema json.RawMessage      `json:"outputSchema,omitempty"`
}

// writeManifest prints the selected tools as a JSON array to w.
func writeManifest(w io.Writer, defs []ToolDef) error {
	manifest := make([]manifestTool, 0, len(defs))
	for _, d := range defs {
		manifest = append(manifest, manifestTool{
			Name:         d.Name,
			Title:        d.Title,
			Description:  d.Description,
			Annotations:  d.annotations(),
			InputSchema:  d.InputSchema,
			OutputSchema: d.OutputSchema,
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(manifest)
}

// redirectStdout keeps stdout clean: on stdio it carries JSON-RPC, so nothing
// else may write to it. DefaultFormatter.Format writes to cli.Stdout at call
// time, and cobra captured cli.Stdout as its output writer at Init time —
// redirect both to stderr so any stray formatter/usage/error output can't
// corrupt the stream.
func redirectStdout() {
	cli.Stdout = cli.Stderr
	if cli.Root != nil {
		cli.Root.SetOut(cli.Stderr)
		cli.Root.SetErr(cli.Stderr)
	}
}

// serveStdio builds the MCP server, registers the selected tools, applies the
// stdio-purity redirections, and serves the JSON-RPC protocol over stdio until
// the context is cancelled (SIGINT/SIGTERM).
func serveStdio(ctx context.Context, info serverInfo, defs []ToolDef) error {
	redirectStdout()

	server, err := buildServer(info, defs)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	return server.Run(ctx, &mcp.StdioTransport{})
}

// buildServer constructs an *mcp.Server with every def registered as a tool.
//
// The server declares only the tools capability: logging is deprecated as of
// 2026-07-28 (SEP-2577) and the SDK would otherwise advertise it, and the tool
// set never changes while the server runs, so there is no list_changed to send.
// SDK diagnostics go to stderr, never stdout.
func buildServer(info serverInfo, defs []ToolDef) (*mcp.Server, error) {
	cacheScope := info.cacheScope
	if cacheScope == "" {
		cacheScope = "public"
	}
	server := mcp.NewServer(&mcp.Implementation{
		Name:    info.name,
		Version: info.version,
	}, &mcp.ServerOptions{
		Instructions: info.instructions,
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
		Logger:       slog.New(slog.NewTextHandler(cli.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
		SetCacheable: func(_ context.Context, _ mcp.Request, c *mcp.Cacheable) {
			c.TTLMs = int(listTTL / time.Millisecond)
			c.CacheScope = cacheScope
		},
	})

	for _, def := range defs {
		if err := registerTool(server, def); err != nil {
			return nil, fmt.Errorf("registering tool %q: %w", def.Name, err)
		}
	}
	return server, nil
}

// emptyObjectSchema is the input schema of a tool that declares none.
var emptyObjectSchema = json.RawMessage(`{"type":"object"}`)

// registerTool resolves the tool's input schema once and registers it on the
// server with a self-validating handler (Server.AddTool does not validate).
// Every check the SDK would panic on is reported as an error instead, since a
// custom tool from cli/custom reaches here unvetted.
func registerTool(server *mcp.Server, def ToolDef) error {
	if def.Handler == nil {
		return errors.New("tool has no handler")
	}
	inputSchema := def.InputSchema
	if len(inputSchema) == 0 {
		inputSchema = emptyObjectSchema
	}
	resolved, err := resolveInputSchema(inputSchema)
	if err != nil {
		return err
	}
	if len(def.OutputSchema) > 0 && !json.Valid(def.OutputSchema) {
		return errors.New("output schema is not valid JSON")
	}

	tool := &mcp.Tool{
		Name:        def.Name,
		Title:       def.Title,
		Description: def.Description,
		InputSchema: inputSchema,
		Annotations: def.annotations(),
	}
	if len(def.OutputSchema) > 0 {
		tool.OutputSchema = def.OutputSchema
	}

	server.AddTool(tool, makeHandler(def, resolved))
	return nil
}

// resolveInputSchema parses and resolves an input schema, which MCP requires to
// be of type "object".
func resolveInputSchema(raw json.RawMessage) (*jsonschema.Resolved, error) {
	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, fmt.Errorf("unmarshal input schema: %w", err)
	}
	if schema.Type != "object" {
		return nil, errors.New(`input schema must have type "object"`)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return nil, fmt.Errorf("resolve input schema: %w", err)
	}
	return resolved, nil
}

// makeHandler wraps a ToolDef's handler with argument unmarshaling, schema
// validation, result mapping, and panic recovery. It never returns a protocol
// error for tool-level failures — those become IsError results the model can
// read and correct — reserving protocol errors for the SDK (unknown tool,
// malformed JSON-RPC).
func makeHandler(def ToolDef, resolved *jsonschema.Resolved) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (result *mcp.CallToolResult, retErr error) {
		defer func() {
			if r := recover(); r != nil {
				result = errorResult(fmt.Sprintf("tool panicked: %v", r))
				retErr = nil
			}
		}()

		args := map[string]any{}
		if raw := req.Params.Arguments; len(raw) > 0 {
			if err := json.Unmarshal(raw, &args); err != nil {
				return errorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
			}
		}
		if err := resolved.Validate(args); err != nil {
			return errorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
		}

		out, err := def.Handler(ctx, args)
		if err != nil {
			return handlerErrorResult(err), nil
		}
		return successResult(def, out), nil
	}
}

// successResult returns the value as serialized JSON text and, where clients
// accept it, as structuredContent. Clients before 2026-07-28 require
// structuredContent to be an object, so a tool without an output schema sends
// arrays and scalars as text only. A tool with an output schema must always
// send structuredContent, so an empty response body becomes {}.
func successResult(def ToolDef, out any) *mcp.CallToolResult {
	data, err := json.Marshal(out)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to serialize result: %v", err))
	}
	res := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}

	hasSchema := len(def.OutputSchema) > 0
	switch {
	case hasSchema && string(data) == "null":
		res.StructuredContent = map[string]any{}
	case hasSchema || bytes.HasPrefix(data, []byte("{")):
		res.StructuredContent = json.RawMessage(data)
	}
	return res
}

// handlerErrorResult maps a handler error to an IsError result. *cli.HTTPError
// gets a structured {status, body} payload; anything else surfaces its string.
func handlerErrorResult(err error) *mcp.CallToolResult {
	var httpErr *cli.HTTPError
	if !errors.As(err, &httpErr) {
		return errorResult(err.Error())
	}
	var body any
	if json.Unmarshal(httpErr.Body, &body) != nil {
		body = string(httpErr.Body)
	}
	res := errorResult(httpErr.Error())
	res.StructuredContent = map[string]any{
		"status": httpErr.StatusCode,
		"body":   body,
	}
	return res
}

// errorResult builds an IsError result carrying a single text content block.
func errorResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}
}
