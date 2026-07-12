package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/google/jsonschema-go/jsonschema"
)

// resolveSchema round-trips a compact JSON schema string through
// jsonschema.Schema (unmarshal + Resolve), the same path the generated
// mcpserver runtime takes at startup. It fails the test on any error, giving
// the strongest available correctness gate on a converted schema.
func resolveSchema(t *testing.T, label, raw string) {
	t.Helper()
	var schema jsonschema.Schema
	if err := json.Unmarshal([]byte(raw), &schema); err != nil {
		t.Fatalf("%s: unmarshal schema: %v", label, err)
	}
	if _, err := schema.Resolve(nil); err != nil {
		t.Fatalf("%s: resolve schema: %v", label, err)
	}
}

// schemaFromBody loads a spec and returns the JSON request-body schema ref for
// the given path/POST operation.
func bodySchemaRef(t *testing.T, spec, path string) *openapi3.SchemaRef {
	t.Helper()
	doc := loadTestSpec(t, spec)
	item := doc.Paths.Value(path)
	if item == nil || item.Post == nil {
		t.Fatalf("no POST op at %s", path)
	}
	return item.Post.RequestBody.Value.Content.Get("application/json").Schema
}

func findOp(api *OpenAPI, path, method string) *Operation {
	for _, o := range api.Operations {
		if o.Path == path && strings.EqualFold(o.Method, method) {
			return o
		}
	}
	for _, g := range api.Groups {
		for _, o := range g.Operations {
			if o.Path == path && strings.EqualFold(o.Method, method) {
				return o
			}
		}
	}
	return nil
}

