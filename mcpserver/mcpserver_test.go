package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/orq-ai/bartolo/cli"
)

// echoSchema requires a "name" string property.
const echoSchema = `{
	"type": "object",
	"properties": {"name": {"type": "string"}},
	"required": ["name"],
	"additionalProperties": false
}`

// newSession wires a client and server over in-memory transports and returns a
// connected client session. It registers t.Cleanup to close both sides.
func newSession(t *testing.T, defs []ToolDef) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()

	server, err := buildServer(serverInfo{name: "testapp", version: "1.2.3", instructions: "instructions here"}, defs)
	if err != nil {
		t.Fatalf("buildServer: %v", err)
	}

	serverT, clientT := mcp.NewInMemoryTransports()

	ss, err := server.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	t.Cleanup(func() { _ = ss.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	return cs
}

func textOf(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatalf("expected content, got none")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", res.Content[0])
	}
	return tc.Text
}

func TestListToolsExposesSchema(t *testing.T) {
	defs := []ToolDef{{
		Name:        "echo",
		Title:       "Echo",
		Description: "Echoes the name back",
		Group:       "misc",
		InputSchema: json.RawMessage(echoSchema),
		ReadOnly:    true,
		Idempotent:  true,
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			return args, nil
		},
	}}
	cs := newSession(t, defs)

	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(res.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(res.Tools))
	}
	tool := res.Tools[0]
	if tool.Name != "echo" {
		t.Errorf("name = %q, want echo", tool.Name)
	}
	if tool.InputSchema == nil {
		t.Errorf("input schema not exposed")
	}
	if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
		t.Errorf("expected ReadOnlyHint true, got %+v", tool.Annotations)
	}
	// OpenWorldHint must be explicitly false (closed API domain).
	if tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
		t.Errorf("expected OpenWorldHint false, got %v", tool.Annotations.OpenWorldHint)
	}
}

func TestHappyCallStructuredContentAndTextAgree(t *testing.T) {
	defs := []ToolDef{{
		Name:        "echo",
		InputSchema: json.RawMessage(echoSchema),
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			return map[string]any{"echoed": args["name"]}, nil
		},
	}}
	cs := newSession(t, defs)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "echo",
		Arguments: map[string]any{"name": "world"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected IsError; text=%s", textOf(t, res))
	}

	// StructuredContent should decode to the same object as the text block.
	var fromText map[string]any
	if err := json.Unmarshal([]byte(textOf(t, res)), &fromText); err != nil {
		t.Fatalf("text not JSON: %v", err)
	}
	if fromText["echoed"] != "world" {
		t.Errorf("text echoed = %v, want world", fromText["echoed"])
	}

	sc, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("StructuredContent type = %T", res.StructuredContent)
	}
	if sc["echoed"] != "world" {
		t.Errorf("structured echoed = %v, want world", sc["echoed"])
	}
}

func TestValidationErrorDoesNotInvokeHandler(t *testing.T) {
	invoked := false
	defs := []ToolDef{{
		Name:        "echo",
		InputSchema: json.RawMessage(echoSchema),
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			invoked = true
			return args, nil
		},
	}}
	cs := newSession(t, defs)

	// Missing required "name".
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "echo",
		Arguments: map[string]any{"other": 1},
	})
	if err != nil {
		t.Fatalf("CallTool returned protocol error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError for invalid args")
	}
	if invoked {
		t.Errorf("handler was invoked despite validation failure")
	}
	if !strings.Contains(textOf(t, res), "invalid arguments") {
		t.Errorf("expected 'invalid arguments' message, got %q", textOf(t, res))
	}
}

func TestHTTPErrorMappedToStructuredContent(t *testing.T) {
	defs := []ToolDef{{
		Name:        "boom",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			return nil, &cli.HTTPError{
				StatusCode: 404,
				Body:       []byte(`{"message":"not found"}`),
			}
		},
	}}
	cs := newSession(t, defs)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "boom"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError")
	}
	if txt := textOf(t, res); !strings.Contains(txt, "HTTP 404") {
		t.Errorf("text = %q, want HTTP 404 prefix", txt)
	}
	sc, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("StructuredContent type = %T", res.StructuredContent)
	}
	// JSON round-trips numbers as float64.
	if status, _ := sc["status"].(float64); status != 404 {
		t.Errorf("status = %v, want 404", sc["status"])
	}
	body, ok := sc["body"].(map[string]any)
	if !ok {
		t.Fatalf("body type = %T", sc["body"])
	}
	if body["message"] != "not found" {
		t.Errorf("body.message = %v", body["message"])
	}
}

