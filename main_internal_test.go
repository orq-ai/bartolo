package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/spf13/cobra"
)

func loadTestSpec(t *testing.T, spec string) *openapi3.T {
	t.Helper()

	doc, err := loadOpenAPIDocument([]byte(spec))
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}

	return doc
}

func TestNormalizeSpecName(t *testing.T) {
	cases := map[string]string{
		"openapi.yaml":    "openapi",
		"openapi.yml":     "openapi",
		"openapi.json":    "openapi",
		"orq-api.v1.json": "orq-api-v1",
	}

	for input, expected := range cases {
		if actual := normalizeSpecName(input); actual != expected {
			t.Fatalf("normalizeSpecName(%q) = %q, want %q", input, actual, expected)
		}
	}
}

func TestProcessAPIGroupsOperationsByTag(t *testing.T) {
	doc := loadTestSpec(t, `
openapi: 3.0.3
info:
  title: Grouped API
  version: "1"
tags:
  - name: Files
    description: File operations
paths:
  /files/{file_id}:
    get:
      operationId: FileGet
      summary: Get file
      tags:
        - Files
      parameters:
        - in: path
          name: file_id
          required: true
          schema:
            type: string
      responses:
        "200":
          description: ok
`)

	api := ProcessAPI("example", doc)
	if len(api.Groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(api.Groups))
	}

	group := api.Groups[0]
	if group.CLIName != "files" {
		t.Fatalf("expected group CLI name files, got %q", group.CLIName)
	}
	if len(group.Operations) != 1 {
		t.Fatalf("expected 1 grouped operation, got %d", len(group.Operations))
	}

	op := group.Operations[0]
	if op.Use != "get file-id" {
		t.Fatalf("expected grouped operation use %q, got %q", "get file-id", op.Use)
	}
	if op.CommandPath != "files get file-id" {
		t.Fatalf("expected grouped command path %q, got %q", "files get file-id", op.CommandPath)
	}
}

func TestProcessAPIRespectsCLIGroupExtension(t *testing.T) {
	doc := loadTestSpec(t, `
openapi: 3.0.3
info:
  title: Grouped API
  version: "1"
paths:
  /users/{user_id}:
    delete:
      operationId: DeleteUser
      summary: Delete user
      x-cli-group: admin
      parameters:
        - in: path
          name: user_id
          required: true
          schema:
            type: string
      responses:
        "204":
          description: ok
`)

	api := ProcessAPI("example", doc)
	if len(api.Groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(api.Groups))
	}

	group := api.Groups[0]
	if group.CLIName != "admin" {
		t.Fatalf("expected group CLI name admin, got %q", group.CLIName)
	}
	if len(group.Operations) != 1 {
		t.Fatalf("expected 1 grouped operation, got %d", len(group.Operations))
	}

	op := group.Operations[0]
	if op.CommandPath != "admin delete-user user-id" {
		t.Fatalf("unexpected grouped command path %q", op.CommandPath)
	}
}

func TestProcessAPITrimsGroupSuffixFromLeafName(t *testing.T) {
	doc := loadTestSpec(t, `
openapi: 3.0.3
info:
  title: Grouped API
  version: "1"
tags:
  - name: Files
paths:
  /files:
    get:
      operationId: ListFiles
      summary: List files
      tags:
        - Files
      responses:
        "200":
          description: ok
`)

	api := ProcessAPI("example", doc)
	op := api.Groups[0].Operations[0]
	if op.Use != "list" {
		t.Fatalf("expected grouped operation use %q, got %q", "list", op.Use)
	}
	if !op.IsList {
		t.Fatal("collection GET operation should use list formatting")
	}
}

func TestProcessAPIReadsListFieldsExtension(t *testing.T) {
	doc := loadTestSpec(t, `
openapi: 3.0.3
info:
  title: List fields API
  version: "1"
paths:
  /files:
    get:
      operationId: listFiles
      x-cli-list-fields:
        - name
        - id
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: array
                items:
                  type: object
                  properties:
                    id:
                      type: string
                    name:
                      type: string
`)

	api := ProcessAPI("example", doc)
	var op *Operation
	if len(api.Operations) > 0 {
		op = api.Operations[0]
	} else if len(api.Groups) > 0 && len(api.Groups[0].Operations) > 0 {
		op = api.Groups[0].Operations[0]
	}
	if op == nil {
		t.Fatal("expected generated list operation")
	}
	if got := strings.Join(op.ListFields, ","); got != "name,id" {
		t.Fatalf("unexpected list fields %q", got)
	}
	if !op.IsList {
		t.Fatal("collection GET operation with list fields should use list formatting")
	}
}

func TestProcessAPIRecognizesNestedCollectionResponse(t *testing.T) {
	doc := loadTestSpec(t, `
openapi: 3.0.3
info:
  title: Nested collection API
  version: "1"
paths:
  /folders/{folder_id}/files:
    get:
      operationId: listFolderFiles
      parameters:
        - name: folder_id
          in: path
          required: true
          schema:
            type: string
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: array
                items:
                  type: object
                  properties:
                    id:
                      type: string
`)

	api := ProcessAPI("example", doc)
	var op *Operation
	if len(api.Operations) > 0 {
		op = api.Operations[0]
	} else if len(api.Groups) > 0 && len(api.Groups[0].Operations) > 0 {
		op = api.Groups[0].Operations[0]
	}
	if op == nil {
		t.Fatal("expected generated nested collection operation")
	}
	if !op.IsList {
		t.Fatal("nested collection GET operation should use list formatting")
	}
}

