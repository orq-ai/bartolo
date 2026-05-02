package main

import (
	"os"
	"os/exec"
	"path/filepath"
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
	if _, present := got["nested"]; present {
		t.Errorf("nested object should not be exposed as a flag, got type %q", got["nested"])
	}

	if cs := enumByName["color"]; len(cs) != 3 || cs[0] != "red" || cs[2] != "blue" {
		t.Errorf("color enum = %v, want [red green blue]", cs)
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
	if !schema.ExclusiveMin {
		t.Fatal("expected exclusiveMinimum=true after normalization")
	}
}
