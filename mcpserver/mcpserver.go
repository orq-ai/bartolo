// Package mcpserver is the runtime support library for the MCP server surface
// that Bartolo emits inside every generated CLI. It is imported only by the
// generated MCP code (cli/generated/mcp_tools.go and register.go), which keeps
// the Model Context Protocol SDK out of the `cli` package's import graph — an
// existing generated CLI that merely re-runs `go mod tidy` must not transitively
// pull in the go-sdk through `cli`.
//
// The generator produces a []ToolDef describing each API operation as an MCP
// tool: a raw JSON Schema input/output schema, HTTP-derived annotations, a
// routing table telling the handler where each argument goes, and a handler
// closure that calls the existing generated client func. This package turns
// those definitions into a running MCP server over stdio or Streamable HTTP
// (spec 2026-07-28) and a `mcp list` manifest command. User-owned extra and
// patched tools are registered from cli/custom via Use.
package mcpserver

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ArgRoute describes where a single top-level tool input property is sent on
// the wire when APITool reconstructs the underlying API call.
type ArgRoute struct {
	// Prop is the property name in the tool input schema.
	Prop string
	// WireName is the OpenAPI parameter name or request-body property name.
	// It is empty for a body sent whole (a non-object request body).
	WireName string
	// In is one of "path", "query", "header", or "body".
	In string
	// Flag is the CLI flag name the generated client func reads an optional
	// parameter from.
	Flag string
	// Required marks a parameter the client func takes as a positional
	// argument (see APIRequest.Arg).
	Required bool
}

// ToolDef is the generator-produced description of one MCP tool. Schemas are
// carried as raw JSON so the full fidelity of the OpenAPI schema (oneOf/anyOf/
// enum/nested objects) survives without generating Go structs per operation.
type ToolDef struct {
	// Name is the unique tool name (matches ^[a-zA-Z0-9_-]{1,128}$).
	Name string
	// Title is a human-readable display name (OpenAPI summary).
	Title string
	// Description is the LLM-facing description.
	Description string
	// Group is the command group the operation belongs to (used by --groups).
	Group string
	// InputSchema is a JSON Schema 2020-12 object describing the tool arguments.
	InputSchema json.RawMessage
	// OutputSchema is the JSON Schema for the structured result. nil → omitted.
	OutputSchema json.RawMessage

	// ReadOnly/Destructive/Idempotent map to the MCP tool annotations and are
	// derived from the HTTP method by the generator.
	ReadOnly    bool
	Destructive bool
	Idempotent  bool

	// Routes maps each top-level input property to its wire destination.
	Routes []ArgRoute
	// WrapResult reports that OutputSchema wraps a non-object response body as
	// {"result": ...}; APITool wraps the decoded value to match.
	WrapResult bool
	// Handler executes the underlying API call. args is the validated,
	// unmarshaled tool arguments. It returns the decoded response value (any
	// JSON-serializable value) or an error; a *cli.HTTPError is surfaced as a
	// structured error result.
	Handler ToolHandler
}

// ToolHandler is the business-logic callback for one MCP tool. It runs after
// JSON Schema validation. Wrappers registered via WrapAll / PatchTool wrap this
// function, not the protocol handler.
type ToolHandler func(ctx context.Context, args map[string]any) (any, error)

// annotations builds the MCP tool annotations for this definition. The API
// domain is closed, so OpenWorldHint is always false.
func (d ToolDef) annotations() *mcp.ToolAnnotations {
	destructive := d.Destructive
	openWorld := false
	return &mcp.ToolAnnotations{
		Title:           d.Title,
		ReadOnlyHint:    d.ReadOnly,
		DestructiveHint: &destructive,
		IdempotentHint:  d.Idempotent,
		OpenWorldHint:   &openWorld,
	}
}