func TestInferGroupedLeafNameNormalizesCommonPatterns(t *testing.T) {
	cases := []struct {
		name     string
		method   string
		path     string
		group    string
		explicit bool
		expected string
	}{
		{"GetAllPrompts", "get", "/v2/prompts", "prompts", false, "list"},
		{"FindOnePrompt", "get", "/v2/prompts/{prompt_id}", "prompts", false, "get"},
		{"CreatePromptVersion", "post", "/v2/prompts/{prompt_id}/versions", "prompts", false, "create-version"},
		{"post-v2-logs-query", "post", "/v2/logs/query", "logs", false, "query"},
		{"get-v2-logs-id", "get", "/v2/logs/{log_id}", "logs", false, "get"},
	}

	for _, tc := range cases {
		actual := inferGroupedLeafName(tc.name, tc.method, tc.path, tc.group, tc.explicit)
		if actual != tc.expected {
			t.Fatalf("inferGroupedLeafName(%q, %q, %q, %q) = %q, want %q", tc.name, tc.method, tc.path, tc.group, actual, tc.expected)
		}
	}
}

func TestProcessAPIFallsBackToPathGroupWhenTagsMissing(t *testing.T) {
	doc := loadTestSpec(t, `
openapi: 3.0.3
info:
  title: Grouped API
  version: "1"
paths:
  /v2/human-evals/{id}:
    get:
      summary: Retrieve human eval
      parameters:
        - in: path
          name: id
          required: true
          schema:
            type: string
      responses:
        "200":
          description: ok
`)

	api := ProcessAPI("example", doc)
	if len(api.Groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(api.Groups))
	}

	group := api.Groups[0]
	if group.CLIName != "human-evals" {
		t.Fatalf("expected group CLI name human-evals, got %q", group.CLIName)
	}

	op := group.Operations[0]
	if op.CommandPath != "human-evals get id" {
		t.Fatalf("expected grouped command path %q, got %q", "human-evals get id", op.CommandPath)
	}
}

func TestResolveInitConfigUsesFlagsWithoutPrompting(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().Bool("interactive", false, "")
	cmd.Flags().String("module-path", "github.com/acme/demo-cli", "")
	cmd.Flags().String("api-key-env-var", "MY_TEAM_TOKEN", "")
	cmd.Flags().String("default-format", "yaml", "")

	config, err := resolveInitConfig(cmd, []string{"demo-cli"})
	if err != nil {
		t.Fatalf("resolveInitConfig: %v", err)
	}

	if config.AppName != "demo-cli" {
		t.Fatalf("expected app name demo-cli, got %q", config.AppName)
	}
	if config.EnvPrefix != "DEMO_CLI" {
		t.Fatalf("expected env prefix DEMO_CLI, got %q", config.EnvPrefix)
	}
	if config.ModulePath != "github.com/acme/demo-cli" {
		t.Fatalf("expected module path github.com/acme/demo-cli, got %q", config.ModulePath)
	}
	if config.DefaultOutputFormat != "yaml" {
		t.Fatalf("expected default output format yaml, got %q", config.DefaultOutputFormat)
	}
	if config.APIKeyEnvVar != "MY_TEAM_TOKEN" {
		t.Fatalf("expected api key env var MY_TEAM_TOKEN, got %q", config.APIKeyEnvVar)
	}
}

func TestResolveInitConfigRejectsInvalidAPIKeyEnvVar(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().Bool("interactive", false, "")
	cmd.Flags().String("module-path", "", "")
	cmd.Flags().String("api-key-env-var", "123_BAD", "")
	cmd.Flags().String("default-format", "json", "")

	if _, err := resolveInitConfig(cmd, []string{"demo-cli"}); err == nil {
		t.Fatal("expected invalid api key env var error")
	}
}

func TestResolveInitConfigRejectsInvalidModulePath(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().Bool("interactive", false, "")
	cmd.Flags().String("module-path", "github.com/acme/demo cli", "")
	cmd.Flags().String("api-key-env-var", "", "")
	cmd.Flags().String("default-format", "json", "")

	if _, err := resolveInitConfig(cmd, []string{"demo-cli"}); err == nil {
		t.Fatal("expected invalid module path error")
	}
}

func TestResolveInitConfigRejectsUnknownDefaultFormat(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().Bool("interactive", false, "")
	cmd.Flags().String("module-path", "", "")
	cmd.Flags().String("api-key-env-var", "", "")
	cmd.Flags().String("default-format", "not-a-format", "")

	_, err := resolveInitConfig(cmd, []string{"demo-cli"})
	if err == nil {
		t.Fatal("expected unknown default format error")
	}
	if !strings.Contains(err.Error(), "is not one of [json, yaml, toon]") {
		t.Fatalf("expected the error to name the allowed formats, got %q", err)
	}
}

