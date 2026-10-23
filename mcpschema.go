package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// Schema budgets. Tool schemas are read by the model on every turn, so nesting
// and prose are bounded: below the depth limit a node keeps only its type (and,
// in input schemas, a short description), which also ends $ref recursion.
const (
	// maxInputSchemaDepth counts levels below a tool's top-level property.
	maxInputSchemaDepth = 6
	// maxOutputSchemaDepth counts levels below the response root.
	maxOutputSchemaDepth = 4
	// maxPropertyDescription bounds a top-level input property's description.
	maxPropertyDescription = 600
	// maxNestedDescription bounds a nested input description, which is also
	// cut to its first sentence.
	maxNestedDescription = 200
)

// sentenceEnd matches the end of a description's first sentence: terminal
// punctuation followed by a capitalized word, a newline, or the end.
var sentenceEnd = regexp.MustCompile(`[.!?](\s+\p{Lu}|\s*\n|\s*$)`)

// maxMCPDescription bounds the tool description length (LLM context hygiene).
const maxMCPDescription = 1024

// MCPArgRoute tells the runtime where a single top-level input-schema property
// goes when it rebuilds the API call. It mirrors mcpserver.ArgRoute.
type MCPArgRoute struct {
	Prop     string // property name in the tool input schema
	WireName string // OpenAPI param name / body property name; "" for a whole body
	In       string // "path" | "query" | "header" | "body"
	Flag     string // CLI flag (viper key) the client func reads an optional param from
	Required bool   // a required param is a positional argument of the client func
}

// MCPTool is the generator-side intermediate representation of a single MCP
// tool derived from an OpenAPI operation. It is attached to Operation.MCP and
// consumed by templates/generated_mcp.tmpl.
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
	// as {"result": S}; the runtime wraps the decoded value the same way so
	// structuredContent stays a JSON object.
	WrapResult     bool
	Excluded       bool
	ExcludedReason string
}

// schemaMode selects which side of a tool call a converted schema describes.
type schemaMode int

const (
	// schemaInput describes tool arguments. readOnly properties are dropped and
	// validation keywords are kept: they guide the model, and the server
	// validates arguments against them before calling the API.
	schemaInput schemaMode = iota
	// schemaOutput describes structuredContent. Clients validate results
	// against it and fail the whole call on a mismatch — after the API call has
	// already happened — so it describes shape only. writeOnly properties are
	// dropped, required/enum/format and the numeric and length bounds are
	// omitted, and nested values may be null: those are exactly where real APIs
	// drift from their specs.
	schemaOutput
)

// schemaConverter turns dereferenced OpenAPI schemas into JSON Schema 2020-12.
// visited is the path-scoped cycle guard: kin-openapi shares the *Schema value
// across every $ref site (each site gets its own *SchemaRef), so tracking
// *Schema detects cycles, and removing it on unwind keeps diamond-shaped reuse
// from reading as one.
type schemaConverter struct {
	mode    schemaMode
	visited map[*openapi3.Schema]bool
}

// convertSchema converts a (dereferenced) OpenAPI schema into a JSON Schema
// 2020-12 fragment for the given mode. A nil/absent schema yields {}
// (permissive). It never fails.
func convertSchema(ref *openapi3.SchemaRef, mode schemaMode) map[string]any {
	c := &schemaConverter{mode: mode, visited: map[*openapi3.Schema]bool{}}
	return c.convert(ref, 0)
}

func (c *schemaConverter) convert(ref *openapi3.SchemaRef, depth int) map[string]any {
	if ref == nil || ref.Value == nil {
		return map[string]any{}
	}
	s := ref.Value
	if depth >= c.maxDepth() || c.visited[s] {
		return c.truncated(s, depth)
	}
	c.visited[s] = true
	defer delete(c.visited, s)

	out := map[string]any{}
	if t := schemaType(s); t != nil {
		out["type"] = t
	}
	c.annotate(out, s, depth)
	if c.mode == schemaInput {
		constrainSchema(out, s)
	}
	if s.Items != nil {
		out["items"] = c.convertNested(s.Items, depth)
	}
	c.convertObject(out, s, depth)
	c.convertCompositions(out, s, depth)
	return out
}

// convertNested converts a property, item, or additionalProperties schema.
// Output schemas let every nested value be null; see schemaOutput.
func (c *schemaConverter) convertNested(ref *openapi3.SchemaRef, depth int) map[string]any {
	child := c.convert(ref, depth+1)
	if c.mode == schemaOutput {
		allowNull(child)
	}
	return child
}

