package main

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	yamlv3 "gopkg.in/yaml.v3"
)

// maxExampleDepth bounds schema recursion so deeply nested or self-referential
// request schemas produce a readable example instead of megabytes of output.
const maxExampleDepth = 8

// buildExampleBody returns the example request body for a media type: the
// curated media-type-level example when the spec has one, otherwise a value
// synthesized from the request schema. Returns "" when no example can be
// produced.
func buildExampleBody(mediaType string, item *openapi3.MediaType) string {
	if item == nil {
		return ""
	}

	if item.Example != nil {
		return renderExampleBody(mediaType, item.Example)
	}

	if len(item.Examples) > 0 {
		names := make([]string, 0, len(item.Examples))
		for name := range item.Examples {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			ex := item.Examples[name]
			if ex != nil && ex.Value != nil && ex.Value.Value != nil {
				return renderExampleBody(mediaType, ex.Value.Value)
			}
		}
	}

	if item.Schema == nil || item.Schema.Value == nil {
		return ""
	}
	value, ok := synthesizeExample(item.Schema.Value, "", 0, map[*openapi3.Schema]bool{})
	if !ok {
		return ""
	}
	if m, isMap := value.(map[string]interface{}); isMap && len(m) == 0 {
		// A bare {} tells the user nothing; treat it as "no example" so the
		// flag is not advertised.
		return ""
	}
	return renderExampleBody(mediaType, value)
}

// synthesizeExample builds an example value for a schema. ok=false means
// nothing useful could be produced (nil schema, cycle, exceeded depth).
func synthesizeExample(schema *openapi3.Schema, propName string, depth int, visited map[*openapi3.Schema]bool) (interface{}, bool) {
	if schema == nil {
		return nil, false
	}

	if v, ok := explicitExampleValue(schema); ok {
		return v, true
	}

	if visited[schema] || depth > maxExampleDepth {
		if scalarType(schema) != "" {
			return scalarPlaceholder(schema, propName), true
		}
		return nil, false
	}
	visited[schema] = true
	defer delete(visited, schema)

	// Unions: collapse nullable wrappers to their single real branch, else
	// synthesize from the first non-null branch only — an example must match
	// one branch, not a blend of all of them.
	if len(schema.OneOf)+len(schema.AnyOf) > 0 {
		if eff, _ := effectiveBodySchema(schema); eff != nil && eff != schema {
			return synthesizeExample(eff, propName, depth, visited)
		}
		branches := append([]*openapi3.SchemaRef{}, schema.OneOf...)
		branches = append(branches, schema.AnyOf...)
		for _, branch := range branches {
			if branch == nil || branch.Value == nil {
				continue
			}
			if branch.Value.Type != nil && branch.Value.Type.Is("null") {
				continue
			}
			if v, ok := synthesizeExample(branch.Value, propName, depth+1, visited); ok {
				return v, true
			}
		}
		return nil, false
	}

	if len(schema.AllOf) > 0 {
		return synthesizeExample(mergeAllOf(schema), propName, depth, visited)
	}

	// Strip `type: [X, "null"]` so the concrete type checks below apply.
	eff, _ := effectiveBodySchema(schema)
	if eff == nil {
		return nil, false
	}
	if eff != schema {
		schema = eff
		if v, ok := explicitExampleValue(schema); ok {
			return v, true
		}
	}

	switch {
	case len(schema.Properties) > 0:
		return synthesizeObjectExample(schema, depth, visited), true
	case schema.Type != nil && schema.Type.Is("object"):
		return map[string]interface{}{}, true
	case schema.Type != nil && schema.Type.Is("array"):
		if schema.Items != nil && schema.Items.Value != nil {
			if item, ok := synthesizeExample(schema.Items.Value, propName, depth+1, visited); ok {
				return []interface{}{item}, true
			}
		}
		return []interface{}{}, true
	case scalarType(schema) != "":
		return scalarPlaceholder(schema, propName), true
	}

	return nil, false
}

// synthesizeObjectExample builds an example object: required properties
// always, optional properties only when they carry an explicit example or
// default. When that selection comes up empty the object would read as `{}`,
// so it falls back to including every property of this object.
func synthesizeObjectExample(schema *openapi3.Schema, depth int, visited map[*openapi3.Schema]bool) map[string]interface{} {
	required := map[string]bool{}
	for _, name := range schema.Required {
		required[name] = true
	}

	names := make([]string, 0, len(schema.Properties))
	for name := range schema.Properties {
		names = append(names, name)
	}
	sort.Strings(names)

	build := func(includeAll bool) map[string]interface{} {
		out := map[string]interface{}{}
		for _, name := range names {
			ref := schema.Properties[name]
			if ref == nil || ref.Value == nil {
				continue
			}
			if !includeAll && !required[name] && !hasExplicitValue(ref.Value) {
				continue
			}
			if v, ok := synthesizeExample(ref.Value, name, depth+1, visited); ok {
				out[name] = v
			}
		}
		return out
	}

	out := build(false)
	if len(out) == 0 {
		out = build(true)
	}
	return out
}

// explicitExampleValue returns a value the spec author wrote down themselves:
// an example, a default, or the first non-null enum member.
func explicitExampleValue(schema *openapi3.Schema) (interface{}, bool) {
	if schema.Example != nil {
		return schema.Example, true
	}
	if schema.Default != nil {
		return schema.Default, true
	}
	for _, v := range schema.Enum {
		if v != nil {
			return v, true
		}
	}
	return nil, false
}

func hasExplicitValue(schema *openapi3.Schema) bool {
	_, ok := explicitExampleValue(schema)
	return ok
}

func scalarPlaceholder(schema *openapi3.Schema, propName string) interface{} {
	switch scalarType(schema) {
	case "string":
		switch schema.Format {
		case "date-time":
			return "2024-01-01T00:00:00Z"
		case "date":
			return "2024-01-01"
		case "email":
			return "user@example.com"
		case "uri", "url":
			return "https://example.com"
		case "uuid":
			return "00000000-0000-0000-0000-000000000000"
		}
		if propName != "" {
			return propName
		}
		return "string"
	case "bool":
		return false
	case "int64":
		if schema.Min != nil {
			return int64(*schema.Min)
		}
		return int64(0)
	case "float64":
		if schema.Min != nil {
			return *schema.Min
		}
		return float64(0)
	}
	return nil
}

// renderExampleBody marshals an example value for the media type; "" = none.
func renderExampleBody(mediaType string, value interface{}) string {
	if value == nil {
		return ""
	}
	switch {
	case strings.Contains(mediaType, "json"):
		b, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return ""
		}
		return string(b)
	case strings.Contains(mediaType, "yaml"):
		b, err := yamlv3.Marshal(value)
		if err != nil {
			return ""
		}
		return strings.TrimRight(string(b), "\n")
	}
	return ""
}