func TestGenerateFromJSONFixtureBuildsCLI(t *testing.T) {
	repoRoot, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	tmp := t.TempDir()
	oldWD := repoRoot
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir tempdir: %v", err)
	}
	defer os.Chdir(oldWD)

	config := &ProjectConfig{
		AppName:             "orq",
		AppVersion:          "0.1.0",
		ModulePath:          "github.com/acme/orq",
		BartoloReplacePath:  repoRoot,
		BartoloVersion:      bartoloVersion,
		EnvPrefix:           "ORQ",
		DefaultOutputFormat: "json",
		APIKeyEnvVar:        "ORQ_API_KEY",
	}
	if err := writeProjectScaffold(config, false); err != nil {
		t.Fatalf("writeProjectScaffold: %v", err)
	}

	specPath := filepath.Join(repoRoot, "testdata", "orq", "openapi.json")
	if err := generateFromSpec(specPath); err != nil {
		t.Fatalf("generateFromSpec: %v", err)
	}

	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = tmp
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Fatalf("go mod tidy: %v\n%s", err, string(out))
	}

	for _, path := range []string{
		filepath.Join(tmp, "cmd", "orq", "main.go"),
		filepath.Join(tmp, "cli", "generated", "register.go"),
		filepath.Join(tmp, "cli", "custom", "register.go"),
		filepath.Join(tmp, "examples", "README.md"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected generated file %s: %v", path, err)
		}
	}

	build := exec.Command("go", "build", "./...")
	build.Dir = tmp
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build ./...: %v\n%s", err, string(out))
	}
}

func TestBodyFieldTypeCoversCommonShapes(t *testing.T) {
	doc, err := loadOpenAPIDocument([]byte(`{
  "openapi": "3.1.0",
  "info": {"title": "Body field shapes", "version": "1"},
  "paths": {
    "/things": {
      "post": {
        "operationId": "CreateThing",
        "requestBody": {
          "required": true,
          "content": {
            "application/json": {
              "schema": {
                "type": "object",
                "required": ["name"],
                "properties": {
                  "name": {"type": "string"},
                  "display_name": {"anyOf": [{"type": "string"}, {"type": "null"}]},
                  "count": {"type": ["integer", "null"]},
                  "tags": {"type": "array", "items": {"type": "string"}},
                  "scores": {"type": "array", "items": {"type": "integer"}},
                  "metadata": {"type": "object", "additionalProperties": {"type": "string"}},
                  "metadata_any": {"type": "object", "additionalProperties": true},
                  "color": {"type": "string", "enum": ["red", "green", "blue"]},
                  "nested": {"type": "object", "properties": {"k": {"type": "string"}}}
                }
              }
            }
          }
        },
        "responses": {"200": {"description": "ok"}}
      }
    }
  }
}`))
	if err != nil {
		t.Fatalf("loadOpenAPIDocument: %v", err)
	}

	schema := doc.Paths.Value("/things").Post.RequestBody.Value.Content.Get("application/json").Schema.Value
	fields := getBodyFields(schema)

	got := map[string]string{}
	enumByName := map[string][]string{}
	for _, f := range fields {
		got[f.Name] = f.Type
		if len(f.Enum) > 0 {
			enumByName[f.Name] = f.Enum
		}
	}

	want := map[string]string{
		"name":         "string",
		"display_name": "string-nullable",
		"count":        "int64-nullable",
		"tags":         "string-slice",
		"scores":       "int64-slice",
		"metadata":     "string-map",
		"metadata_any": "string-map",
		"color":        "enum-string",
	}
	for name, typ := range want {
		if got[name] != typ {
			t.Errorf("field %q: type = %q, want %q", name, got[name], typ)
		}
	}
	if got["nested"] != "json" {
		t.Errorf("nested object should fall back to json flag, got %q", got["nested"])
	}

	if cs := enumByName["color"]; len(cs) != 3 || cs[0] != "red" || cs[2] != "blue" {
		t.Errorf("color enum = %v, want [red green blue]", cs)
	}
}

func TestGetBodyFieldsMergesAllOf(t *testing.T) {
	doc, err := loadOpenAPIDocument([]byte(`{
  "openapi": "3.1.0",
  "info": {"title": "AllOf merge", "version": "1"},
  "paths": {
    "/things": {
      "post": {
        "operationId": "CreateThing",
        "requestBody": {
          "required": true,
          "content": {
            "application/json": {
              "schema": {
                "allOf": [
                  {"type": "object", "required": ["name"], "properties": {"name": {"type": "string"}}},
                  {"type": "object", "properties": {"count": {"type": "integer"}, "tags": {"type": "array", "items": {"type": "string"}}}}
                ]
              }
            }
          }
        },
        "responses": {"200": {"description": "ok"}}
      }
    }
  }
}`))
	if err != nil {
		t.Fatalf("loadOpenAPIDocument: %v", err)
	}

	schema := doc.Paths.Value("/things").Post.RequestBody.Value.Content.Get("application/json").Schema.Value
	fields := getBodyFields(schema)

	got := map[string]string{}
	for _, f := range fields {
		got[f.Name] = f.Type
	}
	want := map[string]string{
		"name":  "string",
		"count": "int64",
		"tags":  "string-slice",
	}
	for name, typ := range want {
		if got[name] != typ {
			t.Errorf("field %q: type = %q, want %q", name, got[name], typ)
		}
	}
}

