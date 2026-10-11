package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func generatedEchoTools() []ToolDef {
	return []ToolDef{
		{
			Name:        "echo_echo",
			Title:       "Echo",
			Description: "generated echo",
			Group:       "echo",
			InputSchema: json.RawMessage(echoSchema),
			ReadOnly:    true,
			Idempotent:  true,
			Handler: func(ctx context.Context, args map[string]any) (any, error) {
				return map[string]any{"echoed": args["name"]}, nil
			},
		},
		{
			Name:        "echo_delete",
			Title:       "Delete",
			Description: "generated delete",
			Group:       "echo",
			InputSchema: json.RawMessage(`{"type":"object"}`),
			Destructive: true,
			Handler: func(ctx context.Context, args map[string]any) (any, error) {
				return map[string]any{"ok": true}, nil
			},
		},
	}
}

func TestApplyCustomizationsAddRemovePatchWrap(t *testing.T) {
	t.Cleanup(ResetForTest)

	var wrapped bool
	cfg := &config{}
	AddTools(ToolDef{
		Name:        "custom_ping",
		Title:       "Ping",
		Description: "user-owned",
		Group:       "custom",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		ReadOnly:    true,
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			return map[string]any{"pong": true}, nil
		},
	})(cfg)
	RemoveTools("echo_delete")(cfg)
	PatchTool("echo_echo", func(td *ToolDef) {
		td.Title = "Patched Echo"
		td.Description = "patched"
	})(cfg)
	WrapAll(func(next ToolHandler) ToolHandler {
		return func(ctx context.Context, args map[string]any) (any, error) {
			wrapped = true
			return next(ctx, args)
		}
	})(cfg)

	got, err := applyCustomizations(generatedEchoTools(), cfg)
	if err != nil {
		t.Fatalf("applyCustomizations: %v", err)
	}

	names := make([]string, 0, len(got))
	byName := map[string]ToolDef{}
	for _, d := range got {
		names = append(names, d.Name)
		byName[d.Name] = d
	}
	if strings.Join(names, ",") != "echo_echo,custom_ping" {
		t.Errorf("names = %v, want echo_echo,custom_ping", names)
	}
	if byName["echo_echo"].Title != "Patched Echo" {
		t.Errorf("echo title = %q, want Patched Echo", byName["echo_echo"].Title)
	}

	if _, err := byName["custom_ping"].Handler(context.Background(), map[string]any{}); err != nil {
		t.Fatalf("custom handler: %v", err)
	}
	if !wrapped {
		t.Fatal("WrapAll did not wrap extra tool handler")
	}
}

func TestApplyCustomizationsAddCollision(t *testing.T) {
	cfg := &config{}
	AddTools(ToolDef{Name: "echo_echo"})(cfg)
	if _, err := applyCustomizations(generatedEchoTools(), cfg); err == nil {
		t.Fatal("expected collision error")
	}
}

func TestApplyCustomizationsPatchMissing(t *testing.T) {
	cfg := &config{}
	PatchTool("nope", func(*ToolDef) {})(cfg)
	if _, err := applyCustomizations(generatedEchoTools(), cfg); err == nil {
		t.Fatal("expected missing patch error")
	}
}

func TestApplyCustomizationsPatchAfterRemoveFails(t *testing.T) {
	cfg := &config{}
	RemoveTools("echo_echo")(cfg)
	PatchTool("echo_echo", func(*ToolDef) {})(cfg)
	if _, err := applyCustomizations(generatedEchoTools(), cfg); err == nil {
		t.Fatal("expected patch of removed tool to fail")
	}
}

func TestUseIsAppliedLazilyOnList(t *testing.T) {
	t.Cleanup(ResetForTest)

	cmd := Command("app", "1.0.0", generatedEchoTools)
	Use(
		AddTools(ToolDef{
			Name:        "custom_ping",
			Title:       "Ping",
			ReadOnly:    true,
			InputSchema: json.RawMessage(`{"type":"object"}`),
			Handler: func(ctx context.Context, args map[string]any) (any, error) {
				return map[string]any{"pong": true}, nil
			},
		}),
		RemoveTools("echo_delete"),
		PatchTool("echo_echo", func(td *ToolDef) { td.Title = "Patched Echo" }),
	)

	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"list"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("mcp list: %v", err)
	}

	var manifest []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &manifest); err != nil {
		t.Fatalf("manifest JSON: %v\n%s", err, buf.String())
	}
	got := map[string]string{}
	for _, row := range manifest {
		name, _ := row["name"].(string)
		title, _ := row["title"].(string)
		got[name] = title
	}
	if _, ok := got["echo_delete"]; ok {
		t.Fatal("removed tool still listed")
	}
	if got["echo_echo"] != "Patched Echo" {
		t.Errorf("echo title = %q, want Patched Echo", got["echo_echo"])
	}
	if _, ok := got["custom_ping"]; !ok {
		t.Fatal("custom tool missing from list")
	}
}

func TestListPersistentReadOnlyFilter(t *testing.T) {
	t.Cleanup(ResetForTest)

	cmd := Command("app", "1.0.0", generatedEchoTools)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"list", "--read-only"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("mcp list --read-only: %v", err)
	}

	var manifest []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &manifest); err != nil {
		t.Fatalf("manifest JSON: %v\n%s", err, buf.String())
	}
	if len(manifest) != 1 || manifest[0]["name"] != "echo_echo" {
		t.Fatalf("read-only list = %v, want only echo_echo", manifest)
	}
}

func TestUseAddCollisionSurfacesOnList(t *testing.T) {
	t.Cleanup(ResetForTest)

	cmd := Command("app", "1.0.0", generatedEchoTools)
	Use(AddTools(ToolDef{Name: "echo_echo"}))

	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"list"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected list to fail on name collision")
	}
}

func TestResetForTestClearsUse(t *testing.T) {
	t.Cleanup(ResetForTest)
	Use(RemoveTools("echo_echo"))
	ResetForTest()

	_, selected, err := prepareTools(generatedEchoTools, &config{}, filterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 2 {
		t.Fatalf("after reset got %d tools, want 2", len(selected))
	}
}

func TestCustomToolRoundTripOverInMemory(t *testing.T) {
	t.Cleanup(ResetForTest)

	defs, err := applyCustomizations(generatedEchoTools(), func() *config {
		cfg := &config{}
		AddTools(ToolDef{
			Name:        "custom_ping",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`),
			ReadOnly:    true,
			Handler: func(ctx context.Context, args map[string]any) (any, error) {
				return map[string]any{"hello": args["name"]}, nil
			},
		})(cfg)
		return cfg
	}())
	if err != nil {
		t.Fatal(err)
	}

	cs := newSession(t, defs)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "custom_ping",
		Arguments: map[string]any{"name": "bartolo"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected IsError: %s", textOf(t, res))
	}
	sc, ok := res.StructuredContent.(map[string]any)
	if !ok || sc["hello"] != "bartolo" {
		t.Fatalf("structured = %v", res.StructuredContent)
	}
}

func TestApplyCustomizationsRejectsInvalidCustomTools(t *testing.T) {
	handler := func(context.Context, map[string]any) (any, error) { return nil, nil }
	for name, extra := range map[string]ToolDef{
		"bad name":   {Name: "has space", Handler: handler},
		"no handler": {Name: "no_handler"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := &config{extra: []ToolDef{extra}}
			if _, err := applyCustomizations(generatedEchoTools(), cfg); err == nil {
				t.Error("expected an error naming the invalid tool")
			}
		})
	}
}
