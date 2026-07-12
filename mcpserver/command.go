package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/signal"
	"strings"
	"syscall"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
)

// Option configures the mcp command. Options are applied in order.
type Option func(*config)

type config struct {
	instructions string
}

// SetInstructions sets the MCP server instructions surfaced to connected
// clients (ServerOptions.Instructions). The generator passes app-specific
// guidance (auth env vars, `auth setup`, etc.).
func SetInstructions(s string) Option {
	return func(c *config) { c.instructions = s }
}

// filterOptions capture the runtime tool-narrowing flags.
type filterOptions struct {
	tools    []string
	groups   []string
	readOnly bool
}

// filterTools narrows the tool list per the --tools / --groups / --read-only
// flags. An empty filter returns every tool. When --tools is set it selects by
// exact name; --groups selects by group; --read-only keeps only ReadOnly
// tools. The filters compose (AND) so callers can e.g. take the read-only tools
// within a group.
func filterTools(defs []ToolDef, f filterOptions) []ToolDef {
	toolSet := make(map[string]bool, len(f.tools))
	for _, t := range f.tools {
		if t = strings.TrimSpace(t); t != "" {
			toolSet[t] = true
		}
	}
	groupSet := make(map[string]bool, len(f.groups))
	for _, g := range f.groups {
		if g = strings.TrimSpace(g); g != "" {
			groupSet[g] = true
		}
	}

	out := make([]ToolDef, 0, len(defs))
	for _, d := range defs {
		if len(toolSet) > 0 && !toolSet[d.Name] {
			continue
		}
		if len(groupSet) > 0 && !groupSet[d.Group] {
			continue
		}
		if f.readOnly && !d.ReadOnly {
			continue
		}
		out = append(out, d)
	}
	return out
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
		flagTools    []string
		flagGroups   []string
		flagReadOnly bool
		flagHTTP     string
	)

	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Run an MCP (Model Context Protocol) server for this API",
		Long: "Serve this API's operations as MCP tools over stdio (default), for use\n" +
			"with MCP clients such as Claude Desktop, Claude Code, and Cursor.\n\n" +
			"Use `" + appName + " mcp list` to print the tool manifest as JSON.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if flagHTTP != "" {
				return fmt.Errorf("http transport not yet supported")
			}
			selected := filterTools(tools(), filterOptions{
				tools:    flagTools,
				groups:   flagGroups,
				readOnly: flagReadOnly,
			})
			return serveStdio(cmd.Context(), appName, version, cfg.instructions, selected)
		},
	}

	cmd.Flags().StringSliceVar(&flagTools, "tools", nil, "Only expose these tools (comma-separated exact names)")
	cmd.Flags().StringSliceVar(&flagGroups, "groups", nil, "Only expose tools in these groups (comma-separated)")
	cmd.Flags().BoolVar(&flagReadOnly, "read-only", false, "Only expose read-only (safe) tools")
	cmd.Flags().StringVar(&flagHTTP, "http", "", "Serve over Streamable HTTP at this address (not yet supported)")

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "Print the MCP tool manifest as JSON",
		RunE: func(cmd *cobra.Command, args []string) error {
			selected := filterTools(tools(), filterOptions{
				tools:    flagTools,
				groups:   flagGroups,
				readOnly: flagReadOnly,
			})
			return writeManifest(cmd.OutOrStdout(), selected)
		},
	}
	listCmd.Flags().StringSliceVar(&flagTools, "tools", nil, "Only list these tools (comma-separated exact names)")
	listCmd.Flags().StringSliceVar(&flagGroups, "groups", nil, "Only list tools in these groups (comma-separated)")
	listCmd.Flags().BoolVar(&flagReadOnly, "read-only", false, "Only list read-only (safe) tools")

	cmd.AddCommand(listCmd)
	return cmd
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