func TestGetBodyFieldsUnionsOneOfBranches(t *testing.T) {
	doc, err := loadOpenAPIDocument([]byte(`{
  "openapi": "3.1.0",
  "info": {"title": "OneOf union", "version": "1"},
  "paths": {
    "/parse": {
      "post": {
        "operationId": "Parse",
        "requestBody": {
          "required": true,
          "content": {
            "application/json": {
              "schema": {
                "oneOf": [
                  {"type": "object", "required": ["text", "strategy"], "properties": {"text": {"type": "string"}, "strategy": {"type": "string", "enum": ["token"]}, "chunk_size": {"type": "integer"}}},
                  {"type": "object", "required": ["text", "strategy"], "properties": {"text": {"type": "string"}, "strategy": {"type": "string", "enum": ["semantic"]}, "threshold": {"type": "number"}, "embedding_model": {"type": "string"}}}
                ]
              }
            }
          }
        },
        "responses": {"200": {"description": "ok"}}
      }
    }
  }
}`))
	if err != nil {
		t.Fatalf("loadOpenAPIDocument: %v", err)
	}

	schema := doc.Paths.Value("/parse").Post.RequestBody.Value.Content.Get("application/json").Schema.Value
	fields := getBodyFields(schema)

	got := map[string]string{}
	for _, f := range fields {
		got[f.Name] = f.Type
	}
	want := map[string]string{
		"text":            "string",
		"strategy":        "enum-string",
		"chunk_size":      "int64",
		"threshold":       "float64",
		"embedding_model": "string",
	}
	for name, typ := range want {
		if got[name] != typ {
			t.Errorf("field %q: type = %q, want %q", name, got[name], typ)
		}
	}

	for _, f := range fields {
		if f.Name == "strategy" {
			if !reflect.DeepEqual(f.Enum, []string{"token", "semantic"}) {
				t.Errorf("strategy enum = %v, want the union of both branches [token semantic]", f.Enum)
			}
		}
	}
}

func TestGetBodyFieldsUnionsDiscriminatorEnums(t *testing.T) {
	doc, err := loadOpenAPIDocument([]byte(`{
  "openapi": "3.1.0",
  "info": {"title": "Discriminator union", "version": "1"},
  "paths": {
    "/tools": {
      "post": {
        "operationId": "CreateTool",
        "requestBody": {
          "required": true,
          "content": {
            "application/json": {
              "schema": {
                "oneOf": [
                  {"type": "object", "required": ["type"], "properties": {"type": {"type": "string", "enum": ["function"]}, "status": {"type": "string", "enum": ["live", "draft"]}}},
                  {"type": "object", "required": ["type"], "properties": {"type": {"type": "string", "enum": ["http"]}, "status": {"type": "string", "enum": ["live", "draft"]}}},
                  {"type": "object", "required": ["type"], "properties": {"type": {"type": "string", "enum": ["mcp"]}, "status": {"type": "string", "enum": ["live", "draft"]}}}
                ]
              }
            }
          }
        },
        "responses": {"200": {"description": "ok"}}
      }
    }
  }
}`))
	if err != nil {
		t.Fatalf("loadOpenAPIDocument: %v", err)
	}

	schema := doc.Paths.Value("/tools").Post.RequestBody.Value.Content.Get("application/json").Schema.Value
	fields := getBodyFields(schema)

	byName := map[string]*BodyField{}
	for _, f := range fields {
		byName[f.Name] = f
	}

	typeField := byName["type"]
	if typeField == nil {
		t.Fatal("missing field \"type\"")
	}
	if typeField.Type != "enum-string" {
		t.Errorf("type field: type = %q, want enum-string", typeField.Type)
	}
	if !reflect.DeepEqual(typeField.Enum, []string{"function", "http", "mcp"}) {
		t.Errorf("type enum = %v, want [function http mcp] (union across all branches, in branch order)", typeField.Enum)
	}

	statusField := byName["status"]
	if statusField == nil {
		t.Fatal("missing field \"status\"")
	}
	if !reflect.DeepEqual(statusField.Enum, []string{"live", "draft"}) {
		t.Errorf("status enum = %v, want [live draft] (identical branch enums must not duplicate)", statusField.Enum)
	}

	// The union must be built on a copy: the source branch schemas are shared
	// $ref components in real specs and must never be widened in place.
	firstBranch := schema.OneOf[0].Value.Properties["type"].Value
	if len(firstBranch.Enum) != 1 {
		t.Errorf("source branch enum was mutated: %v, want [function]", firstBranch.Enum)
	}
}

func TestGetBodyFieldsDropsEnumWhenUnionBranchAllowsAnyString(t *testing.T) {
	doc, err := loadOpenAPIDocument([]byte(`{
  "openapi": "3.1.0",
  "info": {"title": "Enum vs plain string", "version": "1"},
  "paths": {
    "/things": {
      "post": {
        "operationId": "CreateThing",
        "requestBody": {
          "required": true,
          "content": {
            "application/json": {
              "schema": {
                "oneOf": [
                  {"type": "object", "properties": {"mode": {"type": "string", "enum": ["auto"]}}},
                  {"type": "object", "properties": {"mode": {"type": "string"}}}
                ]
              }
            }
          }
        },
        "responses": {"200": {"description": "ok"}}
      }
    }
  }
}`))
	if err != nil {
		t.Fatalf("loadOpenAPIDocument: %v", err)
	}

	schema := doc.Paths.Value("/things").Post.RequestBody.Value.Content.Get("application/json").Schema.Value
	fields := getBodyFields(schema)

	for _, f := range fields {
		if f.Name != "mode" {
			continue
		}
		if f.Type != "string" {
			t.Errorf("mode field: type = %q, want plain string (one branch accepts any string, so no enum validation)", f.Type)
		}
		if len(f.Enum) != 0 {
			t.Errorf("mode enum = %v, want empty", f.Enum)
		}
		return
	}
	t.Fatal("missing field \"mode\"")
}