func (c *schemaConverter) convertObject(out map[string]any, s *openapi3.Schema, depth int) {
	if len(s.Properties) > 0 {
		props := map[string]any{}
		for name, pref := range s.Properties {
			if c.keepProperty(pref) {
				props[name] = c.convertNested(pref, depth)
			}
		}
		out["properties"] = props
		if req := c.requiredProperties(s); c.mode == schemaInput && len(req) > 0 {
			out["required"] = req
		}
	}

	switch ap := s.AdditionalProperties; {
	case ap.Schema != nil:
		out["additionalProperties"] = c.convertNested(ap.Schema, depth)
	case ap.Has != nil && !*ap.Has && c.mode == schemaInput:
		out["additionalProperties"] = false
	}
}

// keepProperty drops readOnly properties from input schemas and writeOnly
// properties from output schemas.
func (c *schemaConverter) keepProperty(ref *openapi3.SchemaRef) bool {
	if ref == nil || ref.Value == nil {
		return false
	}
	if c.mode == schemaInput {
		return !ref.Value.ReadOnly
	}
	return !ref.Value.WriteOnly
}

// requiredProperties is s.Required minus the properties keepProperty drops.
// Required names with no matching property are kept.
func (c *schemaConverter) requiredProperties(s *openapi3.Schema) []string {
	req := make([]string, 0, len(s.Required))
	for _, name := range s.Required {
		if pref, ok := s.Properties[name]; ok && pref != nil && !c.keepProperty(pref) {
			continue
		}
		req = append(req, name)
	}
	return req
}

// convertCompositions maps allOf through and folds oneOf into anyOf. OpenAPI
// unions rarely make their branches mutually exclusive (the discriminator that
// would is dropped), so oneOf rejects any value matching two branches; several
// LLM providers also accept anyOf but not oneOf in tool schemas.
func (c *schemaConverter) convertCompositions(out map[string]any, s *openapi3.Schema, depth int) {
	union := append(append(openapi3.SchemaRefs{}, s.OneOf...), s.AnyOf...)
	if len(union) > 0 {
		out["anyOf"] = c.convertAll(union, depth)
	}
	if len(s.AllOf) > 0 {
		out["allOf"] = c.convertAll(s.AllOf, depth)
	}
}

func (c *schemaConverter) convertAll(branches openapi3.SchemaRefs, depth int) []any {
	out := make([]any, 0, len(branches))
	for _, br := range branches {
		out = append(out, c.convert(br, depth+1))
	}
	return out
}

func (c *schemaConverter) maxDepth() int {
	if c.mode == schemaOutput {
		return maxOutputSchemaDepth
	}
	return maxInputSchemaDepth
}

// truncated stands in for a schema cut off by a $ref cycle or the depth limit:
// its own type and description, with everything below left open.
func (c *schemaConverter) truncated(s *openapi3.Schema, depth int) map[string]any {
	out := map[string]any{}
	if t := schemaType(s); t != nil {
		out["type"] = t
	}
	if desc := c.description(s.Description, depth); desc != "" {
		out["description"] = desc
	}
	return out
}

// description applies the description budget: none in output schemas, which
// clients validate rather than show the model; up to maxPropertyDescription on
// a top-level input property; the first sentence, capped, below that.
func (c *schemaConverter) description(desc string, depth int) string {
	switch {
	case c.mode == schemaOutput:
		return ""
	case depth == 0:
		return truncateMCPDescription(desc, maxPropertyDescription)
	}
	return firstSentence(desc, maxNestedDescription)
}

// firstSentence is desc up to the end of its first sentence, capped at max
// runes on a word boundary.
func firstSentence(desc string, max int) string {
	desc = strings.TrimSpace(desc)
	if loc := sentenceEnd.FindStringIndex(desc); loc != nil {
		desc = desc[:loc[0]+1]
	}
	return truncateMCPDescription(desc, max)
}

// schemaType renders the JSON Schema "type", folding OpenAPI 3.0 nullable into
// a type array (3.1 type arrays pass through). nil when untyped.
func schemaType(s *openapi3.Schema) any {
	types := s.Type.Slice()
	if s.Nullable && len(types) > 0 && !slices.Contains(types, "null") {
		types = append(slices.Clone(types), "null")
	}
	switch len(types) {
	case 0:
		return nil
	case 1:
		return types[0]
	}
	out := make([]any, len(types))
	for i, t := range types {
		out[i] = t
	}
	return out
}