func TestPanicRecovery(t *testing.T) {
	defs := []ToolDef{{
		Name:        "panic",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			panic("kaboom")
		},
	}}
	cs := newSession(t, defs)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "panic"})
	if err != nil {
		t.Fatalf("CallTool returned protocol error on panic: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError after panic")
	}
	if !strings.Contains(textOf(t, res), "panicked") {
		t.Errorf("expected panic message, got %q", textOf(t, res))
	}
}

func TestFilterTools(t *testing.T) {
	defs := []ToolDef{
		{Name: "a_list", Group: "a", ReadOnly: true},
		{Name: "a_create", Group: "a", ReadOnly: false},
		{Name: "b_get", Group: "b", ReadOnly: true},
	}

	cases := []struct {
		name string
		f    filterOptions
		want []string
	}{
		{"none", filterOptions{}, []string{"a_list", "a_create", "b_get"}},
		{"tools", filterOptions{tools: []string{"a_list", "b_get"}}, []string{"a_list", "b_get"}},
		{"tools-trim", filterOptions{tools: []string{" a_list "}}, []string{"a_list"}},
		{"groups", filterOptions{groups: []string{"a"}}, []string{"a_list", "a_create"}},
		{"read-only", filterOptions{readOnly: true}, []string{"a_list", "b_get"}},
		{"compose", filterOptions{groups: []string{"a"}, readOnly: true}, []string{"a_list"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := filterTools(defs, tc.f)
			var names []string
			for _, d := range got {
				names = append(names, d.Name)
			}
			if strings.Join(names, ",") != strings.Join(tc.want, ",") {
				t.Errorf("got %v, want %v", names, tc.want)
			}
		})
	}
}

func TestWriteManifest(t *testing.T) {
	defs := []ToolDef{{
		Name:        "echo",
		Title:       "Echo",
		Description: "desc",
		InputSchema: json.RawMessage(echoSchema),
		ReadOnly:    true,
	}}
	var buf bytes.Buffer
	if err := writeManifest(&buf, defs); err != nil {
		t.Fatalf("writeManifest: %v", err)
	}
	var manifest []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &manifest); err != nil {
		t.Fatalf("manifest not JSON: %v\n%s", err, buf.String())
	}
	if len(manifest) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(manifest))
	}
	if manifest[0]["name"] != "echo" {
		t.Errorf("name = %v", manifest[0]["name"])
	}
	if _, ok := manifest[0]["inputSchema"]; !ok {
		t.Errorf("inputSchema missing from manifest")
	}
}

func TestCoerceString(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{"hi", "hi"},
		{true, "true"},
		{false, "false"},
		{float64(1000000), "1000000"},
		{float64(3.5), "3.5"},
		{float64(42), "42"},
		{json.Number("42"), "42"},
		{nil, ""},
	}
	for _, tc := range cases {
		if got := CoerceString(tc.in); got != tc.want {
			t.Errorf("CoerceString(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestStdioPurity runs a real stdio session (over os.Stdin/os.Stdout, the path
// serveStdio actually uses) and asserts that every line written to stdout is a
// valid JSON-RPC message — including that a handler writing to cli.Stdout does
// NOT leak onto the protocol stream (it is redirected to stderr).
func TestStdioPurity(t *testing.T) {
	origIn, origOut := os.Stdin, os.Stdout
	origStdout, origStderr := cli.Stdout, cli.Stderr
	t.Cleanup(func() {
		os.Stdin, os.Stdout = origIn, origOut
		cli.Stdout, cli.Stderr = origStdout, origStderr
	})

	// Route cli.Stderr (where cli.Stdout is redirected) to a buffer so junk from
	// the handler never reaches the terminal or the captured stdout pipe.
	var stderrBuf bytes.Buffer
	cli.Stderr = &stderrBuf
	cli.Stdout = os.Stdout

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = stdinR
	os.Stdout = stdoutW

	const junk = "GARBAGE_NOT_JSONRPC"
	defs := []ToolDef{{
		Name:        "noisy",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			// This must NOT corrupt stdout — cli.Stdout is redirected to stderr.
			_, _ = io.WriteString(cli.Stdout, junk+"\n")
			return map[string]any{"ok": true}, nil
		},
	}}

	// Capture everything the server writes to stdout concurrently.
	captured := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(stdoutR)
		captured <- data
	}()

	done := make(chan error, 1)
	go func() {
		done <- serveStdio(context.Background(), serverInfo{name: "testapp", version: "1.0.0"}, defs)
	}()

	// Drive the protocol manually with newline-delimited JSON-RPC (2026-07-28:
	// no initialize handshake; each request carries _meta).
	const meta = `"_meta":{"io.modelcontextprotocol/clientCapabilities":{},"io.modelcontextprotocol/clientInfo":{"name":"t","version":"0"},"io.modelcontextprotocol/protocolVersion":"2026-07-28"}`
	requests := []string{
		`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{` + meta + `}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{` + meta + `}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"noisy","arguments":{},` + meta + `}}`,
	}
	if _, err := io.WriteString(stdinW, strings.Join(requests, "\n")+"\n"); err != nil {
		t.Fatal(err)
	}
	// Give the server a moment to process the call before closing stdin.
	time.Sleep(200 * time.Millisecond)
	_ = stdinW.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serveStdio: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serveStdio did not return")
	}

	_ = stdoutW.Close()
	data := <-captured

	// cli.Stdout redirect must have caught the junk on stderr, not stdout.
	if !strings.Contains(stderrBuf.String(), junk) {
		t.Errorf("expected handler junk on redirected cli.Stderr, not found; stderr=%q stdout=%q", stderrBuf.String(), string(data))
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	lines := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		lines++
		if strings.Contains(line, junk) {
			t.Fatalf("junk leaked onto stdout: %q", line)
		}
		var msg map[string]any
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatalf("stdout line is not valid JSON: %q (%v)", line, err)
		}
		if msg["jsonrpc"] != "2.0" {
			t.Fatalf("stdout line is not JSON-RPC 2.0: %q", line)
		}
	}
	if lines == 0 {
		t.Fatal("no JSON-RPC output captured")
	}
}

