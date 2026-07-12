package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// maxSchemaDepth bounds $ref inlining recursion. On overflow we emit a
// permissive {"type":"object"} node so generation never fails or loops.
const maxSchemaDepth = 8

// maxMCPDescription bounds the tool description length (LLM context hygiene).
const maxMCPDescription = 1024

// MCPArgRoute tells the generated handler closure where a single top-level
// input-schema property must be placed on the outgoing HTTP request.
type MCPArgRoute struct {
	Prop     string // property name in the tool input schema
	WireName string // original OpenAPI param name / body property name
	In       string // "path" | "query" | "header" | "body"
	Type     string // "string" | "int64" | "float64" | "bool" | "json"
}

// MCPTool is the generator-side intermediate representation of a single MCP
// tool derived from an OpenAPI operation. It is attached to Operation.MCP and
// consumed by the Phase 3 template.
type MCPTool struct {
	Name         string
	Title        string
	Description  string
	InputSchema  string // compact JSON, always a JSON object for included tools
	OutputSchema string // compact JSON, "" when absent
	Routes       []MCPArgRoute
	ReadOnly     bool
	Destructive  bool
	Idempotent   bool
	// WrapResult records that the output schema wrapped a non-object 2xx body
	// as {"result": S}; the Phase 3 closure must wrap the decoded value the
	// same way so structuredContent stays a JSON object.
	WrapResult     bool
	Excluded       bool
	ExcludedReason string
}

// convertSchema converts a (dereferenced) OpenAPI schema into a JSON Schema
// 2020-12 fragment following the plan's conversion table. forInput strips
// readOnly properties; !forInput strips writeOnly properties. A nil/absent
// schema yields {} (permissive). It never fails.
func convertSchema(ref *openapi3.SchemaRef, forInput bool) map[string]any {
	visited := map[*openapi3.Schema]bool{}
	return convertSchemaRec(ref, forInput, 0, visited)
}

func convertSchemaRec(ref *openapi3.SchemaRef, forInput bool, depth int, visited map[*openapi3.Schema]bool) map[string]any {
	if ref == nil || ref.Value == nil {
		// Nil/absent schema is permissive.
		return map[string]any{}
	}

	s := ref.Value

	// Path-scoped cycle guard on the *openapi3.Schema value pointer, plus a
	// hard depth limit. kin-openapi shares the .Value pointer across every
	// $ref site so tracking *Schema (not *SchemaRef) is what detects cycles.
	if depth >= maxSchemaDepth || visited[s] {
		out := map[string]any{"type": "object"}
		if s.Description != "" {
			out["description"] = s.Description
		}
		return out
	}
	visited[s] = true
	defer delete(visited, s)

	out := map[string]any{}

	// Type (with nullable / 3.1 type-array handling).
	types := append([]string{}, s.Type.Slice()...)
	hasNull := false
	for _, t := range types {
		if t == "null" {
			hasNull = true
		}
	}
	if s.Nullable && !hasNull && len(types) > 0 {
		types = append(types, "null")
	}
	switch len(types) {
	case 1:
		out["type"] = types[0]
	default:
		if len(types) > 1 {
			arr := make([]any, 0, len(types))
			for _, t := range types {
				arr = append(arr, t)
			}
			out["type"] = arr
		}
	}

	// Simple carried-over keywords.
	if s.Description != "" {
		out["description"] = s.Description
	}
	if s.Format != "" {
		out["format"] = s.Format
	}
	if len(s.Enum) > 0 {
		out["enum"] = s.Enum
	}
	if s.Default != nil {
		out["default"] = s.Default
	}
	if s.Min != nil {
		out["minimum"] = *s.Min
	}
	if s.Max != nil {
		out["maximum"] = *s.Max
	}
	// Keep at most one example, under "examples" (drop "example"/xml/discriminator/x-*).
	if s.Example != nil {
		out["examples"] = []any{s.Example}
	}

	// Arrays.
	if s.Items != nil {
		out["items"] = convertSchemaRec(s.Items, forInput, depth+1, visited)
	}

	// Objects.
	if len(s.Properties) > 0 {
		props := map[string]any{}
		for name, pref := range s.Properties {
			if pref == nil || pref.Value == nil {
				continue
			}
			if forInput && pref.Value.ReadOnly {
				continue
			}
			if !forInput && pref.Value.WriteOnly {
				continue
			}
			props[name] = convertSchemaRec(pref, forInput, depth+1, visited)
		}
		out["properties"] = props

		if len(s.Required) > 0 {
			req := make([]string, 0, len(s.Required))
			for _, r := range s.Required {
				if pref, ok := s.Properties[r]; ok && pref != nil && pref.Value != nil {
					if forInput && pref.Value.ReadOnly {
						continue
					}
					if !forInput && pref.Value.WriteOnly {
						continue
					}
				}
				req = append(req, r)
			}
			if len(req) > 0 {
				out["required"] = req
			}
		}
	}

	// Compositions.
	if len(s.OneOf) > 0 {
		out["oneOf"] = convertBranches(s.OneOf, forInput, depth+1, visited)
	}
	if len(s.AnyOf) > 0 {
		out["anyOf"] = convertBranches(s.AnyOf, forInput, depth+1, visited)
	}
	if len(s.AllOf) > 0 {
		out["allOf"] = convertBranches(s.AllOf, forInput, depth+1, visited)
	}

	return out
}

