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

// execute a command against the configured CLI
func execute(cmd string) string {
	out := new(bytes.Buffer)
	Root.SetArgs(strings.Split(cmd, " "))
	Root.SetOutput(out)
	Stdout = out
	Stderr = out
	Root.Execute()
	return out.String()
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

	assert.Equal(t, "yaml", viper.GetString("output-format"))

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