// allowNull widens a converted schema's type to also accept null.
func allowNull(schema map[string]any) {
	switch t := schema["type"].(type) {
	case string:
		if t != "null" {
			schema["type"] = []any{t, "null"}
		}
	case []any:
		if !slices.Contains(t, any("null")) {
			schema["type"] = append(t, "null")
		}
	}
}

// annotate copies the keywords that describe a value without constraining it,
// within the description budget. Input schemas keep a default and, on a
// top-level property, one example under "examples"; output schemas keep
// neither.
func (c *schemaConverter) annotate(out map[string]any, s *openapi3.Schema, depth int) {
	if desc := c.description(s.Description, depth); desc != "" {
		out["description"] = desc
	}
	if c.mode == schemaOutput {
		return
	}
	if s.Default != nil {
		out["default"] = s.Default
	}
	if s.Example != nil && depth == 0 {
		out["examples"] = []any{s.Example}
	}
}

// constrainSchema copies the validation keywords for input schemas.
func constrainSchema(out map[string]any, s *openapi3.Schema) {
	if s.Format != "" {
		out["format"] = s.Format
	}
	if len(s.Enum) > 0 {
		enum := s.Enum
		if s.Nullable && !slices.Contains(enum, nil) {
			enum = append(slices.Clone(enum), nil)
		}
		out["enum"] = enum
	}
	if s.Const != nil {
		out["const"] = s.Const
	}
	constrainNumber(out, s)
	constrainSize(out, s)
	// The server compiles patterns with Go's RE2 engine, so an ECMA-262-only
	// construct (lookaround, backreferences) would stop it from starting.
	if s.Pattern != "" {
		if _, err := regexp.Compile(s.Pattern); err == nil {
			out["pattern"] = s.Pattern
		}
	}
}

// constrainNumber maps the numeric bounds, rewriting OpenAPI 3.0's boolean
// exclusiveMinimum/exclusiveMaximum into the 2020-12 numeric form.
func constrainNumber(out map[string]any, s *openapi3.Schema) {
	setBound(out, "minimum", "exclusiveMinimum", s.Min, s.ExclusiveMin)
	setBound(out, "maximum", "exclusiveMaximum", s.Max, s.ExclusiveMax)
	if s.MultipleOf != nil {
		out["multipleOf"] = *s.MultipleOf
	}
}

func setBound(out map[string]any, inclusiveKey, exclusiveKey string, bound *float64, exclusive openapi3.ExclusiveBound) {
	switch {
	case exclusive.Value != nil:
		out[exclusiveKey] = *exclusive.Value
		if bound != nil {
			out[inclusiveKey] = *bound
		}
	case bound != nil && exclusive.IsTrue():
		out[exclusiveKey] = *bound
	case bound != nil:
		out[inclusiveKey] = *bound
	}
}

// constrainSize maps the length, item-count, and property-count bounds.
func constrainSize(out map[string]any, s *openapi3.Schema) {
	setMin(out, "minLength", s.MinLength)
	setMax(out, "maxLength", s.MaxLength)
	setMin(out, "minItems", s.MinItems)
	setMax(out, "maxItems", s.MaxItems)
	setMin(out, "minProperties", s.MinProps)
	setMax(out, "maxProperties", s.MaxProps)
	if s.UniqueItems {
		out["uniqueItems"] = true
	}
}

func setMin(out map[string]any, key string, value uint64) {
	if value > 0 {
		out[key] = value
	}
}

func setMax(out map[string]any, key string, value *uint64) {
	if value != nil {
		out[key] = *value
	}
}