func convertBranches(branches openapi3.SchemaRefs, forInput bool, depth int, visited map[*openapi3.Schema]bool) []any {
	out := make([]any, 0, len(branches))
	for _, br := range branches {
		out = append(out, convertSchemaRec(br, forInput, depth, visited))
	}
	return out
}

// snakeName slugs a name and converts dashes to underscores (snake_case).
func snakeName(value string) string {
	return strings.ReplaceAll(slug(value), "-", "_")
}

// mcpRouteType maps a schema to the closure-friendly value kind used by the
// routing table: string | int64 | float64 | bool | json.
func mcpRouteType(ref *openapi3.SchemaRef) string {
	if ref == nil || ref.Value == nil {
		return "json"
	}
	eff, _ := effectiveBodySchema(ref.Value)
	if eff == nil || eff.Type == nil {
		return "json"
	}
	switch {
	case eff.Type.Is("string"):
		return "string"
	case eff.Type.Is("integer"):
		return "int64"
	case eff.Type.Is("number"):
		return "float64"
	case eff.Type.Is("boolean"):
		return "bool"
	default:
		return "json"
	}
}

// truncateMCPDescription trims to max runes on a word boundary. The cut is
// rune-safe: multibyte UTF-8 sequences are never split even when no word
// boundary exists near the limit.
func truncateMCPDescription(value string, max int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	cut := string(runes[:max])
	if i := strings.LastIndexAny(cut, " \n\t"); i > 0 {
		cut = cut[:i]
	}
	return strings.TrimSpace(cut)
}

