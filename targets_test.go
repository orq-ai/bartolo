package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// initTestCmd builds a cobra command carrying the same flags `bartolo init`
// registers, so resolveInitConfig can be exercised directly. targets seeds the
// --targets flag value.
func initTestCmd(targets string) *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Flags().Bool("interactive", false, "")
	cmd.Flags().String("module-path", "github.com/acme/demo-cli", "")
	cmd.Flags().String("api-key-env-var", "", "")
	cmd.Flags().String("default-format", "json", "")
	cmd.Flags().String("targets", targets, "")
	return cmd
}

func TestResolveInitConfigTargetsDefaultBoth(t *testing.T) {
	config, err := resolveInitConfig(initTestCmd(""), []string{"demo-cli"})
	if err != nil {
		t.Fatalf("resolveInitConfig: %v", err)
	}
	if got := strings.Join(config.Targets, ","); got != "cli,mcp" {
		t.Fatalf("expected default targets cli,mcp, got %q", got)
	}
}

func TestResolveInitConfigTargetsMCPOnly(t *testing.T) {
	config, err := resolveInitConfig(initTestCmd("mcp"), []string{"demo-cli"})
	if err != nil {
		t.Fatalf("resolveInitConfig: %v", err)
	}
	if got := strings.Join(config.Targets, ","); got != "mcp" {
		t.Fatalf("expected targets mcp, got %q", got)
	}
	if config.TargetCLI() {
		t.Fatal("expected TargetCLI false for mcp-only")
	}
	if !config.TargetMCP() {
		t.Fatal("expected TargetMCP true for mcp-only")
	}
}

func TestResolveInitConfigTargetsCLIOnly(t *testing.T) {
	config, err := resolveInitConfig(initTestCmd("cli"), []string{"demo-cli"})
	if err != nil {
		t.Fatalf("resolveInitConfig: %v", err)
	}
	if got := strings.Join(config.Targets, ","); got != "cli" {
		t.Fatalf("expected targets cli, got %q", got)
	}
}

func TestResolveInitConfigTargetsDedupesAndTrims(t *testing.T) {
	config, err := resolveInitConfig(initTestCmd(" mcp , cli , mcp "), []string{"demo-cli"})
	if err != nil {
		t.Fatalf("resolveInitConfig: %v", err)
	}
	if got := strings.Join(config.Targets, ","); got != "mcp,cli" {
		t.Fatalf("expected targets mcp,cli, got %q", got)
	}
}

func TestResolveInitConfigTargetsRejectsInvalid(t *testing.T) {
	if _, err := resolveInitConfig(initTestCmd("cli,bogus"), []string{"demo-cli"}); err == nil {
		t.Fatal("expected error for invalid target member")
	}
}

// targetsMatrixSpec exposes a plain GET op that yields both a CLI command and an
// MCP tool, so file presence differs purely by the selected targets.
const targetsMatrixSpec = `{
  "openapi": "3.1.0",
  "info": {"title": "Targets API", "version": "1.0.0"},
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
    }
  }
}`

// scaffoldAndGenerate scaffolds a project with the given targets into a fresh
// temp dir, generates from targetsMatrixSpec, runs `go mod tidy`, and returns the
// project dir. The caller's working directory is restored on cleanup.
func scaffoldAndGenerate(t *testing.T, appName string, targets []string) string {
	t.Helper()

	repoRoot, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	specDir := t.TempDir()
	specPath := filepath.Join(specDir, "openapi.json")
	if err := os.WriteFile(specPath, []byte(targetsMatrixSpec), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir tempdir: %v", err)
	}
	t.Cleanup(func() { os.Chdir(repoRoot) })

	config := &ProjectConfig{
		AppName:             appName,
		AppVersion:          "0.1.0",
		ModulePath:          "github.com/acme/" + appName,
		BartoloReplacePath:  repoRoot,
		BartoloVersion:      bartoloVersion,
		EnvPrefix:           strings.ToUpper(appName),
		DefaultOutputFormat: "json",
		APIKeyEnvVar:        strings.ToUpper(appName) + "_API_KEY",
		Targets:             targets,
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

	return tmp
}

func fileExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return err == nil
}