// snakeName slugs a name and converts dashes to underscores (snake_case).
func snakeName(value string) string {
	return strings.ReplaceAll(slug(value), "-", "_")
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
// skipping shared-ignored ones. Per OpenAPI semantics an operation-level
// parameter OVERRIDES a path-level parameter with the same (name, in), so
// shadowed path-level entries are dropped. Order: surviving path params first,
// then op params.
func mcpParams(item *openapi3.PathItem, op *openapi3.Operation) openapi3.Parameters {
	var pathLevel, opLevel openapi3.Parameters
	if item != nil {
		pathLevel = item.Parameters
	}
	if op != nil {
		opLevel = op.Parameters
	}

	overridden := map[paramKey]bool{}
	for _, p := range opLevel {
		if p != nil && p.Value != nil {
			overridden[paramKey{p.Value.In, p.Value.Name}] = true
		}
	}

	out := make(openapi3.Parameters, 0, len(pathLevel)+len(opLevel))
	for _, p := range pathLevel {
		if usableParam(p) && !overridden[paramKey{p.Value.In, p.Value.Name}] {
			out = append(out, p)
		}
	}
	for _, p := range opLevel {
		if usableParam(p) {
			out = append(out, p)
		}
	}
	return out
}

// paramKey identifies an OpenAPI parameter: names are unique per location.
type paramKey struct{ in, name string }

func usableParam(p *openapi3.ParameterRef) bool {
	return p != nil && p.Value != nil && !sharedIgnored(p.Value.Extensions)
}

// firstJSONSchema returns the schema of the first JSON media type, in sorted
// media-type order for determinism.
func firstJSONSchema(content openapi3.Content) *openapi3.SchemaRef {
	for _, mt := range slices.Sorted(maps.Keys(content)) {
		if item := content[mt]; strings.Contains(mt, "json") && item != nil && item.Schema != nil {
			return item.Schema
		}
	}
	return nil
}

// mcpRequestBodySchema returns the JSON request-body schema plus whether the
// body is required. A JSON body declared without a schema yields an empty
// (permissive) schema, so the tool can still send one.
func mcpRequestBodySchema(op *openapi3.Operation) (*openapi3.SchemaRef, bool) {
	if op.RequestBody == nil || op.RequestBody.Value == nil {
		return nil, false
	}
	body := op.RequestBody.Value
	if schema := firstJSONSchema(body.Content); schema != nil {
		return schema, body.Required
	}
	for mt := range body.Content {
		if strings.Contains(mt, "json") {
			return openapi3.NewSchemaRef("", &openapi3.Schema{}), body.Required
		}
	}
	return nil, false
}

// successResponses returns the operation's 2xx responses: exact codes in
// ascending order, then a "2XX" range entry.
func successResponses(op *openapi3.Operation) []*openapi3.Response {
	if op.Responses == nil {
		return nil
	}
	codes := make([]string, 0)
	for code := range op.Responses.Map() {
		if isSuccessCode(code) {
			codes = append(codes, code)
		}
	}
	// Digits sort before "X", so exact codes precede the 2XX range.
	sort.Strings(codes)

	out := make([]*openapi3.Response, 0, len(codes))
	for _, code := range codes {
		if resp := op.Responses.Value(code); resp != nil && resp.Value != nil {
			out = append(out, resp.Value)
		}
	}
	return out
}

func isSuccessCode(code string) bool {
	if strings.EqualFold(code, "2XX") {
		return true
	}
	num, err := strconv.Atoi(code)
	return err == nil && num >= 200 && num < 300
}

// mcpResponseSchema returns the first 2xx JSON response schema.
func mcpResponseSchema(op *openapi3.Operation) *openapi3.SchemaRef {
	for _, resp := range successResponses(op) {
		if schema := firstJSONSchema(resp.Content); schema != nil {
			return schema
		}
	}
	return nil
}

// mcpIsSSEOnly reports whether every 2xx response content type is
// text/event-stream (so the op cannot be bridged as a JSON tool).
func mcpIsSSEOnly(op *openapi3.Operation) bool {
	sse := false
	for _, resp := range successResponses(op) {
		for mt := range resp.Content {
			if !strings.Contains(mt, "event-stream") {
				return false
			}
			sse = true
		}
	}
	return sse
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
// into top-level tool properties (an object with declared properties, possibly
// via allOf) versus carried under a single "body" property (array, primitive,
// pure union, or a free-form map with no declared properties).
func mcpBodyIsObject(s *openapi3.Schema) bool {
	if s == nil {
		return false
	}
	if len(s.Properties) > 0 {
		return true
	}
	if len(s.AllOf) == 0 {
		return false
	}
	merged := mergeAllOf(s)
	return merged != nil && len(merged.Properties) > 0
}

// mcpExclusionReason names why an operation cannot be an MCP tool, or "".
// Capability-based exclusions (SSE-only, multipart/binary) are checked before
// the deprecated policy flag so an op that is both a streaming endpoint and
// deprecated is reported by its blocking capability.
func mcpExclusionReason(op *openapi3.Operation) string {
	switch {
	case mcpIgnored(op.Extensions):
		return "x-mcp-ignore"
	case mcpIsSSEOnly(op):
		return "sse-only"
	case mcpIsMultipartOnly(op):
		return "multipart/binary request body"
	case op.Deprecated:
		return "deprecated"
	}
	return ""
}

// mcpMethodHints maps the HTTP method onto the MCP tool annotations. A method
// that changes or removes existing state (PUT, PATCH, DELETE) is destructive,
// since clients use destructiveHint to decide when to ask before calling.
func mcpMethodHints(method string) (readOnly, destructive, idempotent bool) {
	switch strings.ToUpper(method) {
	case "GET", "HEAD":
		return true, false, true
	case "PUT", "DELETE":
		return false, true, true
	case "PATCH":
		return false, true, false
	}
	return false, false, false
}

// mcpToolName is <group>_<leaf> in snake_case (the leaf alone when ungrouped),
// deduplicated later by assignMCPToolNames.
func mcpToolName(o *Operation) string {
	name := snakeName(o.LeafName)
	if o.Group != nil {
		group := snakeName(o.Group.CLIName)
		if group == "mcp" {
			// Reserve the "mcp" root subcommand name.
			group = "mcp_group"
		}
		if group != "" {
			name = group + "_" + name
		}
	}
	return sanitizeToolName(name)
}

// mcpToolDescription is the summary plus the description (or its
// x-bartolo-description override), truncated for LLM context hygiene.
func mcpToolDescription(op *openapi3.Operation) string {
	body := op.Description
	if override := extString(op.Extensions, extKeyDescription); override != "" {
		body = override
	}
	parts := make([]string, 0, 2)
	for _, part := range []string{op.Summary, body} {
		if part = strings.TrimSpace(part); part != "" {
			parts = append(parts, part)
		}
	}
	return truncateMCPDescription(strings.Join(parts, "\n\n"), maxMCPDescription)
}

// buildMCPTool assembles the MCPTool IR for a single operation. Naming
// collisions are resolved later in assignMCPToolNames (deterministic pass).
// Excluded operations still get a full IR: they are reported, not emitted.
func buildMCPTool(o *Operation, op *openapi3.Operation, item *openapi3.PathItem) *MCPTool {
	tool := &MCPTool{
		Name:           mcpToolName(o),
		Description:    mcpToolDescription(op),
		ExcludedReason: mcpExclusionReason(op),
	}
	tool.Excluded = tool.ExcludedReason != ""
	tool.Title = firstNonEmpty(op.Summary, displayNameFromSlug(o.LeafName), tool.Name)
	tool.ReadOnly, tool.Destructive, tool.Idempotent = mcpMethodHints(o.Method)

	input := newMCPInputBuilder(o)
	input.addParams(mcpParams(item, op))
	input.addBody(op)
	tool.InputSchema, tool.Routes = input.build()

	tool.OutputSchema, tool.WrapResult = mcpOutputSchema(op)
	return tool
}

// mcpInputBuilder accumulates a tool's input schema properties and the routes
// that send each one to its place in the API call.
type mcpInputBuilder struct {
	flags      map[paramKey]string // CLI flag name of each optional param
	properties map[string]any
	required   []string
	routes     []MCPArgRoute
}

func newMCPInputBuilder(o *Operation) *mcpInputBuilder {
	flags := map[paramKey]string{}
	for _, p := range o.OptionalParams {
		flags[paramKey{p.In, p.Name}] = p.CLIName
	}
	return &mcpInputBuilder{flags: flags, properties: map[string]any{}}
}

// claim returns a free property name: base, else prefix_base, else a numbered
// variant. Parameters claim names first, so a body property that collides with
// one becomes body_<name>.
func (b *mcpInputBuilder) claim(base, prefix string) string {
	if base == "" {
		return ""
	}
	name := base
	if _, taken := b.properties[name]; taken {
		name = prefix + "_" + base
	}
	for i := 2; ; i++ {
		if _, taken := b.properties[name]; !taken {
			return name
		}
		name = fmt.Sprintf("%s_%s_%d", prefix, base, i)
	}
}

func (b *mcpInputBuilder) add(schema map[string]any, required bool, route MCPArgRoute) {
	b.properties[route.Prop] = schema
	if required {
		b.required = append(b.required, route.Prop)
	}
	b.routes = append(b.routes, route)
}

// addParams adds path/query/header parameters. Cookie parameters are skipped:
// the generated client funcs cannot send them. An optional parameter is
// skipped too when it has no CLI flag, since the client func reads optional
// values by flag name.
func (b *mcpInputBuilder) addParams(params openapi3.Parameters) {
	for _, p := range params {
		pv := p.Value
		required := pv.In == "path" || pv.Required
		flag := b.flags[paramKey{pv.In, pv.Name}]
		if pv.In == "cookie" || (!required && flag == "") {
			continue
		}
		prop := b.claim(snakeName(pv.Name), pv.In)
		if prop == "" {
			continue
		}
		schema := convertSchema(pv.Schema, schemaInput)
		if _, ok := schema["description"]; !ok && pv.Description != "" {
			schema["description"] = truncateMCPDescription(pv.Description, maxPropertyDescription)
		}
		b.add(schema, required, MCPArgRoute{Prop: prop, WireName: pv.Name, In: pv.In, Flag: flag, Required: required})
	}
}

// addBody adds the JSON request body: an object body's properties become
// top-level properties; anything else travels whole under "body".
func (b *mcpInputBuilder) addBody(op *openapi3.Operation) {
	ref, bodyRequired := mcpRequestBodySchema(op)
	if ref == nil || ref.Value == nil {
		return
	}
	if !mcpBodyIsObject(ref.Value) {
		prop := b.claim("body", "body")
		b.add(convertSchema(ref, schemaInput), bodyRequired, MCPArgRoute{Prop: prop, In: "body"})
		return
	}

	merged := mergeAllOf(ref.Value)
	for _, name := range slices.Sorted(maps.Keys(merged.Properties)) {
		pref := merged.Properties[name]
		// readOnly body props never appear in an input schema.
		if pref == nil || pref.Value == nil || pref.Value.ReadOnly {
			continue
		}
		prop := b.claim(snakeName(name), "body")
		if prop == "" {
			continue
		}
		b.add(convertSchema(pref, schemaInput), slices.Contains(merged.Required, name), MCPArgRoute{Prop: prop, WireName: name, In: "body"})
	}
}

// build renders the input schema and the routes, both in property order.
func (b *mcpInputBuilder) build() (string, []MCPArgRoute) {
	sort.Slice(b.routes, func(i, j int) bool { return b.routes[i].Prop < b.routes[j].Prop })
	sort.Strings(b.required)

	schema := map[string]any{
		"type":                 "object",
		"properties":           b.properties,
		"additionalProperties": false,
	}
	if len(b.required) > 0 {
		schema["required"] = b.required
	}
	return mustCompactJSON(schema), b.routes
}

// mcpOutputSchema converts the first 2xx JSON response schema. structuredContent
// must be a JSON object for clients on protocol versions before 2026-07-28, so a
// non-object body is wrapped as {"result": ...}; the bool reports the wrap.
func mcpOutputSchema(op *openapi3.Operation) (string, bool) {
	ref := mcpResponseSchema(op)
	if ref == nil || ref.Value == nil {
		return "", false
	}
	schema := convertSchema(ref, schemaOutput)
	if schema["type"] == "object" {
		return mustCompactJSON(schema), false
	}
	allowNull(schema)
	return mustCompactJSON(map[string]any{
		"type":       "object",
		"properties": map[string]any{"result": schema},
		"required":   []string{"result"},
	}), true
}

// invalidToolNameChars matches what MCP tool names may not contain.
var invalidToolNameChars = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// sanitizeToolName enforces ^[a-zA-Z0-9_-]{1,128}$.
func sanitizeToolName(name string) string {
	out := invalidToolNameChars.ReplaceAllString(name, "_")
	if out == "" {
		return "tool"
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

// MCPToolTemplate is one included operation plus its group slug (used as
// ToolDef.Group for the runtime --groups filter).
type MCPToolTemplate struct {
	Op    *Operation
	Group string
}

// buildMCPTemplateData assembles the render model for the MCP tools file.
func buildMCPTemplateData(api *OpenAPI) *MCPTemplateData {
	data := &MCPTemplateData{API: api}
	for _, op := range api.includedMCPOperations() {
		group := ""
		if op.Group != nil {
			group = op.Group.CLIName
		}
		data.Tools = append(data.Tools, &MCPToolTemplate{Op: op, Group: group})
	}
	return data
}