func TestServerAdvertisesToolsOnlyWithCacheTTL(t *testing.T) {
	defs := []ToolDef{{
		Name:        "noop",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Handler:     func(context.Context, map[string]any) (any, error) { return map[string]any{}, nil },
	}}
	cs := newSession(t, defs)

	caps := cs.InitializeResult().Capabilities
	if caps.Logging != nil { //nolint:staticcheck // asserting the deprecated capability is absent
		t.Errorf("logging is deprecated as of 2026-07-28 and must not be advertised")
	}
	if caps.Tools == nil || caps.Tools.ListChanged {
		t.Errorf("tools capability = %+v, want present without listChanged", caps.Tools)
	}

	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.TTLMs != int(defaultListTTL/time.Millisecond) || res.CacheScope != "public" {
		t.Errorf("tools/list cache = %d/%q, want %d/public", res.TTLMs, res.CacheScope, int(defaultListTTL/time.Millisecond))
	}
}

func TestStructuredContentShape(t *testing.T) {
	returning := func(name string, out any, outputSchema string) ToolDef {
		def := ToolDef{
			Name:        name,
			InputSchema: json.RawMessage(`{"type":"object"}`),
			Handler:     func(context.Context, map[string]any) (any, error) { return out, nil },
		}
		if outputSchema != "" {
			def.OutputSchema = json.RawMessage(outputSchema)
		}
		return def
	}
	cs := newSession(t, []ToolDef{
		returning("array_no_schema", []any{float64(1), float64(2)}, ""),
		returning("object_no_schema", map[string]any{"a": "b"}, ""),
		returning("empty_with_schema", nil, `{"type":"object"}`),
	})
	call := func(name string) *mcp.CallToolResult {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return res
	}

	// Clients before 2026-07-28 reject non-object structuredContent.
	if res := call("array_no_schema"); res.StructuredContent != nil || textOf(t, res) != "[1,2]" {
		t.Errorf("array result: structured=%v text=%q, want text only", res.StructuredContent, textOf(t, res))
	}
	if res := call("object_no_schema"); res.StructuredContent == nil {
		t.Errorf("an object result should be structured even without an output schema")
	}
	// A tool with an output schema must always return structuredContent.
	if res := call("empty_with_schema"); res.StructuredContent == nil {
		t.Errorf("an empty body with an output schema should become {}")
	}
}

func TestBuildServerRejectsUnregistrableTools(t *testing.T) {
	handler := func(context.Context, map[string]any) (any, error) { return nil, nil }
	cases := map[string]ToolDef{
		"non-object input schema": {Name: "a", InputSchema: json.RawMessage(`{"type":"string"}`), Handler: handler},
		"no handler":              {Name: "b", InputSchema: json.RawMessage(`{"type":"object"}`)},
		"invalid output schema":   {Name: "c", OutputSchema: json.RawMessage(`{`), Handler: handler},
	}
	for name, def := range cases {
		t.Run(name, func(t *testing.T) {
			// The SDK panics on these; the server must report them instead.
			if _, err := buildServer(serverInfo{name: "t"}, []ToolDef{def}); err == nil {
				t.Error("expected an error")
			}
		})
	}

	// A tool that declares no input schema takes no arguments.
	if _, err := buildServer(serverInfo{name: "t"}, []ToolDef{{Name: "d", Handler: handler}}); err != nil {
		t.Errorf("a missing input schema should default to an empty object: %v", err)
	}
}
