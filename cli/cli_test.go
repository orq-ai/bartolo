package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
)

// executeArgsStreams runs an explicit argument vector and returns stdout,
// stderr and Root.Execute()'s error. A test needing a genuine empty argument
// passes it as its own element rather than relying on how a string splits.
func executeArgsStreams(args []string) (stdout string, stderr string, err error) {
	outBuf := new(bytes.Buffer)
	errBuf := new(bytes.Buffer)
	Root.SetArgs(args)
	Root.SetOut(outBuf)
	Root.SetErr(errBuf)
	Stdout = outBuf
	Stderr = errBuf
	err = Root.Execute()
	return outBuf.String(), errBuf.String(), err
}

// executeStreams wraps executeArgsStreams for a plain command line, splitting
// on strings.Fields so it cannot manufacture an empty argument the way
// strings.Split did. It discards the execution error.
func executeStreams(cmd string) (stdout string, stderr string) {
	stdout, stderr, _ = executeArgsStreams(strings.Fields(cmd))
	return stdout, stderr
}

// execute is a thin stdout-only wrapper over executeStreams, kept so the
// many existing call sites that only care about the payload need no change.
func execute(cmd string) string {
	stdout, _ := executeStreams(cmd)
	return stdout
}

func TestInit(t *testing.T) {
	viper.Reset()
	Cache = nil
	Client = nil
	Root = nil

	viper.Set("color", true)

	Init(&Config{
		AppName: "test",
	})

	assert.NotNil(t, Cache)
	assert.NotNil(t, Client)
	assert.NotNil(t, Root)
}

func TestHelpCommands(t *testing.T) {
	viper.Reset()
	Init(&Config{
		AppName: "test",
	})

	out := execute("help-config")
	assert.Contains(t, out, "CLI Configuration")

	out = execute("help-input")
	assert.Contains(t, out, "CLI Request Input")
}

func TestCompletionCommand(t *testing.T) {
	viper.Reset()
	Init(&Config{
		AppName: "test",
	})

	out := execute("completion zsh")
	assert.Contains(t, out, "#compdef")
}

func TestPreRun(t *testing.T) {
	viper.Reset()
	Init(&Config{
		AppName: "test",
	})

	ran := false
	PreRun = func(cmd *cobra.Command, args []string) error {
		ran = true
		return nil
	}

	Root.Run = func(cmd *cobra.Command, args []string) {
		// Do nothing, but also don't error.
	}

	execute("")

	assert.True(t, ran)
}

func TestDefaultFormatCommandPersistsConfig(t *testing.T) {
	viper.Reset()
	home := t.TempDir()
	oldHome := os.Getenv("HOME")
	if err := os.Setenv("HOME", home); err != nil {
		t.Fatalf("set HOME: %v", err)
	}
	defer os.Setenv("HOME", oldHome)

	Cache = nil
	Client = nil
	Root = nil

	Init(&Config{
		AppName:             "test-default",
		DefaultOutputFormat: "yaml",
	})

	// The app default is the serialization to fall back to; the output format
	// itself defaults to a table so list commands render one on a terminal.
	assert.Equal(t, tableFormat, viper.GetString("output-format"))
	assert.Equal(t, "yaml", defaultSerialization)

	out := execute("default-format toon")
	assert.Contains(t, out, "output_format: toon")

	data, err := os.ReadFile(filepath.Join(home, ".test-default", "config.json"))
	if err != nil {
		t.Fatalf("read persisted config: %v", err)
	}

	assert.Contains(t, string(data), "\"output-format\": \"toon\"")
}

func TestInitLoadsDotEnvFile(t *testing.T) {
	viper.Reset()
	Cache = nil
	Client = nil
	Root = nil

	home := t.TempDir()
	oldHome := os.Getenv("HOME")
	if err := os.Setenv("HOME", home); err != nil {
		t.Fatalf("set HOME: %v", err)
	}
	defer os.Setenv("HOME", oldHome)

	oldAPIKey := os.Getenv("TEST_DOTENV_KEY")
	if err := os.Unsetenv("TEST_DOTENV_KEY"); err != nil {
		t.Fatalf("unset TEST_DOTENV_KEY: %v", err)
	}
	defer os.Setenv("TEST_DOTENV_KEY", oldAPIKey)

	cwd := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWD)

	if err := os.WriteFile(filepath.Join(cwd, ".env"), []byte("TEST_DOTENV_KEY=from-dotenv\n"), 0600); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	Init(&Config{
		AppName:      "test-dotenv",
		EnvPrefix:    "TEST",
		APIKeyEnvVar: "TEST_DOTENV_KEY",
	})

	if got := os.Getenv("TEST_DOTENV_KEY"); got != "from-dotenv" {
		t.Fatalf("expected dotenv value loaded into env, got %q", got)
	}
}
