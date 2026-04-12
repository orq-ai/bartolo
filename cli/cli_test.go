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
