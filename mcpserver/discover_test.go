package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// discoveryFixture lists echo_echo and leaves echo_delete reachable only
// through the meta-tools.
func discoveryFixture(t *testing.T, f filterOptions) []ToolDef {
	t.Helper()
	cfg := &config{keep: []string{"echo_echo"}, discover: true}
	_, listed, err := prepareTools(generatedEchoTools, cfg, f)
	if err != nil {
		t.Fatal(err)
	}
	return listed
}

func toolNames(defs []ToolDef) string {
	names := make([]string, len(defs))
	for i, d := range defs {
		names[i] = d.Name
	}
	return strings.Join(names, ",")
}

func TestDiscoverToolsListsMetaToolsAfterKeptOnes(t *testing.T) {
	got := toolNames(discoveryFixture(t, filterOptions{}))
	want := "echo_echo,search_operations,describe_operation,call_read_operation,call_operation"
	if got != want {
		t.Errorf("listed = %s, want %s", got, want)
	}

	// --read-only drops call_operation and keeps writes out of the catalog.
	if got := toolNames(discoveryFixture(t, filterOptions{readOnly: true})); strings.Contains(got, "call_operation,") || strings.HasSuffix(got, ",call_operation") {
		t.Errorf("--read-only must not list call_operation: %s", got)
	}
}

func TestDiscoveryFindsDescribesAndCallsHiddenTools(t *testing.T) {
	cs := newSession(t, discoveryFixture(t, filterOptions{}))
	call := func(name string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return res
	}

	found := call(searchOperationsTool, map[string]any{"query": "delete"})
	if !strings.Contains(textOf(t, found), `"name":"echo_delete"`) {
		t.Errorf("search should find the hidden tool: %s", textOf(t, found))
	}

	described := call(describeOperationTool, map[string]any{"name": "echo_delete"})
	var desc map[string]any
	if err := json.Unmarshal([]byte(textOf(t, described)), &desc); err != nil || desc["input_schema"] == nil || desc["destructive"] != true {
		t.Errorf("describe = %s", textOf(t, described))
	}

	// A write goes through call_operation, never call_read_operation.
	if res := call(callReadOperationTool, map[string]any{"name": "echo_delete", "arguments": map[string]any{"id": "x"}}); !res.IsError {
		t.Errorf("call_read_operation must refuse a write")
	}
	if res := call(callOperationTool, map[string]any{"name": "echo_delete", "arguments": map[string]any{"id": "x"}}); res.IsError {
		t.Errorf("call_operation failed: %s", textOf(t, res))
	}

	// Arguments are validated against the operation's own schema.
	if res := call(callReadOperationTool, map[string]any{"name": "echo_echo", "arguments": map[string]any{}}); !res.IsError || !strings.Contains(textOf(t, res), "invalid arguments") {
		t.Errorf("a missing required argument should be refused: %s", textOf(t, res))
	}
	if res := call(callOperationTool, map[string]any{"name": "nope"}); !res.IsError {
		t.Errorf("an unknown operation should be refused")
	}
}

func TestDiscoveryWithoutQueryListsGroups(t *testing.T) {
	cs := newSession(t, discoveryFixture(t, filterOptions{}))
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: searchOperationsTool, Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(textOf(t, res), `"group":"echo"`) {
		t.Errorf("an empty search should list the groups: %s", textOf(t, res))
	}
}
