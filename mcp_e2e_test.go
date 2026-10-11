// End-to-end MCP test: a real go-sdk MCP client drives a freshly generated CLI
// (scaffolded + generated + built exactly like a user would), which in turn
// calls the shared :8005 echo server started in TestMain. It exercises the full
// bridge — tool listing, structured results, outbound auth propagation,
// server-side input validation, and upstream-error mapping.
//
// The generate/build path mirrors TestGenerateFromJSONFixtureBuildsCLI but runs
// out-of-process (this is package main_test) via the built bartolo binary, so a
// single scaffold+build is shared by every subtest.
package main_test

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// e2eSpec is the fixture spec for the MCP e2e test. It declares bearer auth, an
// echo POST op (typed JSON body with a required property, a query param, and a
// header param), a read-only GET op, and an op that always fails upstream. The
// servers block points at the :8005 echo server from TestMain.
const e2eSpec = `openapi: 3.0.3
info:
  title: MCP E2E API
  version: "1"
servers:
  - url: http://localhost:8005
components:
  securitySchemes:
    bearerAuth:
      type: http
      scheme: bearer
security:
  - bearerAuth: []
paths:
  /echo:
    post:
      operationId: echo
      summary: Echo a message
      description: Echo back the JSON body plus query and header values.
      parameters:
        - name: q
          in: query
          schema:
            type: string
        - name: x-request-id
          in: header
          schema:
            type: string
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [message]
              properties:
                message:
                  type: string
                  description: The message to echo.
                count:
                  type: integer
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
  /ping:
    get:
      operationId: ping
      summary: Ping the server
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
  /error:
    post:
      operationId: fail
      summary: Always fails
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
`

// e2eCustomRegister is written over the generated cli/custom stub so the e2e
// binary exercises AddTools + PatchTool. Regeneration does not overwrite this
// file once it exists; the test writes it after generate.
const e2eCustomRegister = `package custom

import (
	"context"
	"encoding/json"

	"github.com/orq-ai/bartolo/mcpserver"
	"github.com/spf13/cobra"
)

func Register(root *cobra.Command) {
	_ = root
	mcpserver.Use(
		mcpserver.AddTools(mcpserver.ToolDef{
			Name:        "custom_ping",
			Title:       "Custom Ping",
			Description: "A user-owned extra tool",
			Group:       "custom",
			InputSchema: json.RawMessage(` + "`" + `{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}` + "`" + `),
			ReadOnly:    true,
			Idempotent:  true,
			Handler: func(ctx context.Context, args map[string]any) (any, error) {
				return map[string]any{"hello": args["name"]}, nil
			},
		}),
		mcpserver.PatchTool("echo_echo", func(t *mcpserver.ToolDef) {
			t.Title = "Patched Echo"
		}),
	)
}
`

// runCmd runs name with args in dir and fails the test on error.
func runCmd(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, string(out))
	}
}

// inputSchemaShape is the subset of a tool input schema the assertions inspect.
type inputSchemaShape struct {
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties"`
	Required   []string       `json:"required"`
}

func TestMCPEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e build in -short mode")
	}

	repoRoot, err := os.Getwd()
	require.NoError(t, err, "getwd")

	// Build the bartolo generator once, then scaffold + generate + build the CLI.
	genBin := filepath.Join(t.TempDir(), "bartolo-gen")
	runCmd(t, repoRoot, "go", "build", "-o", genBin, ".")

	scaffold := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(scaffold, "openapi.yaml"), []byte(e2eSpec), 0o600), "write fixture")

	runCmd(t, scaffold, genBin, "init", "mcpe2e", "--bartolo-path", repoRoot, "--serialization-format", "json")
	runCmd(t, scaffold, genBin, "generate", "openapi.yaml")
	require.NoError(t, os.WriteFile(filepath.Join(scaffold, "cli", "custom", "register.go"), []byte(e2eCustomRegister), 0o600), "write custom MCP hooks")
	runCmd(t, scaffold, "go", "mod", "tidy")

	cliBin := filepath.Join(scaffold, "mcpe2e-bin")
	runCmd(t, scaffold, "go", "build", "-o", cliBin, "./cmd/mcpe2e")

	// Connect one MCP client session (shared by all subtests) over a command
	// transport spawning `mcpe2e-bin mcp`, with the bearer token supplied via the
	// CLI's API key env var so the outbound auth middleware injects it.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	serverCmd := exec.Command(cliBin, "mcp")
	serverCmd.Env = append(os.Environ(), "MCPE2E_API_KEY=test-token")

	client := mcp.NewClient(&mcp.Implementation{Name: "e2e-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: serverCmd}, nil)
	require.NoError(t, err, "connect MCP client")
	t.Cleanup(func() { _ = session.Close() })

	// Shared ListTools result, keyed by name.
	lt, err := session.ListTools(ctx, nil)
	require.NoError(t, err, "ListTools")
	tools := map[string]*mcp.Tool{}
	for _, tool := range lt.Tools {
		tools[tool.Name] = tool
	}

	t.Run("ListToolsExposesExpectedToolsAndSchema", func(t *testing.T) {
		for _, name := range []string{"echo_echo", "ping_ping", "error_fail"} {
			assert.Contains(t, tools, name, "tool %q should be exposed", name)
		}

		echo := tools["echo_echo"]
		require.NotNil(t, echo)
		// Client-side, Tool.InputSchema is the server schema's default JSON
		// marshaling (a map[string]any); re-marshal it to inspect the shape.
		raw, err := json.Marshal(echo.InputSchema)
		require.NoError(t, err, "marshal echo input schema")
		var shape inputSchemaShape
		require.NoError(t, json.Unmarshal(raw, &shape), "unmarshal echo input schema")
		assert.Equal(t, "object", shape.Type)
		// Body's required prop, optional body prop, query param, header param.
		for _, prop := range []string{"message", "count", "q", "x_request_id"} {
			assert.Contains(t, shape.Properties, prop, "echo schema should carry property %q", prop)
		}
		assert.Equal(t, []string{"message"}, shape.Required, "only the body's required prop is required")
	})

	t.Run("ReadOnlyAnnotationOnGetOp", func(t *testing.T) {
		ping := tools["ping_ping"]
		require.NotNil(t, ping)
		require.NotNil(t, ping.Annotations, "ping should carry annotations")
		assert.True(t, ping.Annotations.ReadOnlyHint, "GET op should be readOnlyHint=true")
	})

	t.Run("CallEchoReturnsStructuredContentAndPropagatesAuth", func(t *testing.T) {
		res, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      "echo_echo",
			Arguments: map[string]any{"message": "hi", "q": "foo", "x_request_id": "bar"},
		})
		require.NoError(t, err, "CallTool echo")
		require.False(t, res.IsError, "echo should not be an error result")

		sc, ok := res.StructuredContent.(map[string]any)
		require.True(t, ok, "structuredContent should be an object, got %T", res.StructuredContent)
		assert.Equal(t, "hi", sc["message"], "echoed body")
		assert.Equal(t, "foo", sc["q"], "query param propagated")
		assert.Equal(t, "bar", sc["request-id"], "header param propagated")
		// The echo handler reflects the upstream Authorization header; its
		// presence proves the CLI's env-var bearer token flowed to the API call.
		assert.Equal(t, "Bearer test-token", sc["authorization"], "outbound auth propagated")
	})

	t.Run("InvalidArgsErrorWithoutHittingUpstream", func(t *testing.T) {
		before := atomic.LoadInt64(&echoRequestCount)

		res, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      "echo_echo",
			Arguments: map[string]any{}, // missing required "message"
		})
		require.NoError(t, err, "CallTool should not raise a protocol error for invalid args")
		assert.True(t, res.IsError, "missing required arg should be an isError result")

		after := atomic.LoadInt64(&echoRequestCount)
		assert.Equal(t, before, after, "no upstream request may be made when validation fails")
	})

	t.Run("UpstreamErrorMapsToStructuredError", func(t *testing.T) {
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "error_fail"})
		require.NoError(t, err, "CallTool error_fail")
		require.True(t, res.IsError, "upstream 422 should be an isError result")

		sc, ok := res.StructuredContent.(map[string]any)
		require.True(t, ok, "structuredContent should carry the error payload, got %T", res.StructuredContent)
		// JSON numbers decode as float64.
		assert.EqualValues(t, 422, sc["status"], "structured error should carry the upstream status")
	})

	t.Run("CustomToolAndPatchedGeneratedTool", func(t *testing.T) {
		echo := tools["echo_echo"]
		require.NotNil(t, echo)
		assert.Equal(t, "Patched Echo", echo.Title, "cli/custom PatchTool should change the generated title")

		custom := tools["custom_ping"]
		require.NotNil(t, custom, "cli/custom AddTools should expose custom_ping")

		res, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      "custom_ping",
			Arguments: map[string]any{"name": "bartolo"},
		})
		require.NoError(t, err, "CallTool custom_ping")
		require.False(t, res.IsError, "custom_ping should succeed")
		sc, ok := res.StructuredContent.(map[string]any)
		require.True(t, ok, "structuredContent type %T", res.StructuredContent)
		assert.Equal(t, "bartolo", sc["hello"])
	})

	t.Run("HTTPStatelessRoundTripIncludingCustomTool", func(t *testing.T) {
		httpCtx, httpCancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer httpCancel()

		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err, "reserve port")
		addr := ln.Addr().String()
		require.NoError(t, ln.Close())

		httpCmd := exec.Command(cliBin, "mcp", "--http", addr)
		httpCmd.Env = append(os.Environ(), "MCPE2E_API_KEY=test-token")
		httpCmd.Stderr = os.Stderr
		require.NoError(t, httpCmd.Start(), "start http mcp")
		t.Cleanup(func() {
			_ = httpCmd.Process.Kill()
			_ = httpCmd.Wait()
		})

		endpoint := "http://" + addr
		client := mcp.NewClient(&mcp.Implementation{Name: "e2e-http", Version: "0"}, nil)
		var session *mcp.ClientSession
		var lastErr error
		for i := 0; i < 50; i++ {
			session, lastErr = client.Connect(httpCtx, &mcp.StreamableClientTransport{Endpoint: endpoint}, nil)
			if lastErr == nil {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		require.NoError(t, lastErr, "connect Streamable HTTP")
		t.Cleanup(func() { _ = session.Close() })

		lt, err := session.ListTools(httpCtx, nil)
		require.NoError(t, err, "HTTP ListTools")
		names := map[string]bool{}
		for _, tool := range lt.Tools {
			names[tool.Name] = true
		}
		assert.True(t, names["echo_echo"], "generated tool on HTTP")
		assert.True(t, names["custom_ping"], "custom tool on HTTP")

		res, err := session.CallTool(httpCtx, &mcp.CallToolParams{
			Name:      "custom_ping",
			Arguments: map[string]any{"name": "http"},
		})
		require.NoError(t, err, "HTTP CallTool custom_ping")
		require.False(t, res.IsError)
		sc, ok := res.StructuredContent.(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "http", sc["hello"])
	})
}