// mcpParams returns the combined path-level + operation-level parameters,
// skipping x-cli-ignore'd ones. Per OpenAPI semantics an operation-level
// parameter OVERRIDES a path-level parameter with the same (name, in), so
// shadowed path-level entries are dropped. Order: surviving path params
// first, then op params.
func mcpParams(item *openapi3.PathItem, op *openapi3.Operation) openapi3.Parameters {
	type paramKey struct{ name, in string }

	opLevel := map[paramKey]bool{}
	if op != nil {
		for _, p := range op.Parameters {
			if p == nil || p.Value == nil {
				continue
			}
			opLevel[paramKey{p.Value.Name, p.Value.In}] = true
		}
	}

	var all openapi3.Parameters
	if item != nil {
		for _, p := range item.Parameters {
			if p == nil || p.Value == nil {
				continue
			}
			if opLevel[paramKey{p.Value.Name, p.Value.In}] {
				// Overridden at the operation level.
				continue
			}
			all = append(all, p)
		}
	}
	if op != nil {
		all = append(all, op.Parameters...)
	}

	out := make(openapi3.Parameters, 0, len(all))
	for _, p := range all {
		if p == nil || p.Value == nil {
			continue
		}
		if sharedIgnored(p.Value.Extensions) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// mcpRequestBodySchema returns the JSON request-body schema (preferring a
// media type containing "json") plus whether the body is required.
func mcpRequestBodySchema(op *openapi3.Operation) (*openapi3.SchemaRef, bool) {
	if op.RequestBody == nil || op.RequestBody.Value == nil {
		return nil, false
	}
	content := op.RequestBody.Value.Content
	mts := make([]string, 0, len(content))
	for mt := range content {
		mts = append(mts, mt)
	}
	sort.Strings(mts)
	for _, mt := range mts {
		if strings.Contains(mt, "json") {
			item := content[mt]
			if item != nil && item.Schema != nil {
				return item.Schema, op.RequestBody.Value.Required
			}
		}
	}
	return nil, false
}

// mcpResponseSchema returns the first 2xx JSON response schema, iterating
// status codes in ascending order for determinism.
func mcpResponseSchema(op *openapi3.Operation) *openapi3.SchemaRef {
	if op.Responses == nil {
		return nil
	}
	codes := make([]string, 0)
	for code := range op.Responses.Map() {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	for _, code := range codes {
		num, err := strconv.Atoi(code)
		if err != nil || num < 200 || num >= 300 {
			continue
		}
		resp := op.Responses.Map()[code]
		if resp == nil || resp.Value == nil {
			continue
		}
		mts := make([]string, 0, len(resp.Value.Content))
		for mt := range resp.Value.Content {
			mts = append(mts, mt)
		}
		sort.Strings(mts)
		for _, mt := range mts {
			if strings.Contains(mt, "json") {
				item := resp.Value.Content[mt]
				if item != nil && item.Schema != nil {
					return item.Schema
				}
			}
		}
	}
	return nil
}

// mcpIsSSEOnly reports whether every 2xx response content type is
// text/event-stream (so the op cannot be bridged as a JSON tool).
func mcpIsSSEOnly(op *openapi3.Operation) bool {
	if op.Responses == nil {
		return false
	}
	hasContent := false
	hasNonSSE := false
	for code, resp := range op.Responses.Map() {
		num, err := strconv.Atoi(code)
		if err != nil || num < 200 || num >= 300 {
			continue
		}
		if resp == nil || resp.Value == nil {
			continue
		}
		for mt := range resp.Value.Content {
			hasContent = true
			if !strings.Contains(mt, "event-stream") {
				hasNonSSE = true
			}
		}
	}
	return hasContent && !hasNonSSE
}

// mcpIsMultipartOnly reports whether every request content type is
// multipart/form-data or application/octet-stream (binary upload only).
func mcpIsMultipartOnly(op *openapi3.Operation) bool {
	if op.RequestBody == nil || op.RequestBody.Value == nil {
		return false
	}
	content := op.RequestBody.Value.Content
	if len(content) == 0 {
		return false
	}
	for mt := range content {
		if !strings.Contains(mt, "multipart/form-data") && !strings.Contains(mt, "octet-stream") {
			return false
		}
	}
	return true
}

// mcpBodyIsObject reports whether the JSON body schema should be flattened
// into top-level tool properties (object / allOf-of-objects) versus wrapped
// under a single "body" property (array / primitive / pure oneOf-anyOf union).
func mcpBodyIsObject(s *openapi3.Schema) bool {
	if s == nil {
		return false
	}
	if s.Type.Is("object") {
		return true
	}
	if len(s.Properties) > 0 {
		return true
	}
	if len(s.AllOf) > 0 {
		merged := mergeAllOf(s)
		return merged != nil && len(merged.Properties) > 0
	}
	return false
}

// buildMCPTool assembles the MCPTool IR for a single operation. Naming
// collisions are resolved later in assignMCPToolNames (deterministic pass).
func buildMCPTool(o *Operation, op *openapi3.Operation, item *openapi3.PathItem) *MCPTool {
	tool := &MCPTool{}

	// --- Exclusions (recorded, but the op still generates a CLI command). ---
	// Capability-based exclusions (SSE-only, multipart/binary) are checked
	// before the deprecated policy flag so an op that is both a streaming
	// endpoint and deprecated is reported by its blocking capability.
	switch {
	case mcpIgnored(op.Extensions):
		tool.Excluded = true
		tool.ExcludedReason = "x-mcp-ignore"
	case mcpIsSSEOnly(op):
		tool.Excluded = true
		tool.ExcludedReason = "sse-only"
	case mcpIsMultipartOnly(op):
		tool.Excluded = true
		tool.ExcludedReason = "multipart/binary request body"
	case op.Deprecated:
		tool.Excluded = true
		tool.ExcludedReason = "deprecated"
	}

	// --- Name (base; deduped later). ---
	leaf := snakeName(o.LeafName)
	name := leaf
	if o.Group != nil {
		group := snakeName(o.Group.CLIName)
		if group == "mcp" {
			// Reserve the "mcp" root subcommand name.
			group = "mcp_group"
		}
		if group != "" {
			name = group + "_" + leaf
		}
	}
	tool.Name = sanitizeToolName(name)

	// --- Title + description. ---
	title := strings.TrimSpace(op.Summary)
	if title == "" {
		title = displayNameFromSlug(o.LeafName)
	}
	if title == "" {
		title = tool.Name
	}
	tool.Title = title

	desc := strings.TrimSpace(op.Summary)
	body := strings.TrimSpace(op.Description)
	if body != "" {
		if desc != "" {
			desc += "\n\n" + body
		} else {
			desc = body
		}
	}
	tool.Description = truncateMCPDescription(desc, maxMCPDescription)

	// --- Annotations by method. ---
	switch strings.ToUpper(o.Method) {
	case "GET", "HEAD":
		tool.ReadOnly = true
		tool.Idempotent = true
	case "PUT":
		tool.Idempotent = true
	case "DELETE":
		tool.Destructive = true
		tool.Idempotent = true
	}

	// --- Input schema + routes. ---
	properties := map[string]any{}
	required := make([]string, 0)
	usedProps := map[string]bool{}
	routes := make([]MCPArgRoute, 0)

	params := mcpParams(item, op)
	for _, p := range params {
		pv := p.Value
		prop := snakeName(pv.Name)
		if prop == "" {
			continue
		}
		propSchema := convertSchema(pv.Schema, true)
		// The parameter's own description outranks (and usually replaces) a
		// missing schema description.
		if pv.Description != "" {
			if _, ok := propSchema["description"]; !ok {
				propSchema["description"] = pv.Description
			}
		}
		properties[prop] = propSchema
		usedProps[prop] = true
		if pv.In == "path" || pv.Required {
			required = append(required, prop)
		}
		routes = append(routes, MCPArgRoute{
			Prop:     prop,
			WireName: pv.Name,
			In:       pv.In,
			Type:     mcpRouteType(pv.Schema),
		})
	}

	if bodyRef, bodyRequired := mcpRequestBodySchema(op); bodyRef != nil && bodyRef.Value != nil {
		bodySchema := bodyRef.Value
		if mcpBodyIsObject(bodySchema) {
			merged := mergeAllOf(bodySchema)
			requiredSet := map[string]bool{}
			if merged != nil {
				for _, r := range merged.Required {
					requiredSet[r] = true
				}
			}
			names := make([]string, 0, len(merged.Properties))
			for propName := range merged.Properties {
				names = append(names, propName)
			}
			sort.Strings(names)
			for _, propName := range names {
				pref := merged.Properties[propName]
				if pref == nil || pref.Value == nil {
					continue
				}
				// readOnly body props never appear in an input schema.
				if pref.Value.ReadOnly {
					continue
				}
				prop := snakeName(propName)
				if prop == "" {
					continue
				}
				if usedProps[prop] {
					// Collision with a param property: prefix the body prop.
					prop = "body_" + prop
				}
				properties[prop] = convertSchema(pref, true)
				usedProps[prop] = true
				if requiredSet[propName] {
					required = append(required, prop)
				}
				routes = append(routes, MCPArgRoute{
					Prop:     prop,
					WireName: propName,
					In:       "body",
					Type:     mcpRouteType(pref),
				})
			}
		} else {
			// Non-object body: single "body" property carrying the schema.
			prop := "body"
			if usedProps[prop] {
				prop = "body_" + prop
			}
			properties[prop] = convertSchema(bodyRef, true)
			usedProps[prop] = true
			if bodyRequired {
				required = append(required, prop)
			}
			routes = append(routes, MCPArgRoute{
				Prop:     prop,
				WireName: "",
				In:       "body",
				Type:     "json",
			})
		}
	}

	sort.Slice(routes, func(i, j int) bool {
		return routes[i].Prop < routes[j].Prop
	})
	sort.Strings(required)
	tool.Routes = routes

	inputSchema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		inputSchema["required"] = required
	}
	tool.InputSchema = mustCompactJSON(inputSchema)

	// --- Output schema. ---
	if respRef := mcpResponseSchema(op); respRef != nil && respRef.Value != nil {
		outSchema := convertSchema(respRef, false)
		topType, _ := outSchema["type"].(string)
		if topType != "object" {
			// structuredContent must be a JSON object: wrap non-object bodies.
			outSchema = map[string]any{
				"type":       "object",
				"properties": map[string]any{"result": outSchema},
				"required":   []string{"result"},
			}
			tool.WrapResult = true
		}
		tool.OutputSchema = mustCompactJSON(outSchema)
	}

	return tool
}

// sanitizeToolName enforces ^[a-zA-Z0-9_-]{1,128}$.
func sanitizeToolName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	out := b.String()
	if out == "" {
		out = "tool"
	}
	if len(out) > 128 {
		out = out[:128]
	}
	return out
}

// assignMCPToolNames resolves tool-name collisions deterministically across
// all included operations, sorted by (path, method), appending _2, _3, ...
func assignMCPToolNames(api *OpenAPI) {
	var ops []*Operation
	ops = append(ops, api.Operations...)
	for _, g := range api.Groups {
		ops = append(ops, g.Operations...)
	}

	included := make([]*Operation, 0, len(ops))
	for _, o := range ops {
		if o.MCP != nil && !o.MCP.Excluded {
			included = append(included, o)
		}
	}

	sort.SliceStable(included, func(i, j int) bool {
		if included[i].Path != included[j].Path {
			return included[i].Path < included[j].Path
		}
		return included[i].Method < included[j].Method
	})

	seen := map[string]bool{}
	counter := map[string]int{}
	for _, o := range included {
		base := o.MCP.Name
		name := base
		for seen[name] {
			counter[base]++
			suffix := fmt.Sprintf("_%d", counter[base]+1)
			// Trim the base so base+suffix stays within the 128-char tool
			// name limit enforced by sanitizeToolName / the MCP spec regex.
			trimmed := base
			if len(trimmed)+len(suffix) > 128 {
				trimmed = trimmed[:128-len(suffix)]
			}
			name = trimmed + suffix
		}
		seen[name] = true
		o.MCP.Name = name
	}
}

func mustCompactJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		// Should never happen for the plain map/slice/scalar values we build.
		return "{}"
	}
	return string(data)
}