func TestGetBodyFieldsKeepsFirstBranchOnTypeConflict(t *testing.T) {
	doc, err := loadOpenAPIDocument([]byte(`{
  "openapi": "3.1.0",
  "info": {"title": "Type conflict", "version": "1"},
  "paths": {
    "/things": {
      "post": {
        "operationId": "CreateThing",
        "requestBody": {
          "required": true,
          "content": {
            "application/json": {
              "schema": {
                "oneOf": [
                  {"type": "object", "properties": {"config": {"type": "string", "enum": ["default"]}}},
                  {"type": "object", "properties": {"config": {"type": "object", "properties": {"url": {"type": "string"}}}}}
                ]
              }
            }
          }
        },
        "responses": {"200": {"description": "ok"}}
      }
    }
  }
}`))
	if err != nil {
		t.Fatalf("loadOpenAPIDocument: %v", err)
	}

	schema := doc.Paths.Value("/things").Post.RequestBody.Value.Content.Get("application/json").Schema.Value
	fields := getBodyFields(schema)

	for _, f := range fields {
		if f.Name != "config" {
			continue
		}
		if f.Type != "enum-string" || !reflect.DeepEqual(f.Enum, []string{"default"}) {
			t.Errorf("config field = (%q, %v), want first-wins (enum-string, [default]) when branch types conflict", f.Type, f.Enum)
		}
		return
	}
	t.Fatal("missing field \"config\"")
}

func TestBodyFieldTypeStringUnionUsesJSONOrString(t *testing.T) {
	doc, err := loadOpenAPIDocument([]byte(`{
  "openapi": "3.1.0",
  "info": {"title": "String unions", "version": "1"},
  "paths": {
    "/agents": {
      "post": {
        "operationId": "CreateAgent",
        "requestBody": {
          "required": true,
          "content": {
            "application/json": {
              "schema": {
                "type": "object",
                "properties": {
                  "model": {"anyOf": [{"type": "string"}, {"type": "object", "properties": {"id": {"type": "string"}}}]},
                  "input": {"oneOf": [{"type": "string"}, {"type": "array", "items": {"type": "object"}}]},
                  "config": {"oneOf": [{"type": "object", "properties": {"a": {"type": "string"}}}, {"type": "array", "items": {"type": "object"}}]}
                }
              }
            }
          }
        },
        "responses": {"200": {"description": "ok"}}
      }
    }
  }
}`))
	if err != nil {
		t.Fatalf("loadOpenAPIDocument: %v", err)
	}

	schema := doc.Paths.Value("/agents").Post.RequestBody.Value.Content.Get("application/json").Schema.Value
	got := map[string]string{}
	for _, f := range getBodyFields(schema) {
		got[f.Name] = f.Type
	}

	// Unions with a string branch accept a bare string; unions without one stay strict JSON.
	if got["model"] != "json-or-string" {
		t.Errorf("model: type = %q, want json-or-string", got["model"])
	}
	if got["input"] != "json-or-string" {
		t.Errorf("input: type = %q, want json-or-string", got["input"])
	}
	if got["config"] != "json" {
		t.Errorf("config (object|array, no string branch): type = %q, want json", got["config"])
	}
}

func TestLoadOpenAPIDocumentSupportsNumericExclusiveBounds(t *testing.T) {
	doc, err := loadOpenAPIDocument([]byte(`{
  "openapi": "3.1.0",
  "info": {
    "title": "OpenAPI 3.1 Test",
    "version": "1"
  },
  "paths": {
    "/widgets": {
      "post": {
        "operationId": "CreateWidget",
        "requestBody": {
          "required": true,
          "content": {
            "application/json": {
              "schema": {
                "type": "object",
                "properties": {
                  "count": {
                    "type": "integer",
                    "exclusiveMinimum": 0
                  }
                }
              }
            }
          }
        },
        "responses": {
          "200": {
            "description": "ok"
          }
        }
      }
    }
  }
}`))
	if err != nil {
		t.Fatalf("loadOpenAPIDocument: %v", err)
	}

	schema := doc.Paths.Value("/widgets").Post.RequestBody.Value.Content.Get("application/json").Schema.Value.Properties["count"].Value
	if schema == nil {
		t.Fatal("expected request schema for count")
	}
	if schema.Min == nil || *schema.Min != 0 {
		t.Fatalf("expected minimum 0 after normalization, got %#v", schema.Min)
	}
	if !schema.ExclusiveMin.IsTrue() {
		t.Fatal("expected exclusiveMinimum=true after normalization")
	}
}