func TestGenerationTargetMatrix(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping build matrix in -short mode")
	}

	cases := []struct {
		name        string
		app         string
		targets     []string
		wantRootCmd bool
		wantMCP     bool
	}{
		{"both", "tgtboth", []string{"cli", "mcp"}, true, true},
		{"cli-only", "tgtcli", []string{"cli"}, true, false},
		{"mcp-only", "tgtmcp", []string{"mcp"}, false, true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			tmp := scaffoldAndGenerate(t, tc.app, tc.targets)
			gen := filepath.Join(tmp, "cli", "generated")

			rootCmd := filepath.Join(gen, "root_commands.go")
			mcpTools := filepath.Join(gen, "mcp_tools.go")

			if got := fileExists(t, rootCmd); got != tc.wantRootCmd {
				t.Fatalf("root_commands.go present=%v, want %v", got, tc.wantRootCmd)
			}
			if got := fileExists(t, mcpTools); got != tc.wantMCP {
				t.Fatalf("mcp_tools.go present=%v, want %v", got, tc.wantMCP)
			}

			registerSrc, err := os.ReadFile(filepath.Join(gen, "register.go"))
			if err != nil {
				t.Fatalf("read register.go: %v", err)
			}
			if tc.name == "mcp-only" {
				if strings.Contains(string(registerSrc), "registerRootCommands") {
					t.Fatal("mcp-only register.go must not reference registerRootCommands")
				}
				if !strings.Contains(string(registerSrc), "root.RunE = mcpCommand.RunE") {
					t.Fatal("mcp-only register.go must wire root.RunE to the mcp command")
				}
			}

			if tc.name == "cli-only" {
				// No generated file may import the mcpserver package.
				entries, err := os.ReadDir(gen)
				if err != nil {
					t.Fatalf("read generated dir: %v", err)
				}
				for _, e := range entries {
					if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
						continue
					}
					src, err := os.ReadFile(filepath.Join(gen, e.Name()))
					if err != nil {
						t.Fatalf("read %s: %v", e.Name(), err)
					}
					if strings.Contains(string(src), "orq-ai/bartolo/mcpserver") {
						t.Fatalf("cli-only generated file %s must not import mcpserver", e.Name())
					}
				}
			}

			build := exec.Command("go", "build", "./...")
			build.Dir = tmp
			if out, err := build.CombinedOutput(); err != nil {
				t.Fatalf("go build ./...: %v\n%s", err, string(out))
			}
		})
	}
}

// TestLegacyConfigMigrationNonTTYDefaultsBoth writes a .bartolo.json without a
// targets field and generates with a non-interactive stdin. The migration must
// default to both surfaces (no prompt) and persist the resolved targets.
func TestLegacyConfigMigrationNonTTYDefaultsBoth(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping build in -short mode")
	}

	repoRoot, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	specDir := t.TempDir()
	specPath := filepath.Join(specDir, "openapi.json")
	if err := os.WriteFile(specPath, []byte(targetsMatrixSpec), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(repoRoot)

	// Scaffold with targets, then strip the targets field to simulate a legacy
	// pre-targets .bartolo.json.
	config := &ProjectConfig{
		AppName:             "legacy",
		AppVersion:          "0.1.0",
		ModulePath:          "github.com/acme/legacy",
		BartoloReplacePath:  repoRoot,
		BartoloVersion:      bartoloVersion,
		EnvPrefix:           "LEGACY",
		DefaultOutputFormat: "json",
		APIKeyEnvVar:        "LEGACY_API_KEY",
		Targets:             []string{"cli", "mcp"},
	}
	if err := writeProjectScaffold(config, false); err != nil {
		t.Fatalf("writeProjectScaffold: %v", err)
	}
	stripTargetsFromConfig(t)

	// Force a non-interactive stdin so ensureConfigTargets takes the non-TTY path.
	restore := replaceStdinWithPipe(t)
	defer restore()

	if err := generateFromSpec(specPath); err != nil {
		t.Fatalf("generateFromSpec (legacy migration): %v", err)
	}

	// The persisted config must now record both targets.
	reloaded := loadProjectConfig()
	if reloaded == nil {
		t.Fatal("expected reloaded config")
	}
	if got := strings.Join(reloaded.Targets, ","); got != "cli,mcp" {
		t.Fatalf("expected migrated targets cli,mcp, got %q", got)
	}

	// Both surfaces should have been generated.
	gen := filepath.Join(tmp, "cli", "generated")
	if !fileExists(t, filepath.Join(gen, "root_commands.go")) {
		t.Fatal("expected root_commands.go after both-target migration")
	}
	if !fileExists(t, filepath.Join(gen, "mcp_tools.go")) {
		t.Fatal("expected mcp_tools.go after both-target migration")
	}
}

