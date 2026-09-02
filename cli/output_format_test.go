package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
			assert.Equal(t, tc.want, OutputFormat())
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

// A CLI's configured serialization is what it falls back to, never what it
// starts at — the bug RES-1483 reported was the two being the same knob.
func TestOutputFormatDefaultsToTableWhateverTheCLIConfigured(t *testing.T) {
	for _, tc := range []struct {
		name          string
		serialization string
		env           string
		want          string
		wantFallback  string
	}{
		{"unconfigured", "", "", "table", "json"},
		{"configured toon", "toon", "", "table", "toon"},
		{"configured yaml", "yaml", "", "table", "yaml"},
		{"table cannot be its own fallback", "table", "", "table", "json"},
		{"from env", "yaml", "json", "json", "json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			viper.Reset()
			t.Setenv("HOME", t.TempDir())
			t.Setenv("TEST_OUTPUT_FORMAT", tc.env)

			Init(&Config{AppName: "test", EnvPrefix: "TEST", SerializationFormat: tc.serialization})
			Root.RunE = func(cmd *cobra.Command, args []string) error { return nil }

			_, _, code := executeForExit("")

			assert.Equal(t, ExitOK, code)
			assert.Equal(t, tc.want, OutputFormat())
			assert.Equal(t, tc.wantFallback, serializationFormat())
			// RES-1483 itself: a configured serialization must not kill the table.
			assert.Equal(t, tc.want == tableFormat, NewDefaultFormatter(true, true).shouldRenderTable())
		})
	}
}

// The normalized format goes to its own key so one command cannot pin the next.
func TestOutputFormatIsResolvedPerCommand(t *testing.T) {
	initExitTestCLI(t)

	_, _, code := executeForExit("group leaf")
	assert.Equal(t, ExitOK, code)
	assert.Equal(t, tableFormat, OutputFormat())

	_, _, code = executeForExit("group leaf -o yaml")
	assert.Equal(t, ExitOK, code)
	assert.Equal(t, "yaml", OutputFormat())
}

// A pinned format is how a user opts out of tables, and `table` is how they
// opt back in — the mechanism this PR chose over an implicit predicate.
func TestPersistedFormatDecidesWhetherTablesRender(t *testing.T) {
	for _, tc := range []struct {
		name      string
		pinned    string
		args      string
		want      string
		wantTable bool
	}{
		{"a pin turns tables off", "toon", "", "toon", false},
		{"and back on", "table", "", "table", true},
		// A pin is not a one-way door: the flag still outranks it.
		{"an explicit flag beats the pin", "toon", "-o table", "table", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			viper.Reset()
			home := t.TempDir()
			t.Setenv("HOME", home)
			require.NoError(t, os.MkdirAll(filepath.Join(home, ".test"), 0700))
			require.NoError(t, os.WriteFile(filepath.Join(home, ".test", "config.json"),
				[]byte(`{"output-format": "`+tc.pinned+`"}`), 0600))

			Init(&Config{AppName: "test", EnvPrefix: "TEST", SerializationFormat: "toon"})
			Root.RunE = func(cmd *cobra.Command, args []string) error { return nil }

			_, _, code := executeForExit(tc.args)

			assert.Equal(t, ExitOK, code)
			assert.Equal(t, tc.want, OutputFormat())
			assert.Equal(t, tc.wantTable, NewDefaultFormatter(true, true).shouldRenderTable())
		})
	}
}

// Init must derive the table gate from a real terminal, not from forced color,
// or `--color | cat` starts emitting a table.
func TestInitKeepsForcedColorOffTheTableGate(t *testing.T) {
	viper.Reset()
	t.Setenv("HOME", t.TempDir())
	viper.Set("color", true)

	Init(&Config{AppName: "test", EnvPrefix: "TEST"})

	formatter, ok := Formatter.(*DefaultFormatter)
	require.True(t, ok)
	assert.True(t, formatter.tty, "forced color must still colorize")
	assert.False(t, formatter.terminal, "test stdout is a pipe, so no table")
}
