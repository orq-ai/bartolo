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
	assert.Contains(t, stderr, `--output-format: "not-a-format" is not one of [json, yaml, toon]`)
	// Nothing may reach stdout: the whole point is that the caller does not
	// get output in a format it did not ask for.
	assert.Empty(t, stdout)
}

func TestValidOutputFormatIsAccepted(t *testing.T) {
	for _, tc := range []struct {
		args string
		want string
	}{
		{"group leaf -o json", "json"},
		{"group leaf -o yaml", "yaml"},
		{"group leaf -o toon", "toon"},
		// Values are normalized the same way the persisted default is.
		{"group leaf -o YAML", "yaml"},
		// The --json alias still wins over an explicit format.
		{"group leaf -o yaml --json", "json"},
	} {
		t.Run(tc.args, func(t *testing.T) {
			initExitTestCLI(t)

			_, _, code := executeForExit(tc.args)

			assert.Equal(t, ExitOK, code)
			assert.Equal(t, tc.want, viper.GetString("output-format"))
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
	assert.Contains(t, stderr, `--output-format: "not-a-format" is not one of [json, yaml, toon]`)
}

func TestDefaultFormatCommandRejectsUnknownFormat(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	initExitTestCLI(t)

	_, stderr, code := executeForExit("default-format not-a-format")

	assert.Equal(t, ExitUsage, code)
	assert.Contains(t, stderr, `"not-a-format" is not one of [json, yaml, toon]`)

	_, err := os.Stat(filepath.Join(home, ".test", "config.json"))
	assert.True(t, os.IsNotExist(err), "a rejected format must not be persisted")
}
