package main

import (
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/stretchr/testify/assert"
)

func synth(t *testing.T, schema *openapi3.Schema) (interface{}, bool) {
	t.Helper()
	return synthesizeExample(schema, "", 0, map[*openapi3.Schema]bool{})
}

func typed(name string) *openapi3.Types {
	return &openapi3.Types{name}
}

func TestSynthesizeExampleExplicitValues(t *testing.T) {
	v, ok := synth(t, &openapi3.Schema{Type: typed("string"), Example: "from-example", Default: "from-default"})
	assert.True(t, ok)
	assert.Equal(t, "from-example", v)

	v, ok = synth(t, &openapi3.Schema{Type: typed("string"), Default: "from-default"})
	assert.True(t, ok)
	assert.Equal(t, "from-default", v)

	v, ok = synth(t, &openapi3.Schema{Type: typed("string"), Enum: []interface{}{nil, "first", "second"}})
	assert.True(t, ok)
	assert.Equal(t, "first", v)
}

func TestSynthesizeExampleScalarPlaceholders(t *testing.T) {
	v, ok := synthesizeExample(&openapi3.Schema{Type: typed("string")}, "display_name", 0, map[*openapi3.Schema]bool{})
	assert.True(t, ok)
	assert.Equal(t, "display_name", v)

	v, ok = synth(t, &openapi3.Schema{Type: typed("string"), Format: "date-time"})
	assert.True(t, ok)
	assert.Equal(t, "2024-01-01T00:00:00Z", v)

	v, ok = synth(t, &openapi3.Schema{Type: typed("string"), Format: "email"})
	assert.True(t, ok)
	assert.Equal(t, "user@example.com", v)

	min := 3.0
	v, ok = synth(t, &openapi3.Schema{Type: typed("integer"), Min: &min})
	assert.True(t, ok)
	assert.Equal(t, int64(3), v)

	v, ok = synth(t, &openapi3.Schema{Type: typed("boolean")})
	assert.True(t, ok)
	assert.Equal(t, false, v)

	// JSON Schema 3.1 nullable form still yields the concrete placeholder.
	v, ok = synth(t, &openapi3.Schema{Type: &openapi3.Types{"integer", "null"}})
	assert.True(t, ok)
	assert.Equal(t, int64(0), v)
}

func TestSynthesizeExampleObjectSelection(t *testing.T) {
	schema := &openapi3.Schema{
		Type:     typed("object"),
		Required: []string{"key"},
		Properties: openapi3.Schemas{
			"key":      &openapi3.SchemaRef{Value: &openapi3.Schema{Type: typed("string")}},
			"path":     &openapi3.SchemaRef{Value: &openapi3.Schema{Type: typed("string"), Example: "Default"}},
			"internal": &openapi3.SchemaRef{Value: &openapi3.Schema{Type: typed("string")}},
		},
	}

	v, ok := synth(t, schema)
	assert.True(t, ok)
	// Required props always, optional props only with an explicit example;
	// "internal" is optional and placeholder-only, so it is left out.
	assert.Equal(t, map[string]interface{}{"key": "key", "path": "Default"}, v)
}

func TestSynthesizeExampleObjectFallsBackToAllProps(t *testing.T) {
	schema := &openapi3.Schema{
		Type: typed("object"),
		Properties: openapi3.Schemas{
			"name":  &openapi3.SchemaRef{Value: &openapi3.Schema{Type: typed("string")}},
			"count": &openapi3.SchemaRef{Value: &openapi3.Schema{Type: typed("integer")}},
		},
	}

	v, ok := synth(t, schema)
	assert.True(t, ok)
	assert.Equal(t, map[string]interface{}{"name": "name", "count": int64(0)}, v)
}

func TestSynthesizeExampleArray(t *testing.T) {
	schema := &openapi3.Schema{
		Type: typed("array"),
		Items: &openapi3.SchemaRef{
			Value: &openapi3.Schema{Type: typed("string"), Example: "tag"},
		},
	}

	v, ok := synth(t, schema)
	assert.True(t, ok)
	assert.Equal(t, []interface{}{"tag"}, v)
}

func TestSynthesizeExampleUnionsUseFirstBranch(t *testing.T) {
	schema := &openapi3.Schema{
		AnyOf: openapi3.SchemaRefs{
			{Value: &openapi3.Schema{Type: typed("string"), Example: "openai/gpt-4o"}},
			{Value: &openapi3.Schema{Type: typed("object"), Properties: openapi3.Schemas{
				"id": &openapi3.SchemaRef{Value: &openapi3.Schema{Type: typed("string")}},
			}}},
		},
	}

	v, ok := synth(t, schema)
	assert.True(t, ok)
	assert.Equal(t, "openai/gpt-4o", v)
}

func TestSynthesizeExampleNullableUnionCollapses(t *testing.T) {
	schema := &openapi3.Schema{
		AnyOf: openapi3.SchemaRefs{
			{Value: &openapi3.Schema{Type: typed("null")}},
			{Value: &openapi3.Schema{Type: typed("string"), Example: "value"}},
		},
	}

	v, ok := synth(t, schema)
	assert.True(t, ok)
	assert.Equal(t, "value", v)
}

