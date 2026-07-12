package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestGenerateTwoSchemeSpecBuildsCLI scaffolds and builds a generated CLI from a
// spec that declares both an apiKey and an OAuth2 client-credentials scheme,
// exercising the typed multi-scheme auth emission (apikey + oauth imports, typed
// inits, and SetDefaultAuthType) end to end through `go build`.
func TestGenerateTwoSchemeSpecBuildsCLI(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping build test in short mode")
	}

	repoRoot, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	spec := `{
  "openapi": "3.0.0",
  "info": {"title": "Two Scheme API", "version": "1.0.0"},
  "servers": [{"url": "https://api.example.com"}],
  "security": [{"keyauth": []}, {"oauthcc": []}],
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
    }
  },
  "components": {
    "securitySchemes": {
      "keyauth": {"type": "apiKey", "in": "header", "name": "X-API-Key"},
      "oauthcc": {
        "type": "oauth2",
        "flows": {"clientCredentials": {"tokenUrl": "https://api.example.com/token", "scopes": {"read": "read access"}}}
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
		AppName:             "twoscheme",
		AppVersion:          "0.1.0",
		ModulePath:          "github.com/acme/twoscheme",
		BartoloReplacePath:  repoRoot,
		BartoloVersion:      bartoloVersion,
		EnvPrefix:           "TWOSCHEME",
		DefaultOutputFormat: "json",
		APIKeyEnvVar:        "TWOSCHEME_API_KEY",
	}
	if err := writeProjectScaffold(config, false); err != nil {
		t.Fatalf("writeProjectScaffold: %v", err)
	}

	if err := generateFromSpec(specPath); err != nil {
		t.Fatalf("generateFromSpec: %v", err)
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