// rawJSONLiteral renders s as a Go raw-string literal for embedding in the
// generated mcp_tools.go. Compact JSON schemas never contain newlines but may
// carry backticks inside markdown descriptions, which a raw string literal
// cannot hold — so on any backtick the value is emitted as a concatenation
// (`part` + "`" + `part`), the only way to place a backtick in Go source.
func rawJSONLiteral(s string) string {
	if !strings.Contains(s, "`") {
		return "`" + s + "`"
	}
	parts := strings.Split(s, "`")
	for i := range parts {
		parts[i] = "`" + parts[i] + "`"
	}
	return strings.Join(parts, " + \"`\" + ")
}

// includedMCPTools collects every non-excluded MCP tool across the root
// operations and all groups, preserving a deterministic order (root ops first,
// then groups in order). Used by both HasMCPTools and the template builder.
func (o *OpenAPI) includedMCPOperations() []*Operation {
	out := make([]*Operation, 0)
	for _, op := range o.Operations {
		if op.MCP != nil && !op.MCP.Excluded {
			out = append(out, op)
		}
	}
	for _, g := range o.Groups {
		for _, op := range g.Operations {
			if op.MCP != nil && !op.MCP.Excluded {
				out = append(out, op)
			}
		}
	}
	return out
}

// HasMCPTools reports whether the MCP surface should be emitted: the MCP target
// must be enabled AND at least one operation must be exposed as an MCP tool. The
// generated register.go only mounts the `mcp` command (and the generator only
// emits mcp_tools.go) when this is true.
func (o *OpenAPI) HasMCPTools() bool {
	return o.TargetMCP && len(o.includedMCPOperations()) > 0
}

