package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/orq-ai/bartolo/cli"
)

func noopTool(name string) ToolDef {
	return ToolDef{
		Name:        name,
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Handler:     func(context.Context, map[string]any) (any, error) { return map[string]any{}, nil },
	}
}

// sessionWith connects a client to a server built from info.
func sessionWith(t *testing.T, info serverInfo, defs []ToolDef) *mcp.ClientSession {
	t.Helper()
	server, err := buildServer(info, defs)
	if err != nil {
		t.Fatalf("buildServer: %v", err)
	}
	serverT, clientT := mcp.NewInMemoryTransports()
	ss, err := server.Connect(context.Background(), serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(context.Background(), clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// A request for a feature the server does not declare must fail: a reply
// tells the client the feature exists while discover says it does not
// (SEP-2575). Prompts and resources are declared, with none to list.
func TestUndeclaredFeaturesAreMethodNotFound(t *testing.T) {
	cs := sessionWith(t, serverInfo{name: "t"}, []ToolDef{noopTool("noop")})
	ctx := context.Background()

	caps := cs.InitializeResult().Capabilities
	if caps.Prompts == nil || caps.Resources == nil || caps.Completions != nil {
		t.Errorf("capabilities = %+v, want tools, prompts and resources only", caps)
	}
	if res, err := cs.ListPrompts(ctx, nil); err != nil || len(res.Prompts) != 0 {
		t.Errorf("prompts/list = %v, %v; want an empty list", res, err)
	}
	if res, err := cs.ListResources(ctx, nil); err != nil || len(res.Resources) != 0 {
		t.Errorf("resources/list = %v, %v; want an empty list", res, err)
	}

	_, errComplete := cs.Complete(ctx, &mcp.CompleteParams{
		Ref:      &mcp.CompleteReference{Type: "ref/prompt", Name: "x"},
		Argument: mcp.CompleteParamsArgument{Name: "a", Value: "b"},
	})
	var rpcErr *jsonrpc.Error
	if !errors.As(errComplete, &rpcErr) || rpcErr.Code != jsonrpc.CodeMethodNotFound {
		t.Errorf("completion/complete: err = %v, want Method not found", errComplete)
	}

	// Methods only older protocol versions send, checked at the middleware.
	next := func(context.Context, string, mcp.Request) (mcp.Result, error) { return nil, nil }
	for _, method := range []string{"resources/subscribe", "resources/unsubscribe", "logging/setLevel"} {
		_, err := rejectUndeclaredMethods(next)(ctx, method, nil)
		if !errors.As(err, &rpcErr) || rpcErr.Code != jsonrpc.CodeMethodNotFound {
			t.Errorf("%s: err = %v, want Method not found", method, err)
		}
	}
}

func TestMetadataReachesServerInfo(t *testing.T) {
	info := serverInfo{name: "acme", version: "1.2.3", settings: settings{metadata: Metadata{
		Title:      "Acme",
		WebsiteURL: "https://acme.example",
		Icons:      []Icon{{Src: "https://acme.example/icon.png", MIMEType: "image/png", Sizes: []string{"48x48"}}},
	}}}
	got := sessionWith(t, info, []ToolDef{noopTool("noop")}).InitializeResult().ServerInfo
	if got.Title != "Acme" || got.WebsiteURL != "https://acme.example" || len(got.Icons) != 1 || got.Icons[0].Source != "https://acme.example/icon.png" {
		t.Errorf("serverInfo = %+v", got)
	}
}

func TestListTTLIsConfigurable(t *testing.T) {
	ttl := 90 * time.Second
	cs := sessionWith(t, serverInfo{name: "t", settings: settings{listTTL: &ttl}}, []ToolDef{noopTool("noop")})
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.TTLMs != 90000 {
		t.Errorf("ttlMs = %d, want 90000", res.TTLMs)
	}
}

func TestProtocolVersionsMustBeSupported(t *testing.T) {
	bad := serverInfo{name: "t", settings: settings{protocolVersions: []string{"1999-01-01"}}}
	if _, err := buildServer(bad, nil); err == nil || !strings.Contains(err.Error(), "1999-01-01") {
		t.Errorf("err = %v, want one naming the unsupported version", err)
	}
	ok := serverInfo{name: "t", settings: settings{protocolVersions: []string{"2026-07-28"}}}
	if _, err := buildServer(ok, nil); err != nil {
		t.Errorf("a supported version was refused: %v", err)
	}
}

// traceTool reports the outbound headers its context carries.
func traceTool() ToolDef {
	return ToolDef{
		Name:        "trace",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(ctx context.Context, _ map[string]any) (any, error) {
			return map[string]any{"headers": cli.RequestHeaders(ctx)}, nil
		},
	}
}

func TestTraceContextReachesOutboundRequests(t *testing.T) {
	const parent = "00-0af7651916cd43dd8448eb211c80319c-00f067aa0ba902b7-01"
	call := func(t *testing.T, s settings, meta mcp.Meta) map[string]any {
		t.Helper()
		cs := sessionWith(t, serverInfo{name: "t", settings: s}, []ToolDef{traceTool()})
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "trace", Arguments: map[string]any{}, Meta: meta})
		if err != nil {
			t.Fatal(err)
		}
		headers, _ := res.StructuredContent.(map[string]any)["headers"].(map[string]any)
		return headers
	}

	got := call(t, settings{}, mcp.Meta{"traceparent": parent, "tracestate": "vendor=1"})
	if got["traceparent"] != parent || got["tracestate"] != "vendor=1" {
		t.Errorf("headers = %v, want the _meta trace context", got)
	}
	if got := call(t, settings{}, mcp.Meta{"traceparent": "not-a-traceparent"}); len(got) != 0 {
		t.Errorf("a malformed traceparent must not be propagated: %v", got)
	}
	off := false
	if got := call(t, settings{traceContext: &off}, mcp.Meta{"traceparent": parent}); len(got) != 0 {
		t.Errorf("trace context was propagated while disabled: %v", got)
	}
}

// validateCard checks a served card against the official Server Card schema.
func validateCard(t *testing.T, card map[string]any) {
	t.Helper()
	data, err := os.ReadFile("testdata/server-card.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	schema.Ref = "#/$defs/ServerCard"
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatalf("resolve schema: %v", err)
	}
	if err := resolved.Validate(card); err != nil {
		t.Errorf("the card does not match the Server Card schema: %v\n%v", err, card)
	}
}

func TestServerCardIsServedAndValid(t *testing.T) {
	info := serverInfo{name: "acme", version: "1.2.3", settings: settings{
		metadata:   Metadata{Title: "Acme", Description: "Acme's API as MCP tools.", WebsiteURL: "https://acme.example"},
		serverCard: &ServerCard{Name: "example.acme/api", Endpoint: "/v2/mcp", Repository: "https://github.com/acme/mcp"},
	}}
	if err := info.settings.validate(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(serverCardHandler(info, authModeForward))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v2/mcp/server-card", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "mcp.acme.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "application/mcp-server-card+json" {
		t.Errorf("Content-Type = %q", ct)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("cards are public: any origin may read them")
	}
	var card map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		t.Fatal(err)
	}
	validateCard(t, card)

	remote := card["remotes"].([]any)[0].(map[string]any)
	if remote["url"] != "https://mcp.acme.example/v2/mcp" {
		t.Errorf("remote url = %v, want the forwarded public URL", remote["url"])
	}
	if headers, _ := remote["headers"].([]any); len(headers) != 1 {
		t.Errorf("forward auth should advertise the Authorization header: %v", remote["headers"])
	}
}

func TestServerCardConfigIsValidated(t *testing.T) {
	for name, s := range map[string]settings{
		"name without namespace": {metadata: Metadata{Description: "x"}, serverCard: &ServerCard{Name: "acme"}},
		"missing description":    {serverCard: &ServerCard{Name: "example.acme/api"}},
		"relative endpoint":      {metadata: Metadata{Description: "x"}, serverCard: &ServerCard{Name: "example.acme/api", Endpoint: "mcp"}},
		"bad public URL":         {metadata: Metadata{Description: "x"}, serverCard: &ServerCard{Name: "example.acme/api", PublicURL: "mcp.acme"}},
	} {
		if err := s.validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// The SDK panics on an invalid x-mcp-header annotation; a custom tool carrying
// one must fail registration with an error instead.
func TestInvalidHeaderAnnotationIsAnError(t *testing.T) {
	tool := noopTool("bad_header")
	tool.InputSchema = json.RawMessage(`{"type":"object","properties":{"region":{"type":"string","x-mcp-header":"Bad Header"}}}`)
	if _, err := buildServer(serverInfo{name: "t"}, []ToolDef{tool}); err == nil {
		t.Error("expected an error for an invalid header name")
	}
}