// serveStdio builds the MCP server, registers the selected tools, applies the
// stdio-purity redirections, and serves the JSON-RPC protocol over stdio until
// the context is cancelled (SIGINT/SIGTERM).
func serveStdio(ctx context.Context, appName, version, instructions string, defs []ToolDef) error {
	// stdio purity: stdout carries JSON-RPC, so nothing else may write to it.
	// DefaultFormatter.Format writes to cli.Stdout at call time, and cobra
	// captured cli.Stdout as its output writer at Init time — redirect both to
	// stderr so any stray formatter/usage/error output can't corrupt the stream.
	cli.Stdout = cli.Stderr
	if cli.Root != nil {
		cli.Root.SetOut(cli.Stderr)
		cli.Root.SetErr(cli.Stderr)
	}

	server, err := buildServer(appName, version, instructions, defs)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	return server.Run(ctx, &mcp.StdioTransport{})
}

// buildServer constructs an *mcp.Server with every def registered as a tool.
func buildServer(appName, version, instructions string, defs []ToolDef) (*mcp.Server, error) {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    appName,
		Version: version,
	}, &mcp.ServerOptions{
		Instructions: instructions,
	})

	for _, def := range defs {
		if err := registerTool(server, def); err != nil {
			return nil, fmt.Errorf("registering tool %q: %w", def.Name, err)
		}
	}
	return server, nil
}

// registerTool resolves the tool's input schema once and registers it on the
// server with a self-validating handler (Server.AddTool does not validate).
func registerTool(server *mcp.Server, def ToolDef) error {
	var resolved *jsonschema.Resolved
	if len(def.InputSchema) > 0 {
		var schema jsonschema.Schema
		if err := json.Unmarshal(def.InputSchema, &schema); err != nil {
			return fmt.Errorf("unmarshal input schema: %w", err)
		}
		r, err := schema.Resolve(nil)
		if err != nil {
			return fmt.Errorf("resolve input schema: %w", err)
		}
		resolved = r
	}

	tool := &mcp.Tool{
		Name:        def.Name,
		Description: def.Description,
		InputSchema: def.InputSchema,
		Annotations: def.annotations(),
	}
	if def.Title != "" {
		tool.Title = def.Title
	}
	if len(def.OutputSchema) > 0 {
		tool.OutputSchema = def.OutputSchema
	}

	server.AddTool(tool, makeHandler(def, resolved))
	return nil
}

// makeHandler wraps a ToolDef's handler with argument unmarshaling, schema
// validation, result mapping, and panic recovery. It never returns a protocol
// error for tool-level failures — those become IsError results — reserving
// protocol errors for the SDK (unknown tool, malformed JSON-RPC).
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

		if resolved != nil {
			if err := resolved.Validate(args); err != nil {
				return errorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
			}
		}

		out, err := def.Handler(ctx, args)
		if err != nil {
			return handlerErrorResult(err), nil
		}

		return successResult(out)
	}
}

// successResult builds the standard success result: a text content block with
// the JSON-serialized value plus StructuredContent carrying the decoded value.
func successResult(out any) (*mcp.CallToolResult, error) {
	data, err := json.Marshal(out)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to serialize result: %v", err)), nil
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: string(data)}},
		StructuredContent: out,
	}, nil
}

// handlerErrorResult maps a handler error to an IsError result. *cli.HTTPError
// gets a structured {status, body} payload; anything else surfaces its string.
func handlerErrorResult(err error) *mcp.CallToolResult {
	var httpErr *cli.HTTPError
	if errors.As(err, &httpErr) {
		var parsed any
		if json.Unmarshal(httpErr.Body, &parsed) != nil {
			parsed = string(httpErr.Body)
		}
		res := errorResult(fmt.Sprintf("HTTP %d: %s", httpErr.StatusCode, string(httpErr.Body)))
		res.StructuredContent = map[string]any{
			"status": httpErr.StatusCode,
			"body":   parsed,
		}
		return res
	}
	return errorResult(err.Error())
}

// errorResult builds an IsError result carrying a single text content block.
func errorResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}
}
