package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/orq-ai/bartolo/mcpserver"
)

// MCPConfig is the `mcp` block of .bartolo.json: the protocol-level choices of
// the generated MCP server. Every field is optional. docs/mcp-conformance.md
// maps each one to the MCP SEP it implements.
type MCPConfig struct {
	// Title, Description, WebsiteURL and Icons describe the server to clients
	// (SEP-973) and fill its Server Card.
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	WebsiteURL  string          `json:"website_url,omitempty"`
	Icons       []MCPIconConfig `json:"icons,omitempty"`

	// ListTTLSeconds is how long clients may cache server/discover and
	// tools/list results (SEP-2549). Default 300.
	ListTTLSeconds *int `json:"list_ttl_seconds,omitempty"`
	// TraceContext propagates a caller's W3C trace context to the API
	// (SEP-414). Default true.
	TraceContext *bool `json:"trace_context,omitempty"`
	// ProtocolVersions narrows the protocol versions the server accepts.
	ProtocolVersions []string `json:"protocol_versions,omitempty"`
	// ServerCard publishes an MCP Server Card (SEP-2127) next to the HTTP
	// endpoint. It needs Description.
	ServerCard *MCPServerCardConfig `json:"server_card,omitempty"`
}

// MCPIconConfig is one server icon (SEP-973).
type MCPIconConfig struct {
	Src      string   `json:"src"`
	MIMEType string   `json:"mime_type,omitempty"`
	Sizes    []string `json:"sizes,omitempty"`
}

// MCPServerCardConfig configures the Server Card (SEP-2127).
type MCPServerCardConfig struct {
	// Name is the reverse-DNS server name, "namespace/name".
	Name string `json:"name"`
	// Endpoint is the URL path clients connect to. Default "/mcp".
	Endpoint string `json:"endpoint,omitempty"`
	// PublicURL is the endpoint's full public URL; by default it is built from
	// each request.
	PublicURL  string `json:"public_url,omitempty"`
	Repository string `json:"repository,omitempty"`
}

// check reports a configuration the generated server could not honor, using
// the same validation the server runs at startup.
func (c *MCPConfig) check() error {
	if c == nil {
		return nil
	}
	if c.ListTTLSeconds != nil && *c.ListTTLSeconds < 0 {
		return fmt.Errorf(".bartolo.json mcp.list_ttl_seconds cannot be negative")
	}
	if err := mcpserver.CheckOptions(c.runtimeOptions()...); err != nil {
		return fmt.Errorf(".bartolo.json mcp: %w", err)
	}
	return nil
}

// runtimeOptions are the options optionSource renders, as values, so check
// validates exactly what the generated server will run with.
func (c *MCPConfig) runtimeOptions() []mcpserver.Option {
	var opts []mcpserver.Option
	if c.hasMetadata() {
		meta := mcpserver.Metadata{Title: c.Title, Description: c.Description, WebsiteURL: c.WebsiteURL}
		for _, icon := range c.Icons {
			meta.Icons = append(meta.Icons, mcpserver.Icon{Src: icon.Src, MIMEType: icon.MIMEType, Sizes: icon.Sizes})
		}
		opts = append(opts, mcpserver.SetMetadata(meta))
	}
	if c.ListTTLSeconds != nil {
		opts = append(opts, mcpserver.SetListTTL(time.Duration(*c.ListTTLSeconds)*time.Second))
	}
	if c.TraceContext != nil {
		opts = append(opts, mcpserver.SetTraceContext(*c.TraceContext))
	}
	if len(c.ProtocolVersions) > 0 {
		opts = append(opts, mcpserver.SetProtocolVersions(c.ProtocolVersions...))
	}
	if card := c.ServerCard; card != nil {
		opts = append(opts, mcpserver.PublishServerCard(mcpserver.ServerCard{
			Name: card.Name, Endpoint: card.Endpoint, PublicURL: card.PublicURL, Repository: card.Repository,
		}))
	}
	return opts
}

func (c *MCPConfig) hasMetadata() bool {
	return c.Title != "" || c.Description != "" || c.WebsiteURL != "" || len(c.Icons) > 0
}

// optionSource renders runtimeOptions as Go expressions for the generated
// mcpOptions func. Keep the two in step.
func (c *MCPConfig) optionSource() []string {
	if c == nil {
		return nil
	}
	var opts []string
	if c.hasMetadata() {
		var icons []string
		for _, icon := range c.Icons {
			icons = append(icons, fmt.Sprintf("{Src: %q, MIMEType: %q, Sizes: %s}", icon.Src, icon.MIMEType, goStrings(icon.Sizes)))
		}
		opts = append(opts, fmt.Sprintf("mcpserver.SetMetadata(mcpserver.Metadata{Title: %q, Description: %q, WebsiteURL: %q, Icons: []mcpserver.Icon{%s}})",
			c.Title, c.Description, c.WebsiteURL, strings.Join(icons, ", ")))
	}
	if c.ListTTLSeconds != nil {
		opts = append(opts, fmt.Sprintf("mcpserver.SetListTTL(%d * time.Second)", *c.ListTTLSeconds))
	}
	if c.TraceContext != nil {
		opts = append(opts, fmt.Sprintf("mcpserver.SetTraceContext(%t)", *c.TraceContext))
	}
	if len(c.ProtocolVersions) > 0 {
		opts = append(opts, fmt.Sprintf("mcpserver.SetProtocolVersions(%s...)", goStrings(c.ProtocolVersions)))
	}
	if card := c.ServerCard; card != nil {
		opts = append(opts, fmt.Sprintf("mcpserver.PublishServerCard(mcpserver.ServerCard{Name: %q, Endpoint: %q, PublicURL: %q, Repository: %q})",
			card.Name, card.Endpoint, card.PublicURL, card.Repository))
	}
	return opts
}

// goStrings renders values as a Go []string literal.
func goStrings(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = strconv.Quote(v)
	}
	return "[]string{" + strings.Join(quoted, ", ") + "}"
}

// MCPServerOptions are the Go expressions passed to mcpserver.Command in the
// generated code: the server instructions, then the `mcp` block's options.
func (o *OpenAPI) MCPServerOptions() []string {
	return append([]string{"mcpserver.SetInstructions(mcpInstructions)"}, o.MCPConfig.optionSource()...)
}

// MCPNeedsTime reports whether the generated options use the time package.
func (o *OpenAPI) MCPNeedsTime() bool {
	return o.MCPConfig != nil && o.MCPConfig.ListTTLSeconds != nil
}