func TestSynthesizeExampleAllOfMerges(t *testing.T) {
	schema := &openapi3.Schema{
		AllOf: openapi3.SchemaRefs{
			{Value: &openapi3.Schema{Type: typed("object"), Required: []string{"name"}, Properties: openapi3.Schemas{
				"name": &openapi3.SchemaRef{Value: &openapi3.Schema{Type: typed("string"), Example: "thing"}},
			}}},
			{Value: &openapi3.Schema{Type: typed("object"), Required: []string{"count"}, Properties: openapi3.Schemas{
				"count": &openapi3.SchemaRef{Value: &openapi3.Schema{Type: typed("integer"), Example: float64(2)}},
			}}},
		},
	}

	v, ok := synth(t, schema)
	assert.True(t, ok)
	assert.Equal(t, map[string]interface{}{"name": "thing", "count": float64(2)}, v)
}

func TestSynthesizeExampleTerminatesOnCycle(t *testing.T) {
	schema := &openapi3.Schema{
		Type:       typed("object"),
		Required:   []string{"child", "name"},
		Properties: openapi3.Schemas{},
	}
	schema.Properties["child"] = &openapi3.SchemaRef{Value: schema}
	schema.Properties["name"] = &openapi3.SchemaRef{Value: &openapi3.Schema{Type: typed("string")}}

	v, ok := synth(t, schema)
	assert.True(t, ok)
	// The self-referential property is dropped; the rest survives.
	assert.Equal(t, map[string]interface{}{"name": "name"}, v)
}

func TestSynthesizeExampleDepthCap(t *testing.T) {
	leaf := &openapi3.Schema{Type: typed("object"), Required: []string{"deep"}, Properties: openapi3.Schemas{
		"deep": &openapi3.SchemaRef{Value: &openapi3.Schema{Type: typed("string")}},
	}}
	schema := leaf
	for i := 0; i < maxExampleDepth+4; i++ {
		schema = &openapi3.Schema{Type: typed("object"), Required: []string{"nested"}, Properties: openapi3.Schemas{
			"nested": &openapi3.SchemaRef{Value: schema},
		}}
	}

	v, ok := synth(t, schema)
	assert.True(t, ok)
	assert.IsType(t, map[string]interface{}{}, v)
}

func TestBuildExampleBodyPrefersCuratedExample(t *testing.T) {
	item := &openapi3.MediaType{
		Example: map[string]interface{}{"input": "hello"},
		Schema: &openapi3.SchemaRef{Value: &openapi3.Schema{Type: typed("object"), Required: []string{"other"}, Properties: openapi3.Schemas{
			"other": &openapi3.SchemaRef{Value: &openapi3.Schema{Type: typed("string")}},
		}}},
	}

	assert.JSONEq(t, `{"input": "hello"}`, buildExampleBody("application/json", item))
}

func TestBuildExampleBodyPicksNamedExamplesDeterministically(t *testing.T) {
	item := &openapi3.MediaType{
		Examples: openapi3.Examples{
			"zebra": &openapi3.ExampleRef{Value: &openapi3.Example{Value: map[string]interface{}{"pick": "z"}}},
			"alpha": &openapi3.ExampleRef{Value: &openapi3.Example{Value: map[string]interface{}{"pick": "a"}}},
		},
	}

	for i := 0; i < 10; i++ {
		assert.JSONEq(t, `{"pick": "a"}`, buildExampleBody("application/json", item))
	}
}

func TestBuildExampleBodyStringExample(t *testing.T) {
	item := &openapi3.MediaType{Example: "just a string"}
	assert.Equal(t, `"just a string"`, buildExampleBody("application/json", item))
}

func TestBuildExampleBodyEmptyForFreeFormObject(t *testing.T) {
	has := true
	item := &openapi3.MediaType{
		Schema: &openapi3.SchemaRef{Value: &openapi3.Schema{
			Type:                 typed("object"),
			AdditionalProperties: openapi3.AdditionalProperties{Has: &has},
		}},
	}

	assert.Equal(t, "", buildExampleBody("application/json", item))
}

func TestProcessAPISetsBodyExample(t *testing.T) {
	doc := loadTestSpec(t, `
openapi: 3.1.0
info:
  title: Example API
  version: "1"
paths:
  /agents:
    post:
      operationId: CreateAgent
      summary: Create agent
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required:
                - key
                - role
              properties:
                key:
                  type: string
                  description: Unique identifier
                role:
                  type: string
                path:
                  type: string
                  example: Default
                notes:
                  type: string
      responses:
        "200":
          description: ok
`)

	api := ProcessAPI("example", doc)
	if len(api.Groups) != 1 || len(api.Groups[0].Operations) != 1 {
		t.Fatalf("expected 1 grouped operation, got %+v", api.Groups)
	}

	op := api.Groups[0].Operations[0]
	assert.JSONEq(t, `{"key": "key", "role": "role", "path": "Default"}`, op.BodyExample)
}