func TestProcessAPIRenamesFlagsThatShadowGlobals(t *testing.T) {
	doc := loadTestSpec(t, `
openapi: 3.0.3
info:
  title: Traces API
  version: "1"
tags:
  - name: Traces
paths:
  /v2/traces/search:
    post:
      operationId: TracesSearch
      summary: Search traces
      tags:
        - Traces
      parameters:
        - in: query
          name: profile
          schema:
            type: string
        - in: query
          name: page_token
          schema:
            type: string
        - in: query
          name: query
          schema:
            type: string
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                query:
                  type: string
                raw:
                  type: boolean
                limit:
                  type: integer
      responses:
        "200":
          description: ok
`)

	api := ProcessAPI("example", doc)
	if len(api.Groups) != 1 || len(api.Groups[0].Operations) != 1 {
		t.Fatalf("expected a single grouped operation, got %+v", api.Groups)
	}
	op := api.Groups[0].Operations[0]

	bodyFlags := map[string]string{}
	for _, field := range op.BodyFields {
		bodyFlags[field.Name] = field.CLIName
	}
	expectedBody := map[string]string{
		// `query` is not reserved — the global JMESPath flag is `--jmespath`.
		"query": "query",
		"raw":   "body-raw",
		"limit": "limit",
	}
	if !reflect.DeepEqual(bodyFlags, expectedBody) {
		t.Fatalf("body flag names: got %+v, want %+v", bodyFlags, expectedBody)
	}

	paramFlags := map[string]string{}
	for _, param := range op.OptionalParams {
		paramFlags[param.Name] = param.CLIName
	}
	expectedParams := map[string]string{
		"profile":    "param-profile",
		"page_token": "page-token",
		// A param and a body field both named `query`: the body field claims
		// the name first, so the param has to move — registering both would
		// make pflag panic.
		"query": "param-query",
	}
	if !reflect.DeepEqual(paramFlags, expectedParams) {
		t.Fatalf("param flag names: got %+v, want %+v", paramFlags, expectedParams)
	}

	// The renamed param must still be looked up under its new name and sent
	// under the original wire name.
	for _, param := range op.OptionalParams {
		if param.Name == "profile" && param.GoName != "paramParamProfile" {
			t.Fatalf("renamed param Go name: got %q", param.GoName)
		}
	}

	if !strings.Contains(op.Long, "--body-raw") {
		t.Fatalf("expected the renamed flags to be documented in help, got:\n%s", op.Long)
	}
}

func TestReserveGeneratedFlagNamesResolvesParamBodyCollision(t *testing.T) {
	bodyFields := []*BodyField{{Name: "limit", CLIName: "limit"}}
	optionalParams := []*Param{{Name: "limit", CLIName: "limit"}}

	renamed := reserveGeneratedFlagNames(bodyFields, optionalParams)

	if bodyFields[0].CLIName != "limit" {
		t.Fatalf("body field should keep the name it claimed first, got %q", bodyFields[0].CLIName)
	}
	if optionalParams[0].CLIName != "param-limit" {
		t.Fatalf("colliding param should be renamed, got %q", optionalParams[0].CLIName)
	}
	if len(renamed) != 1 || renamed[0].To != "param-limit" {
		t.Fatalf("unexpected rename record: %+v", renamed)
	}
}

func TestGeneratedClientURLEscapesPathParamsAndRejectsEmpty(t *testing.T) {
	doc := loadTestSpec(t, `
openapi: 3.0.3
info:
  title: Path Param API
  version: "1"
paths:
  /v2/agents/{agent_id}:
    get:
      operationId: GetAgent
      summary: Get agent
      parameters:
        - in: path
          name: agent_id
          required: true
          schema:
            type: string
      responses:
        "200":
          description: ok
    delete:
      operationId: DeleteAgent
      summary: Delete agent
      parameters:
        - in: path
          name: agent_id
          required: true
          schema:
            type: string
      responses:
        "204":
          description: ok
`)

	api := ProcessAPI("example", doc)

	if !api.Imports.Url {
		t.Fatal("expected Imports.Url to be true when path params are present")
	}

	rendered := renderCommandTemplate("templates/generated_client.tmpl", api)

	if !strings.Contains(rendered, "neturl \"net/url\"") {
		t.Fatal("generated client should import net/url when path params are present")
	}

	// Every path-param substitution must go through neturl.PathEscape so that
	// crafted IDs like "../../v2/projects" cannot traverse to other endpoints.
	if !strings.Contains(rendered, "neturl.PathEscape(") {
		t.Fatal("generated client must URL-escape path parameters with neturl.PathEscape")
	}

	// Empty path-param values must be rejected client-side so a missing ID
	// cannot collapse the URL into a collection-level DELETE/GET.
	if !strings.Contains(rendered, `== ""`) {
		t.Fatal("generated client must reject empty path parameters")
	}
}

func TestProcessAPIRecognizesResourceNamedCollectionWrapper(t *testing.T) {
	doc := loadTestSpec(t, `
openapi: 3.0.3
info:
  title: Wrapper API
  version: "1"
paths:
  /agents/{agent_key}/schedules:
    get:
      operationId: listSchedules
      parameters:
        - name: agent_key
          in: path
          required: true
          schema:
            type: string
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                properties:
                  schedules:
                    type: array
                    items:
                      type: object
`)

	api := ProcessAPI("example", doc)
	var op *Operation
	if len(api.Operations) > 0 {
		op = api.Operations[0]
	} else if len(api.Groups) > 0 && len(api.Groups[0].Operations) > 0 {
		op = api.Groups[0].Operations[0]
	}
	if op == nil {
		t.Fatal("expected generated operation")
	}
	if !op.IsList {
		t.Fatal("collection wrapped in a resource-named key should use list formatting")
	}
}

