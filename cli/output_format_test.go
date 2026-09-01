package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
)

func TestInvalidOutputFormatIsRejected(t *testing.T) {
	initExitTestCLI(t)

	stdout, stderr, code := executeForExit("group leaf -o not-a-format")

	assert.Equal(t, ExitUsage, code)
	assert.Contains(t, stderr, `--output-format: "not-a-format" is not one of [json, yaml, toon, table]`)
	// Nothing may reach stdout: the whole point is that the caller does not
	// get output in a format it did not ask for.
	assert.Empty(t, stdout)
}

// --json forced JSON past an explicit --output-format, so it was never the alias it claimed.
func TestJSONFlagIsGone(t *testing.T) {
	initExitTestCLI(t)

	_, stderr, code := executeForExit("group leaf --json")

	assert.NotEqual(t, ExitOK, code)
	assert.Contains(t, stderr, "unknown flag: --json")
}

func TestValidOutputFormatIsAccepted(t *testing.T) {
	for _, tc := range []struct {
		args string
		want string
	}{
		{"group leaf -o json", "json"},
		{"group leaf -o yaml", "yaml"},
		{"group leaf -o toon", "toon"},
		{"group leaf -o table", "table"},
		// Values are normalized the same way the persisted default is.
		{"group leaf -o YAML", "yaml"},
	} {
		t.Run(tc.args, func(t *testing.T) {
			initExitTestCLI(t)

			_, _, code := executeForExit(tc.args)

			assert.Equal(t, ExitOK, code)
			assert.Equal(t, tc.want, outputFormat())
		})
	}
}

// The format can also arrive from the environment or a config file, so the
// check lives where the value is consumed rather than on the flag itself.
func TestInvalidOutputFormatFromEnvIsRejected(t *testing.T) {
	viper.Reset()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TEST_OUTPUT_FORMAT", "not-a-format")

	Init(&Config{AppName: "test", EnvPrefix: "TEST"})
	Root.RunE = func(cmd *cobra.Command, args []string) error { return nil }

	_, stderr, code := executeForExit("")

	assert.Equal(t, ExitUsage, code)
	assert.Contains(t, stderr, `--output-format: "not-a-format" is not one of [json, yaml, toon, table]`)
}

func TestDefaultFormatCommandRejectsUnknownFormat(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	initExitTestCLI(t)

	_, stderr, code := executeForExit("default-format not-a-format")

	assert.Equal(t, ExitUsage, code)
	assert.Contains(t, stderr, `"not-a-format" is not one of [json, yaml, toon, table]`)

	_, err := os.Stat(filepath.Join(home, ".test", "config.json"))
	assert.True(t, os.IsNotExist(err), "a rejected format must not be persisted")
}

func TestOutputFormatDefaultsToTableAndEnvOverridesIt(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configured string
		env        string
		want       string
	}{
		{"unconfigured", "", "", "table"},
		{"configured", "toon", "", "toon"},
		{"from env", "", "yaml", "yaml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			viper.Reset()
			t.Setenv("HOME", t.TempDir())
			t.Setenv("TEST_OUTPUT_FORMAT", tc.env)

			Init(&Config{AppName: "test", EnvPrefix: "TEST", DefaultOutputFormat: tc.configured})
			Root.RunE = func(cmd *cobra.Command, args []string) error { return nil }

			_, _, code := executeForExit("")

			assert.Equal(t, ExitOK, code)
			assert.Equal(t, tc.want, outputFormat())
		})
	}
}

// The normalized format goes to its own key so one command cannot pin the next.
func TestOutputFormatIsResolvedPerCommand(t *testing.T) {
	initExitTestCLI(t)

	_, _, code := executeForExit("group leaf")
	assert.Equal(t, ExitOK, code)
	assert.Equal(t, tableFormat, outputFormat())

	_, _, code = executeForExit("group leaf -o yaml")
	assert.Equal(t, ExitOK, code)
	assert.Equal(t, "yaml", outputFormat())
}
