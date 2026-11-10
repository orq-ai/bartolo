package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/orq-ai/bartolo/cli"
)

func apiRoutes() []ArgRoute {
	return []ArgRoute{
		{Prop: "id", WireName: "id", In: "path", Required: true},
		{Prop: "ids", WireName: "ids", In: "query", Required: true},
		{Prop: "archived", WireName: "archived", In: "query", Flag: "archived"},
		{Prop: "tags", WireName: "tags", In: "query", Flag: "tag-list"},
		{Prop: "name", WireName: "name", In: "body"},
		{Prop: "body_id", WireName: "id", In: "body"},
	}
}

func TestNewAPIRequestRoutesArguments(t *testing.T) {
	args := map[string]any{
		"id":       float64(42),
		"ids":      []any{"a", "b"},
		"archived": false,
		"tags":     []any{"x", float64(2)},
		"name":     "widget",
		"body_id":  "w-1",
	}
	req, err := newAPIRequest(context.Background(), args, apiRoutes())
	if err != nil {
		t.Fatal(err)
	}

	if got := req.Arg("path", "id"); got != "42" {
		t.Errorf("path arg = %q, want 42", got)
	}
	if got := req.Arg("query", "ids"); got != "a,b" {
		t.Errorf("array arg = %q, want the comma list a CLI flag takes", got)
	}

	// An explicit false is an argument the caller gave, not an absent one.
	if !cli.FlagPassed(req.Params, "archived") || req.Params.GetBool("archived") {
		t.Errorf("archived: passed=%v value=%v, want an explicit false", cli.FlagPassed(req.Params, "archived"), req.Params.Get("archived"))
	}
	if got := req.Params.GetString("tag-list"); got != "x,2" {
		t.Errorf("optional array = %q, want x,2 under the flag name", got)
	}

	var body map[string]any
	if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
		t.Fatalf("body %q: %v", req.Body, err)
	}
	if body["name"] != "widget" || body["id"] != "w-1" {
		t.Errorf("body = %v, want props under their wire names", body)
	}
}

func TestNewAPIRequestOmitsAbsentArguments(t *testing.T) {
	req, err := newAPIRequest(context.Background(), map[string]any{"id": "7"}, apiRoutes())
	if err != nil {
		t.Fatal(err)
	}
	if req.Body != "" {
		t.Errorf("body = %q, want none when no body args were given", req.Body)
	}
	if cli.FlagPassed(req.Params, "archived") {
		t.Errorf("an absent optional param must not be marked as passed")
	}
	if got := req.Arg("query", "ids"); got != "" {
		t.Errorf("absent required arg = %q, want empty", got)
	}
}

func TestNewAPIRequestWholeBody(t *testing.T) {
	routes := []ArgRoute{{Prop: "body", In: "body"}}
	req, err := newAPIRequest(context.Background(), map[string]any{"body": []any{"a", "b"}}, routes)
	if err != nil {
		t.Fatal(err)
	}
	if req.Body != `["a","b"]` {
		t.Errorf("body = %q, want the value encoded whole", req.Body)
	}
}

func TestAPIToolWrapsResult(t *testing.T) {
	def := APITool(ToolDef{Name: "list", WrapResult: true}, func(*APIRequest) (any, error) {
		return []any{"a"}, nil
	})
	out, err := def.Handler(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	wrapped, ok := out.(map[string]any)
	if !ok || len(wrapped["result"].([]any)) != 1 {
		t.Errorf("out = %#v, want {result: [a]}", out)
	}
}
