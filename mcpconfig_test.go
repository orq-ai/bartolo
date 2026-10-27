package main

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func headerSpec(annotation string) string {
	return `
openapi: 3.1.0
info: {title: t, version: "1"}
paths:
  /widgets:
    post:
      operationId: CreateWidget
      parameters:
        - name: region
          in: query
          schema: ` + annotation + `
          x-bartolo-mcp-header: Region
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                tenant: {type: string, x-bartolo-mcp-header: Tenant}
      responses: {"200": {description: ok}}
`
}

// SEP-2243: an annotated parameter or body property carries x-mcp-header, so
// clients mirror its value into an Mcp-Param-* header.
func TestMCPHeaderAnnotation(t *testing.T) {
	op := findOp(ProcessAPI("t", loadTestSpec(t, headerSpec("{type: string}"))), "/widgets", "post")
	schema := schemaOf(t, op.MCP.InputSchema)
	if got := propOf(t, schema, "region")["x-mcp-header"]; got != "Region" {
		t.Errorf("region x-mcp-header = %v", got)
	}
	if got := propOf(t, schema, "tenant")["x-mcp-header"]; got != "Tenant" {
		t.Errorf("tenant x-mcp-header = %v", got)
	}
	resolveSchema(t, "annotated input", op.MCP.InputSchema)
}

func TestMCPHeaderAnnotationRejectsWhatTheSDKRefuses(t *testing.T) {
	for name, spec := range map[string]string{
		"array value": headerSpec("{type: array, items: {type: string}}"),
		"nullable":    headerSpec("{type: [string, \"null\"]}"),
		"bad name":    strings.Replace(headerSpec("{type: string}"), "x-bartolo-mcp-header: Region", `x-bartolo-mcp-header: "Re gion"`, 1),
		"duplicate":   strings.Replace(headerSpec("{type: string}"), "x-bartolo-mcp-header: Tenant", "x-bartolo-mcp-header: region", 1),
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("expected generation to fail")
				}
			}()
			ProcessAPI("t", loadTestSpec(t, spec))
		})
	}
}

func intPtr(n int) *int    { return &n }
func boolPtr(b bool) *bool { return &b }

func fullMCPConfig() *MCPConfig {
	return &MCPConfig{
		Title:            "Acme",
		Description:      "Acme's API as MCP tools.",
		WebsiteURL:       "https://acme.example",
		Icons:            []MCPIconConfig{{Src: "https://acme.example/icon.png", MIMEType: "image/png", Sizes: []string{"48x48"}}},
		ListTTLSeconds:   intPtr(120),
		TraceContext:     boolPtr(false),
		ProtocolVersions: []string{"2026-07-28", "2025-11-25"},
		ServerCard:       &MCPServerCardConfig{Name: "example.acme/api", Endpoint: "/v2/mcp", Repository: "https://github.com/acme/mcp"},
	}
}

func TestMCPConfigCheck(t *testing.T) {
	if err := fullMCPConfig().check(); err != nil {
		t.Fatalf("a valid config was refused: %v", err)
	}
	var none *MCPConfig
	if err := none.check(); err != nil {
		t.Errorf("no mcp block must be valid: %v", err)
	}
	for name, mutate := range map[string]func(*MCPConfig){
		"unknown protocol version": func(c *MCPConfig) { c.ProtocolVersions = []string{"1999-01-01"} },
		"negative TTL":             func(c *MCPConfig) { c.ListTTLSeconds = intPtr(-1) },
		"card without description": func(c *MCPConfig) { c.Description = "" },
		"card name not namespaced": func(c *MCPConfig) { c.ServerCard.Name = "acme" },
		"relative website":         func(c *MCPConfig) { c.WebsiteURL = "acme.example" },
		"icon not a URL":           func(c *MCPConfig) { c.Icons[0].Src = "icon.png" },
	} {
		c := fullMCPConfig()
		mutate(c)
		if err := c.check(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// Every option the mcp block can produce must render as valid Go.
func TestMCPConfigRendersValidGo(t *testing.T) {
	api := ProcessAPI("acme", loadTestSpec(t, headerSpec("{type: string}")))
	api.MCPConfig = fullMCPConfig()
	src := renderCommandTemplate("templates/generated_mcp.tmpl", buildMCPTemplateData(api))
	if _, err := parser.ParseFile(token.NewFileSet(), "mcp_tools.go", src, 0); err != nil {
		t.Fatalf("generated mcp_tools.go does not parse: %v\n%s", err, src)
	}
	for _, want := range []string{
		`mcpserver.SetInstructions(mcpInstructions)`,
		`mcpserver.SetListTTL(120 * time.Second)`,
		`mcpserver.SetTraceContext(false)`,
		`mcpserver.SetProtocolVersions([]string{"2026-07-28", "2025-11-25"}...)`,
		`mcpserver.PublishServerCard(mcpserver.ServerCard{Name: "example.acme/api", Endpoint: "/v2/mcp"`,
		`"time"`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated source lacks %s", want)
		}
	}
}