// stripTargetsFromConfig rewrites .bartolo.json in the cwd removing the targets
// field, emulating a project scaffolded before generation targets existed.
func stripTargetsFromConfig(t *testing.T) {
	t.Helper()
	data, err := os.ReadFile(projectConfigFilename)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	delete(raw, "targets")
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(projectConfigFilename, out, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// replaceStdinWithPipe swaps os.Stdin for the read end of a pipe (a non-char
// device), making isInteractiveInput() report false. Returns a restore func.
func replaceStdinWithPipe(t *testing.T) func() {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	old := os.Stdin
	os.Stdin = r
	return func() {
		os.Stdin = old
		_ = r.Close()
		_ = w.Close()
	}
}

// driveStdioToolsList runs bin (with args) from dir, feeds it the MCP stdio
// initialize/tools-list script, and returns the captured stdout. Stdin is a
// pipe kept open briefly after the requests (mirroring the Makefile smoke's
// `sleep 2`) so the server flushes the tools/list response before EOF shuts it
// down.
func driveStdioToolsList(t *testing.T, dir, bin string, args ...string) string {
	t.Helper()

	script := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		"",
	}, "\n")

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}

	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Stdin = stdinR
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s %v: %v", bin, args, err)
	}
	_ = stdinR.Close() // the child holds its own copy

	go func() {
		_, _ = stdinW.Write([]byte(script))
		time.Sleep(2 * time.Second)
		_ = stdinW.Close()
	}()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}

	if stderr.Len() > 0 {
		t.Logf("stderr from %s %v:\n%s", bin, args, stderr.String())
	}
	return stdout.String()
}

// TestMCPOnlyBareBinaryServesStdio builds the mcp-only scaffold binary and
// drives it with a stdio initialize/tools-list script — with NO `mcp` argument
// — to prove the MCP-first binary serves the transport directly. It also runs
// the bare binary with `--read-only` to prove the mcp command's flags parse at
// the root (the mcp flag set is mirrored onto root in mcp-only mode).
func TestMCPOnlyBareBinaryServesStdio(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping binary build in -short mode")
	}

	tmp := scaffoldAndGenerate(t, "mcponly", []string{"mcp"})

	bin := filepath.Join(tmp, "mcponly-bin")
	build := exec.Command("go", "build", "-o", bin, "./cmd/mcponly")
	build.Dir = tmp
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build mcp-only binary: %v\n%s", err, string(out))
	}

	// Bare invocation: no "mcp" arg.
	out := driveStdioToolsList(t, tmp, bin)
	if !strings.Contains(out, `"tools"`) {
		t.Fatalf("bare mcp-only binary did not emit a tools list; got:\n%s", out)
	}

	// Bare invocation with an mcp flag at the root: must not die with
	// "unknown flag" and must still answer tools/list.
	out = driveStdioToolsList(t, tmp, bin, "--read-only")
	if !strings.Contains(out, `"tools"`) {
		t.Fatalf("bare mcp-only binary with --read-only did not emit a tools list; got:\n%s", out)
	}
}