func TestProcessAPIDeduplicatesGoNames(t *testing.T) {
	doc := loadTestSpec(t, `
openapi: 3.0.3
info:
  title: Duplicate operation id API
  version: "1"
paths:
  /evaluators/invoke:
    post:
      operationId: InvokeEval
      responses:
        "200":
          description: ok
  /evals/invoke:
    post:
      operationId: InvokeEval
      responses:
        "200":
          description: ok
`)

	api := ProcessAPI("example", doc)
	seen := map[string]bool{}
	count := 0
	for _, op := range api.Operations {
		if seen[op.GoName] {
			t.Fatalf("duplicate Go name %q", op.GoName)
		}
		seen[op.GoName] = true
		count++
	}
	for _, group := range api.Groups {
		for _, op := range group.Operations {
			if seen[op.GoName] {
				t.Fatalf("duplicate Go name %q", op.GoName)
			}
			seen[op.GoName] = true
			count++
		}
	}
	if count != 2 {
		t.Fatalf("expected 2 operations, got %d", count)
	}
}

func TestProcessAPIReadsCLIHelpSectionFromTagAndOperation(t *testing.T) {
	doc := loadTestSpec(t, `
openapi: 3.0.3
info:
  title: Sectioned API
  version: "1"
tags:
  - name: Files
    x-cli-help-section: Storage
paths:
  /files:
    get:
      operationId: ListFiles
      summary: List files
      tags:
        - Files
      responses:
        "200":
          description: ok
  /agents:
    get:
      operationId: ListAgents
      summary: List agents
      tags:
        - Agents
      x-cli-help-section: Managed agents
      responses:
        "200":
          description: ok
  /ping:
    get:
      operationId: Ping
      summary: Ping
      x-cli-group: ""
      x-cli-help-section: Utilities
      responses:
        "200":
          description: ok
`)

	api := ProcessAPI("example", doc)

	sections := map[string]string{}
	for _, group := range api.Groups {
		sections[group.CLIName] = group.HelpSection
	}
	if sections["files"] != "Storage" {
		t.Fatalf("expected tag section Storage, got %q", sections["files"])
	}
	if sections["agents"] != "Managed agents" {
		t.Fatalf("expected operation override section, got %q", sections["agents"])
	}
	if sections["ping"] != "Utilities" {
		t.Fatalf("expected path-inferred group to carry section, got %q", sections["ping"])
	}
}

