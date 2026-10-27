// Package custom adds the conformance suite's fixture tools that bartolo's
// runtime can express, so those scenarios exercise its result, error, and
// schema handling instead of being skipped. conformance/run.sh copies this
// file into the generated fixture project. The generated tools themselves are
// what every other scenario lists and calls.
package custom

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/orq-ai/bartolo/mcpserver"
	"github.com/spf13/cobra"
)

// jsonSchema202012 is the conformance suite's SEP-1613 / SEP-2106 fixture: a
// server must list it with every 2020-12 keyword intact.
const jsonSchema202012 = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "$defs": {
    "address": {
      "$anchor": "addressDef",
      "type": "object",
      "properties": {"street": {"type": "string"}, "city": {"type": "string"}}
    }
  },
  "properties": {
    "name": {"type": "string"},
    "address": {"$ref": "#/$defs/address"},
    "contactMethod": {"type": "string", "enum": ["phone", "email"]},
    "phone": {"type": "string"},
    "email": {"type": "string"}
  },
  "allOf": [{"anyOf": [{"required": ["phone"]}, {"required": ["email"]}]}],
  "if": {"properties": {"contactMethod": {"const": "phone"}}, "required": ["contactMethod"]},
  "then": {"required": ["phone"]},
  "else": {"required": ["email"]},
  "additionalProperties": false
}`

func Register(root *cobra.Command) {
	_ = root
	mcpserver.Use(mcpserver.AddTools(
		mcpserver.ToolDef{
			Name:        "test_simple_text",
			Description: "Returns simple text content.",
			ReadOnly:    true,
			Handler: func(context.Context, map[string]any) (any, error) {
				return "This is a simple text response for testing.", nil
			},
		},
		mcpserver.ToolDef{
			Name:        "test_error_handling",
			Description: "Always fails.",
			ReadOnly:    true,
			Handler: func(context.Context, map[string]any) (any, error) {
				return nil, errors.New("This tool intentionally returns an error for testing")
			},
		},
		mcpserver.ToolDef{
			Name:        "json_schema_2020_12_tool",
			Description: "Tool with JSON Schema 2020-12 features",
			InputSchema: json.RawMessage(jsonSchema202012),
			ReadOnly:    true,
			Handler: func(context.Context, map[string]any) (any, error) {
				return map[string]any{"ok": true}, nil
			},
		},
	))
}