// MCPInstructions builds the server instructions string surfaced to MCP
// clients: the CLI name, the API title, and short outbound-auth guidance drawn
// from the generated AuthDoc. Kept intentionally brief for LLM context hygiene.
func (o *OpenAPI) MCPInstructions() string {
	name := o.CommandName
	if name == "" {
		name = o.Name
	}
	title := o.Title
	if title == "" {
		title = o.Name
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s exposes the %s as MCP tools.", name, title)

	if o.AuthDoc != nil && o.AuthDoc.Enabled {
		switch {
		case len(o.AuthDoc.EnvVars) > 0:
			fmt.Fprintf(&b, " Authenticate by setting the %s environment variable", o.AuthDoc.EnvVars[0])
			if o.AuthDoc.ProfileCommand != "" {
				fmt.Fprintf(&b, " or running `%s`", o.AuthDoc.ProfileCommand)
			}
			b.WriteString(".")
		case o.AuthDoc.ProfileCommand != "":
			fmt.Fprintf(&b, " Authenticate by running `%s`.", o.AuthDoc.ProfileCommand)
		}
	}

	return b.String()
}

// MCPTemplateData is the render model for templates/generated_mcp.tmpl.
type MCPTemplateData struct {
	API   *OpenAPI
	Tools []*MCPToolTemplate
}

// MCPToolTemplate is one included operation, flattened with the API's public Go
// name (used to reference the generated client func) and its group slug (used
// as ToolDef.Group for the runtime --groups filter).
type MCPToolTemplate struct {
	Op        *Operation
	ApiPublic string
	Group     string
}

// buildMCPTemplateData assembles the render model for the MCP tools file.
func buildMCPTemplateData(api *OpenAPI) *MCPTemplateData {
	data := &MCPTemplateData{API: api}
	for _, op := range api.includedMCPOperations() {
		group := ""
		if op.Group != nil {
			group = op.Group.CLIName
		}
		data.Tools = append(data.Tools, &MCPToolTemplate{
			Op:        op,
			ApiPublic: api.PublicGoName,
			Group:     group,
		})
	}
	return data
}