// TestBuildRevision covers the suffix a dev build carries. Go omits the VCS
// stamp entirely in a git worktree and under -buildvcs=false, so the empty case
// is the one that actually shows up, not a theoretical one.
func TestBuildRevision(t *testing.T) {
	cases := []struct {
		name     string
		settings []debug.BuildSetting
		want     string
	}{
		{"full sha is shortened", []debug.BuildSetting{{Key: "vcs.revision", Value: "1a2b3c4d5e6f7890"}}, "+1a2b3c4"},
		{"short sha is left alone", []debug.BuildSetting{{Key: "vcs.revision", Value: "1a2b3c"}}, "+1a2b3c"},
		{"other settings are skipped", []debug.BuildSetting{{Key: "vcs", Value: "git"}, {Key: "vcs.revision", Value: "abcdefgh"}}, "+abcdefg"},
		{"no stamp", []debug.BuildSetting{{Key: "vcs.modified", Value: "false"}}, ""},
		{"empty revision", []debug.BuildSetting{{Key: "vcs.revision", Value: ""}}, ""},
		{"no settings", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildRevision(tc.settings); got != tc.want {
				t.Errorf("buildRevision() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNormalizeVersion(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"tagged version loses its v", "v0.4.7", "0.4.7"},
		{"prerelease is kept whole", "v1.2.3-rc.1", "1.2.3-rc.1"},
		{"version without a v is left alone", "0.4.7", "0.4.7"},
		{"go build reports devel", "(devel)", devel},
		{"no module version at all", "", devel},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeVersion(tc.raw); got != tc.want {
				t.Errorf("normalizeVersion(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestResolveVersionFrom(t *testing.T) {
	revision := []debug.BuildSetting{{Key: "vcs.revision", Value: "1a2b3c4d5e6f"}}
	cases := []struct {
		name string
		info *debug.BuildInfo
		ok   bool
		want string
	}{
		{"no build info", nil, false, devel},
		{
			"installed version wins over the revision",
			&debug.BuildInfo{Main: debug.Module{Version: "v0.5.0"}, Settings: revision},
			true,
			"0.5.0",
		},
		{
			"dev build carries the revision",
			&debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: revision},
			true,
			"devel+1a2b3c4",
		},
		{
			"dev build without a stamp is bare devel",
			&debug.BuildInfo{Main: debug.Module{Version: "(devel)"}},
			true,
			devel,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveVersionFrom(tc.info, tc.ok); got != tc.want {
				t.Errorf("resolveVersionFrom() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGeneratedClientChecksParamEnumAndFormat(t *testing.T) {
	doc := loadTestSpec(t, `
openapi: 3.0.3
info:
  title: Param Validation API
  version: "1"
paths:
  /v2/agents/{agent_id}:
    get:
      operationId: GetAgent
      summary: Get agent
      parameters:
        - in: path
          name: agent_id
          required: true
          schema:
            type: string
            format: uuid
        - in: query
          name: status
          schema:
            anyOf:
              - type: string
                enum: [active, "archi\"ved"]
              - type: "null"
        - in: query
          name: opaque_cursor
          x-cli-no-validate: true
          schema:
            type: string
            format: uuid
      responses:
        "200":
          description: ok
`)

	rendered := renderCommandTemplate("templates/generated_client.tmpl", ProcessAPI("example", doc))

	if !strings.Contains(rendered, `bartolocli.CheckParam("argument agent-id", paramAgentId, "uuid"`) {
		t.Error("required path param with format: uuid should be checked")
	}

	// The enum sits behind an anyOf/null wrapper, which must be resolved, and
	// its values are embedded as Go literals so quotes have to be escaped.
	if !strings.Contains(rendered, `"active", "archi\"ved",`) {
		t.Errorf("nullable enum values should be carried through escaped, got:\n%s", rendered)
	}

	// x-cli-no-validate opts a parameter out entirely. Asserting the flag exists
	// first matters: without it the check below passes for a param that simply
	// was not rendered.
	if !strings.Contains(rendered, "opaque-cursor") {
		t.Fatalf("the opted-out param should still be a flag, got:\n%s", rendered)
	}
	if strings.Contains(rendered, `CheckParam("--opaque-cursor"`) {
		t.Error("x-cli-no-validate should suppress the check")
	}
}

// x-cli-no-validate is read as a boolean, not as mere presence, so writing the
// documented `: true` is what disables the check — and `: false` re-enables it.
func TestNoValidateExtensionReadsItsValue(t *testing.T) {
	spec := `
openapi: 3.0.3
info:
  title: Ext API
  version: "1"
paths:
  /things:
    get:
      operationId: ListThings
      parameters:
        - in: query
          name: cursor
          x-cli-no-validate: %s
          schema:
            type: string
            format: uuid
      responses:
        "200":
          description: ok
`

	if rendered := renderCommandTemplate("templates/generated_client.tmpl", ProcessAPI("example", loadTestSpec(t, fmt.Sprintf(spec, "false")))); !strings.Contains(rendered, `CheckParam("--cursor"`) {
		t.Errorf("x-cli-no-validate: false should leave the check in place, got:\n%s", rendered)
	}

	if rendered := renderCommandTemplate("templates/generated_client.tmpl", ProcessAPI("example", loadTestSpec(t, fmt.Sprintf(spec, "true")))); strings.Contains(rendered, `CheckParam("--cursor"`) {
		t.Errorf("x-cli-no-validate: true should suppress the check, got:\n%s", rendered)
	}
}

func TestGeneratedRootCommandsReturnErrors(t *testing.T) {
	// The path has no group to infer, so the operation stays ungrouped and the
	// root template actually renders it. `/v2/agents` would not: inferGroupFromPath
	// skips the version segment and groups it under `agents`, leaving the root
	// render empty and every assertion below satisfied by the group render.
	root, group := renderRootAndGroup(t, `
openapi: 3.0.3
info:
  title: Error API
  version: "1"
paths:
  /{id}:
    get:
      operationId: GetThing
      summary: Get a thing
      parameters:
        - name: id
          in: path
          required: true
          schema: {type: string}
        - name: detailed
          in: query
          schema: {type: boolean}
      responses:
        "200":
          description: ok
    post:
      operationId: CreateThing
      summary: Create a thing
      parameters:
        - name: id
          in: path
          required: true
          schema: {type: string}
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                status:
                  type: string
                  enum: [active, archived]
      responses:
        "200":
          description: ok
  /v2/agents:
    get:
      operationId: ListAgents
      summary: List agents
      responses:
        "200":
          description: ok
`)

	if !strings.Contains(root, `Use: "get-thing id"`) {
		t.Fatalf("fixture must render an ungrouped operation into the root template, got:\n%s", root)
	}

	for name, rendered := range map[string]string{"root": root, "group": group} {
		// Commands must return their errors so cli/exit.go can tell a usage error
		// (exit 2) from an operation failure (exit 1). log.Fatal bypasses that.
		if !strings.Contains(rendered, "RunE: func(cmd *cobra.Command, args []string) error {") {
			t.Errorf("%s: generated commands should use RunE", name)
		}
		if strings.Contains(rendered, "log.Fatal") {
			t.Errorf("%s: generated commands should not call log.Fatal", name)
		}
	}

	// The two blocks below were the copy-paste divergences that motivated
	// templates/command_partials.tmpl: both were correct in the group template
	// and silently wrong or missing in the root one.
	if !strings.Contains(root, `Enum: []string{`) || !strings.Contains(root, `"archived",`) {
		t.Errorf("root: body-field enums should be wired up, got:\n%s", root)
	}
	if !strings.Contains(root, `cmd.Flags().Bool("detailed"`) {
		t.Errorf("root: an optional boolean parameter should register a Bool flag, got:\n%s", root)
	}
}

// renderRootAndGroup renders a spec through both command templates and returns
// the two outputs separately. Concatenating them lets an assertion meant for one
// template pass on the other's output, which is exactly the drift these tests
// exist to catch.
func renderRootAndGroup(t *testing.T, spec string) (string, string) {
	t.Helper()

	api := ProcessAPI("example", loadTestSpec(t, spec))

	root := renderCommandTemplate("templates/generated_root_commands.tmpl", &CommandsTemplateData{
		API:        api,
		Operations: api.Operations,
		Waiters:    api.Waiters,
		NeedsFmt:   commandFileNeedsFmt(api.Operations),
	})

	var group string
	for _, g := range api.Groups {
		group += renderCommandTemplate("templates/generated_group_commands.tmpl", &CommandsTemplateData{
			API:        api,
			Group:      g,
			Operations: g.Operations,
			NeedsFmt:   commandFileNeedsFmt(g.Operations),
		})
	}

	return root, group
}