func TestConvertSchemaNullableScalars(t *testing.T) {
	cases := []struct {
		name string
		prop string
		want []any
	}{
		{"nullable-flag", "flag_nullable", []any{"string", "null"}},
		{"type-array-3.1", "arr_null", []any{"integer", "null"}},
	}

	ref := bodySchemaRef(t, `{
  "openapi": "3.1.0",
  "info": {"title": "t", "version": "1"},
  "paths": {"/x": {"post": {"operationId": "X",
    "requestBody": {"content": {"application/json": {"schema": {
      "type": "object",
      "properties": {
        "flag_nullable": {"type": "string", "nullable": true},
        "arr_null": {"type": ["integer", "null"]}
      }
    }}}},
    "responses": {"200": {"description": "ok"}}}}}
}`, "/x")

	out := convertSchema(ref, true)
	props, _ := out["properties"].(map[string]any)
	for _, tc := range cases {
		p, _ := props[tc.prop].(map[string]any)
		got, ok := p["type"].([]any)
		if !ok {
			t.Fatalf("%s: type not an array: %#v", tc.name, p["type"])
		}
		if len(got) != len(tc.want) || got[0] != tc.want[0] || got[1] != tc.want[1] {
			t.Errorf("%s: type = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestConvertSchemaEnumsPreserved(t *testing.T) {
	ref := bodySchemaRef(t, `{
  "openapi": "3.1.0",
  "info": {"title": "t", "version": "1"},
  "paths": {"/x": {"post": {"operationId": "X",
    "requestBody": {"content": {"application/json": {"schema": {
      "type": "object",
      "properties": {"color": {"type": "string", "enum": ["red", "green", "blue"]}}
    }}}},
    "responses": {"200": {"description": "ok"}}}}}
}`, "/x")

	out := convertSchema(ref, true)
	props := out["properties"].(map[string]any)
	color := props["color"].(map[string]any)
	enum, ok := color["enum"].([]any)
	if !ok || len(enum) != 3 || enum[0] != "red" || enum[2] != "blue" {
		t.Fatalf("enum = %#v", color["enum"])
	}
}

func TestConvertSchemaCompositionsPreserved(t *testing.T) {
	ref := bodySchemaRef(t, `{
  "openapi": "3.1.0",
  "info": {"title": "t", "version": "1"},
  "paths": {"/x": {"post": {"operationId": "X",
    "requestBody": {"content": {"application/json": {"schema": {
      "oneOf": [
        {"type": "object", "properties": {"a": {"type": "string"}}},
        {"type": "object", "properties": {"b": {"type": "integer"}}}
      ]
    }}}},
    "responses": {"200": {"description": "ok"}}}}}
}`, "/x")

	out := convertSchema(ref, true)
	branches, ok := out["oneOf"].([]any)
	if !ok || len(branches) != 2 {
		t.Fatalf("oneOf not preserved: %#v", out["oneOf"])
	}

	// anyOf + allOf too.
	for _, kw := range []string{"anyOf", "allOf"} {
		refK := bodySchemaRef(t, `{
  "openapi": "3.1.0",
  "info": {"title": "t", "version": "1"},
  "paths": {"/x": {"post": {"operationId": "X",
    "requestBody": {"content": {"application/json": {"schema": {
      "`+kw+`": [
        {"type": "object", "properties": {"a": {"type": "string"}}},
        {"type": "object", "properties": {"b": {"type": "integer"}}}
      ]
    }}}},
    "responses": {"200": {"description": "ok"}}}}}
}`, "/x")
		o := convertSchema(refK, true)
		b, ok := o[kw].([]any)
		if !ok || len(b) != 2 {
			t.Errorf("%s not preserved: %#v", kw, o[kw])
		}
	}
}

func TestConvertSchemaRefCycleTerminates(t *testing.T) {
	ref := bodySchemaRef(t, `{
  "openapi": "3.1.0",
  "info": {"title": "t", "version": "1"},
  "components": {"schemas": {"Node": {"type": "object", "properties": {
    "name": {"type": "string"},
    "child": {"$ref": "#/components/schemas/Node"}
  }}}},
  "paths": {"/n": {"post": {"operationId": "CreateNode",
    "requestBody": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/Node"}}}},
    "responses": {"200": {"description": "ok"}}}}}
}`, "/n")

	out := convertSchema(ref, true) // must terminate
	props := out["properties"].(map[string]any)
	child, ok := props["child"].(map[string]any)
	if !ok {
		t.Fatalf("child missing: %#v", props)
	}
	// The cycle back-edge collapses to a bare object.
	if child["type"] != "object" {
		t.Errorf("cycle back-edge type = %v, want object", child["type"])
	}
	if _, hasProps := child["properties"]; hasProps {
		t.Errorf("cycle back-edge should not recurse into properties: %#v", child)
	}
	// Must be JSON-serializable.
	if _, err := json.Marshal(out); err != nil {
		t.Fatalf("marshal cyclic result: %v", err)
	}
}

func TestConvertSchemaReadOnlyWriteOnlyStripping(t *testing.T) {
	spec := `{
  "openapi": "3.1.0",
  "info": {"title": "t", "version": "1"},
  "paths": {"/x": {"post": {"operationId": "X",
    "requestBody": {"content": {"application/json": {"schema": {
      "type": "object",
      "required": ["id", "name", "secret"],
      "properties": {
        "id": {"type": "string", "readOnly": true},
        "secret": {"type": "string", "writeOnly": true},
        "name": {"type": "string"}
      }
    }}}},
    "responses": {"200": {"description": "ok"}}}}}
}`
	ref := bodySchemaRef(t, spec, "/x")

	in := convertSchema(ref, true)
	inProps := in["properties"].(map[string]any)
	if _, ok := inProps["id"]; ok {
		t.Errorf("readOnly id must be stripped from input")
	}
	if _, ok := inProps["secret"]; !ok {
		t.Errorf("writeOnly secret must remain in input")
	}
	inReq := toStringSlice(in["required"])
	if contains(inReq, "id") {
		t.Errorf("readOnly id must be removed from input required: %v", inReq)
	}

	out := convertSchema(ref, false)
	outProps := out["properties"].(map[string]any)
	if _, ok := outProps["secret"]; ok {
		t.Errorf("writeOnly secret must be stripped from output")
	}
	if _, ok := outProps["id"]; !ok {
		t.Errorf("readOnly id must remain in output")
	}
	outReq := toStringSlice(out["required"])
	if contains(outReq, "secret") {
		t.Errorf("writeOnly secret must be removed from output required: %v", outReq)
	}
}

func toStringSlice(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func TestBuildMCPToolParamBodyCollision(t *testing.T) {
	api := ProcessAPI("example", loadTestSpec(t, `
openapi: 3.1.0
info: {title: t, version: "1"}
paths:
  /widgets/{name}:
    post:
      operationId: CreateWidget
      parameters:
        - in: path
          name: name
          required: true
          schema: {type: string}
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                name: {type: string}
                size: {type: integer}
      responses:
        "200": {description: ok}
`))
	op := findOp(api, "/widgets/{name}", "post")
	if op == nil || op.MCP == nil {
		t.Fatal("op/MCP missing")
	}

	var schema map[string]any
	if err := json.Unmarshal([]byte(op.MCP.InputSchema), &schema); err != nil {
		t.Fatalf("input schema not JSON: %v", err)
	}
	props := schema["properties"].(map[string]any)
	if _, ok := props["name"]; !ok {
		t.Errorf("param 'name' should keep the plain property")
	}
	if _, ok := props["body_name"]; !ok {
		t.Errorf("colliding body prop should be prefixed body_name; got %v", keysOf(props))
	}

	// Route for body_name should point at wire name "name" in body.
	var found bool
	for _, r := range op.MCP.Routes {
		if r.Prop == "body_name" {
			found = true
			if r.WireName != "name" || r.In != "body" {
				t.Errorf("body_name route = %+v", r)
			}
		}
	}
	if !found {
		t.Errorf("no route for body_name")
	}
}

func TestBuildMCPToolNonObjectBody(t *testing.T) {
	api := ProcessAPI("example", loadTestSpec(t, `
openapi: 3.1.0
info: {title: t, version: "1"}
paths:
  /bulk:
    post:
      operationId: BulkCreate
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: array
              items: {type: string}
      responses:
        "200": {description: ok}
`))
	op := findOp(api, "/bulk", "post")
	var schema map[string]any
	if err := json.Unmarshal([]byte(op.MCP.InputSchema), &schema); err != nil {
		t.Fatalf("input schema: %v", err)
	}
	props := schema["properties"].(map[string]any)
	if _, ok := props["body"]; !ok {
		t.Fatalf("non-object body should be under 'body': %v", keysOf(props))
	}
	req := toStringSlice(schema["required"])
	if !contains(req, "body") {
		t.Errorf("required body missing: %v", req)
	}
	var route MCPArgRoute
	for _, r := range op.MCP.Routes {
		if r.Prop == "body" {
			route = r
		}
	}
	if route.In != "body" || route.Type != "json" {
		t.Errorf("body route = %+v", route)
	}
}

func TestMCPExclusionSSEOnlyVsJSONAndSSE(t *testing.T) {
	api := ProcessAPI("example", loadTestSpec(t, `
openapi: 3.1.0
info: {title: t, version: "1"}
paths:
  /stream:
    post:
      operationId: StreamOnly
      responses:
        "200":
          description: ok
          content:
            text/event-stream: {schema: {type: string}}
  /chat:
    post:
      operationId: ChatCompletions
      responses:
        "200":
          description: ok
          content:
            application/json: {schema: {type: object, properties: {id: {type: string}}}}
            text/event-stream: {schema: {type: string}}
`))

	sse := findOp(api, "/stream", "post")
	if !sse.MCP.Excluded || sse.MCP.ExcludedReason != "sse-only" {
		t.Errorf("stream op should be sse-only excluded, got excluded=%v reason=%q", sse.MCP.Excluded, sse.MCP.ExcludedReason)
	}
	chat := findOp(api, "/chat", "post")
	if chat.MCP.Excluded {
		t.Errorf("json+sse op should be included, got excluded reason %q", chat.MCP.ExcludedReason)
	}
	if chat.MCP.OutputSchema == "" {
		t.Errorf("json+sse op should get an output schema from the JSON media type")
	}
}

func TestMCPExclusionMultipart(t *testing.T) {
	api := ProcessAPI("example", loadTestSpec(t, `
openapi: 3.1.0
info: {title: t, version: "1"}
paths:
  /upload:
    post:
      operationId: Upload
      requestBody:
        content:
          multipart/form-data:
            schema: {type: object, properties: {file: {type: string, format: binary}}}
      responses:
        "200": {description: ok}
`))
	op := findOp(api, "/upload", "post")
	if !op.MCP.Excluded || op.MCP.ExcludedReason != "multipart/binary request body" {
		t.Errorf("multipart op should be excluded, got excluded=%v reason=%q", op.MCP.Excluded, op.MCP.ExcludedReason)
	}
}

func TestMCPExclusionDeprecated(t *testing.T) {
	api := ProcessAPI("example", loadTestSpec(t, `
openapi: 3.1.0
info: {title: t, version: "1"}
paths:
  /old:
    get:
      operationId: OldThing
      deprecated: true
      responses:
        "200": {description: ok}
`))
	op := findOp(api, "/old", "get")
	if !op.MCP.Excluded || op.MCP.ExcludedReason != "deprecated" {
		t.Errorf("deprecated op should be excluded, got excluded=%v reason=%q", op.MCP.Excluded, op.MCP.ExcludedReason)
	}
}

func TestMCPExclusionXMCPIgnore(t *testing.T) {
	api := ProcessAPI("example", loadTestSpec(t, `
openapi: 3.1.0
info: {title: t, version: "1"}
paths:
  /hidden:
    get:
      operationId: HiddenThing
      x-mcp-ignore: true
      responses:
        "200": {description: ok}
`))
	op := findOp(api, "/hidden", "get")
	if !op.MCP.Excluded || op.MCP.ExcludedReason != "x-mcp-ignore" {
		t.Errorf("x-mcp-ignore op should be excluded, got excluded=%v reason=%q", op.MCP.Excluded, op.MCP.ExcludedReason)
	}
}

func TestMCPAnnotationMappingPerMethod(t *testing.T) {
	api := ProcessAPI("example", loadTestSpec(t, `
openapi: 3.1.0
info: {title: t, version: "1"}
paths:
  /r:
    get: {operationId: G, responses: {"200": {description: ok}}}
    put: {operationId: U, responses: {"200": {description: ok}}}
    post: {operationId: C, responses: {"200": {description: ok}}}
    delete: {operationId: D, responses: {"200": {description: ok}}}
    patch: {operationId: P, responses: {"200": {description: ok}}}
  /h:
    head: {operationId: H, responses: {"200": {description: ok}}}
`))

	cases := []struct {
		path, method                      string
		readOnly, destructive, idempotent bool
	}{
		{"/r", "get", true, false, true},
		{"/h", "head", true, false, true},
		{"/r", "put", false, false, true},
		{"/r", "post", false, false, false},
		{"/r", "patch", false, false, false},
		{"/r", "delete", false, true, true},
	}
	for _, tc := range cases {
		op := findOp(api, tc.path, tc.method)
		if op == nil || op.MCP == nil {
			t.Fatalf("%s %s missing", tc.method, tc.path)
		}
		m := op.MCP
		if m.ReadOnly != tc.readOnly || m.Destructive != tc.destructive || m.Idempotent != tc.idempotent {
			t.Errorf("%s %s: ro=%v dest=%v idem=%v, want %v/%v/%v",
				tc.method, tc.path, m.ReadOnly, m.Destructive, m.Idempotent,
				tc.readOnly, tc.destructive, tc.idempotent)
		}
	}
}

func TestMCPNamingCollisionDeterminism(t *testing.T) {
	spec := `
openapi: 3.1.0
info: {title: t, version: "1"}
paths:
  /alpha:
    post:
      operationId: AlphaRun
      x-cli-group: shared
      x-cli-name: run
      responses: {"200": {description: ok}}
  /beta:
    post:
      operationId: BetaRun
      x-cli-group: shared
      x-cli-name: run
      responses: {"200": {description: ok}}
`
	names := func() []string {
		api := ProcessAPI("example", loadTestSpec(t, spec))
		a := findOp(api, "/alpha", "post")
		b := findOp(api, "/beta", "post")
		return []string{a.MCP.Name, b.MCP.Name}
	}

	run1 := names()
	run2 := names()
	if run1[0] != run2[0] || run1[1] != run2[1] {
		t.Fatalf("non-deterministic naming: %v vs %v", run1, run2)
	}
	// /alpha sorts before /beta, so it keeps the base name.
	if run1[0] != "shared_run" || run1[1] != "shared_run_2" {
		t.Fatalf("collision resolution = %v, want [shared_run shared_run_2]", run1)
	}
}

func TestMCPDescriptionTruncation(t *testing.T) {
	long := strings.Repeat("word ", 500) // ~2500 chars
	api := ProcessAPI("example", loadTestSpec(t, `
openapi: 3.1.0
info: {title: t, version: "1"}
paths:
  /d:
    get:
      operationId: DescThing
      summary: A summary
      description: `+"\""+long+"\""+`
      responses: {"200": {description: ok}}
`))
	op := findOp(api, "/d", "get")
	if l := len(op.MCP.Description); l > maxMCPDescription {
		t.Fatalf("description length %d exceeds max %d", l, maxMCPDescription)
	}
	if !strings.HasPrefix(op.MCP.Description, "A summary") {
		t.Errorf("description should start with summary: %q", op.MCP.Description[:20])
	}
	// Word-boundary trim: must not end mid-word.
	if strings.HasSuffix(op.MCP.Description, "wor") {
		t.Errorf("description cut mid-word")
	}
}

// TestMCPFixtureScale loads the Orq fixture and asserts the tool surface.
//
// Every included tool's input schema AND output schema are round-tripped
// through jsonschema.Schema Resolve (the exact path the generated mcpserver
// runtime takes at startup), asserting the converter never emits a schema the
// runtime would reject — the strongest schema-correctness gate available.
func TestMCPFixtureScale(t *testing.T) {
	specPath := filepath.Join("testdata", "orq", "openapi.json")
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Skipf("fixture missing: %v", err)
	}
	doc, err := loadOpenAPIDocument(data)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}

	api := ProcessAPI("orq", doc)

	var all []*Operation
	all = append(all, api.Operations...)
	for _, g := range api.Groups {
		all = append(all, g.Operations...)
	}

	included := 0
	byReason := map[string]int{}
	for _, o := range all {
		if o.MCP == nil {
			t.Fatalf("op %s %s has no MCP spec", o.Method, o.Path)
		}
		if o.MCP.Excluded {
			byReason[o.MCP.ExcludedReason]++
			continue
		}
		included++

		// Every included tool's input schema must parse as a JSON object and
		// resolve under jsonschema-go (the runtime registration gate).
		var parsed map[string]any
		if err := json.Unmarshal([]byte(o.MCP.InputSchema), &parsed); err != nil {
			t.Fatalf("op %s %s input schema not JSON: %v", o.Method, o.Path, err)
		}
		if parsed["type"] != "object" {
			t.Fatalf("op %s %s input schema type = %v, want object", o.Method, o.Path, parsed["type"])
		}
		resolveSchema(t, o.Method+" "+o.Path+" input", o.MCP.InputSchema)

		// The output schema (when present) must resolve too.
		if o.MCP.OutputSchema != "" {
			resolveSchema(t, o.Method+" "+o.Path+" output", o.MCP.OutputSchema)
		}
	}

	if included < 120 {
		t.Errorf("included tools = %d, want >= 120", included)
	}
	if byReason["sse-only"] != 3 {
		t.Errorf("sse-only exclusions = %d, want 3", byReason["sse-only"])
	}
	if byReason["multipart/binary request body"] != 5 {
		t.Errorf("multipart exclusions = %d, want 5", byReason["multipart/binary request body"])
	}

	// The JSON+SSE ops must be present (using the JSON media type).
	chat := findOp(api, "/v2/router/chat/completions", "post")
	if chat == nil || chat.MCP == nil {
		t.Fatal("/v2/router/chat/completions op missing")
	}
	if chat.MCP.Excluded {
		t.Errorf("/v2/router/chat/completions should be included, excluded reason=%q", chat.MCP.ExcludedReason)
	}

	t.Logf("orq fixture: included=%d exclusions=%v", included, byReason)
}

func TestMCPParamsOpLevelOverridesPathLevel(t *testing.T) {
	api := ProcessAPI("example", loadTestSpec(t, `
openapi: 3.1.0
info: {title: t, version: "1"}
paths:
  /things:
    parameters:
      - in: query
        name: limit
        required: false
        description: path-level limit
        schema: {type: string}
      - in: query
        name: cursor
        required: false
        schema: {type: string}
    get:
      operationId: ListThings
      parameters:
        - in: query
          name: limit
          required: true
          description: op-level limit
          schema: {type: integer}
      responses:
        "200": {description: ok}
`))
	op := findOp(api, "/things", "get")
	if op == nil || op.MCP == nil {
		t.Fatal("op/MCP missing")
	}

	var schema map[string]any
	if err := json.Unmarshal([]byte(op.MCP.InputSchema), &schema); err != nil {
		t.Fatalf("input schema: %v", err)
	}
	props := schema["properties"].(map[string]any)
	if len(props) != 2 {
		t.Fatalf("expected 2 properties (limit, cursor), got %v", keysOf(props))
	}

	// Op-level schema must win: integer type + op-level description.
	limit, _ := props["limit"].(map[string]any)
	if limit["type"] != "integer" {
		t.Errorf("limit type = %v, want integer (op-level override)", limit["type"])
	}
	if limit["description"] != "op-level limit" {
		t.Errorf("limit description = %v, want op-level description", limit["description"])
	}

	// Exactly one required entry for limit (op-level required: true), no dupes.
	req := toStringSlice(schema["required"])
	count := 0
	for _, r := range req {
		if r == "limit" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("required contains limit %d times, want exactly 1: %v", count, req)
	}

	// Exactly one route for limit, using the op-level schema type.
	limitRoutes := 0
	for _, r := range op.MCP.Routes {
		if r.Prop == "limit" {
			limitRoutes++
			if r.Type != "int64" {
				t.Errorf("limit route type = %q, want int64 (op-level schema)", r.Type)
			}
		}
	}
	if limitRoutes != 1 {
		t.Errorf("routes contain limit %d times, want exactly 1: %+v", limitRoutes, op.MCP.Routes)
	}
}

func TestTruncateMCPDescriptionRuneSafe(t *testing.T) {
	// No word boundary anywhere: a solid run of multibyte runes forces the
	// raw cut path. Every rune is 3 bytes, so a byte-based value[:max] slice
	// would split a rune and produce invalid UTF-8.
	long := strings.Repeat("日", 600) // 600 runes, 1800 bytes
	got := truncateMCPDescription(long, maxMCPDescription)

	if runes := []rune(got); len(runes) > maxMCPDescription {
		t.Fatalf("truncated length = %d runes, want <= %d", len(runes), maxMCPDescription)
	}
	if !strings.HasPrefix(long, got) {
		t.Fatalf("truncation split a multibyte rune (result is not a prefix of the input)")
	}
	for i, r := range got {
		if r == '�' {
			t.Fatalf("invalid UTF-8 at index %d after truncation", i)
		}
	}

	// Word-boundary trim still applies for multibyte text with spaces.
	spaced := strings.Repeat("日本語 ", 400)
	got = truncateMCPDescription(spaced, maxMCPDescription)
	if strings.HasSuffix(got, " ") || len([]rune(got)) > maxMCPDescription {
		t.Fatalf("word-boundary trim failed for multibyte text: len=%d runes", len([]rune(got)))
	}

	// Short values are returned untouched.
	if out := truncateMCPDescription("hello", maxMCPDescription); out != "hello" {
		t.Fatalf("short value changed: %q", out)
	}
}

func TestAssignMCPToolNamesRespects128CharLimitOnCollision(t *testing.T) {
	base := strings.Repeat("a", 128)
	mkOp := func(path string) *Operation {
		return &Operation{
			Path:   path,
			Method: "Post",
			MCP:    &MCPTool{Name: sanitizeToolName(base)},
		}
	}
	api := &OpenAPI{Operations: []*Operation{mkOp("/a"), mkOp("/b"), mkOp("/c")}}

	assignMCPToolNames(api)

	seen := map[string]bool{}
	for _, o := range api.Operations {
		name := o.MCP.Name
		if len(name) > 128 || len(name) == 0 {
			t.Errorf("tool name length %d out of [1,128]: %q", len(name), name)
		}
		if seen[name] {
			t.Errorf("duplicate tool name after collision pass: %q", name)
		}
		seen[name] = true
	}
	// The later ops must carry the numeric suffix within the limit.
	if !strings.HasSuffix(api.Operations[1].MCP.Name, "_2") {
		t.Errorf("second op name = %q, want _2 suffix", api.Operations[1].MCP.Name)
	}
	if !strings.HasSuffix(api.Operations[2].MCP.Name, "_3") {
		t.Errorf("third op name = %q, want _3 suffix", api.Operations[2].MCP.Name)
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
