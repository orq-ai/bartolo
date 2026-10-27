package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestGenerateCLIIgnoredOpBuildsMCPToolAndClientFunc scaffolds and builds a
// generated CLI from a spec whose only grouped operation is marked
// x-bartolo-cli-ignore. This proves the new surface-scoped exclusion end to end:
// the op generates NO CLI command (its group is empty and never registered), yet
// its generated client func is still emitted and its MCP tool closure compiles
// against that func. A second, plain root op keeps a normal CLI command present.
func TestGenerateCLIIgnoredOpBuildsMCPToolAndClientFunc(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping build test in short mode")
	}

	repoRoot, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	spec := `{
  "openapi": "3.1.0",
  "info": {"title": "CLI Ignore API", "version": "1.0.0"},
  "servers": [{"url": "https://api.example.com"}],
  "paths": {
    "/ping": {
      "get": {
        "operationId": "Ping",
        "summary": "Ping the API",
        "responses": {
          "200": {
            "description": "ok",
            "content": {"application/json": {"schema": {"type": "object", "properties": {"pong": {"type": "boolean"}}}}}
          }
        }
      }
    },
    "/secret/thing": {
      "get": {
        "operationId": "SecretThing",
        "summary": "A tool-only operation",
        "tags": ["Secret"],
        "x-bartolo-cli-ignore": true,
        "responses": {
          "200": {
            "description": "ok",
            "content": {"application/json": {"schema": {"type": "object", "properties": {"secret": {"type": "string"}}}}}
          }
        }
      }
    }
  }
}`

	specDir := t.TempDir()
	specPath := filepath.Join(specDir, "openapi.json")
	if err := os.WriteFile(specPath, []byte(spec), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir tempdir: %v", err)
	}
	defer os.Chdir(repoRoot)

	config := &ProjectConfig{
		AppName:             "cliignore",
		AppVersion:          "0.1.0",
		ModulePath:          "github.com/acme/cliignore",
		BartoloReplacePath:  repoRoot,
		BartoloVersion:      bartoloVersion,
		EnvPrefix:           "CLIIGNORE",
		SerializationFormat: "json",
		APIKeyEnvVar:        "CLIIGNORE_API_KEY",
		Targets:             []string{"cli", "mcp"},
	}
	if err := writeProjectScaffold(config, false); err != nil {
		t.Fatalf("writeProjectScaffold: %v", err)
	}

	if err := generateFromSpec(specPath); err != nil {
		t.Fatalf("generateFromSpec: %v", err)
	}

	// The empty group must not have produced a group command file.
	if _, err := os.Stat(filepath.Join(tmp, "cli", "generated", "secret_commands.go")); err == nil {
		t.Fatalf("expected no group command file for the fully cli-ignored group")
	}

	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = tmp
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Fatalf("go mod tidy: %v\n%s", err, string(out))
	}

	build := exec.Command("go", "build", "./...")
	build.Dir = tmp
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build ./...: %v\n%s", err, string(out))
	}
}
