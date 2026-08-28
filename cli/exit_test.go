package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
)

// executeForExit runs a command line the way a generated CLI's main does, and
// returns stdout, stderr, and the exit code the process would use.
func executeForExit(cmd string) (string, string, int) {
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	Root.SetArgs(strings.Split(cmd, " "))
	Root.SetOut(stdout)
	Root.SetErr(stderr)
	Stdout = stdout
	Stderr = stderr
	code := Execute()
	return stdout.String(), stderr.String(), code
}

// executeContextForExit is executeForExit through the context-taking entrypoint
// used by CLIs that cancel on SIGINT/SIGTERM.
func executeContextForExit(ctx context.Context, cmd string) (string, string, int) {
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	Root.SetArgs(strings.Split(cmd, " "))
	Root.SetOut(stdout)
	Root.SetErr(stderr)
	Stdout = stdout
	Stderr = stderr
	code := ExecuteContext(ctx)
	return stdout.String(), stderr.String(), code
}

func initExitTestCLI(t *testing.T) {
	t.Helper()

	viper.Reset()
	Init(&Config{AppName: "test"})

	group := &cobra.Command{Use: "group", Short: "A group of commands"}
	group.AddCommand(&cobra.Command{
		Use:  "leaf",
		Args: cobra.NoArgs,
		Run:  func(cmd *cobra.Command, args []string) {},
	})
	Root.AddCommand(group)

	Root.AddCommand(&cobra.Command{
		Use:  "boom",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return errors.New("operation failed")
		},
	})
}

func TestExitCodes(t *testing.T) {
	for _, tc := range []struct {
		name string
		args string
		code int
	}{
		{"success", "group leaf", ExitOK},
		{"unknown command", "definitely-not-a-command", ExitUsage},
		{"unknown subcommand of a group", "group definitely-not-a-command", ExitUsage},
		{"unknown shorthand flag", "group leaf -Z value", ExitUsage},
		{"unknown long flag", "group leaf --nope", ExitUsage},
		{"too many args", "group leaf extra", ExitUsage},
		{"runtime failure", "boom", ExitError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			initExitTestCLI(t)
			stdout, _, code := executeForExit(tc.args)
			assert.Equal(t, tc.code, code)

			if tc.code != ExitOK {
				// Diagnostics belong on stderr; anything on stdout would
				// corrupt a caller piping the output.
				assert.Empty(t, stdout)
			}
		})
	}
}

// ExecuteContext shares its classification with Execute, which TestExitCodes
// covers. What is its own is the context: it has to reach the handler, which is
// the only reason to call it over Execute.
func TestExecuteContextPassesTheContextToTheHandler(t *testing.T) {
	initExitTestCLI(t)

	type ctxKey struct{}
	var seen context.Context
	Root.AddCommand(&cobra.Command{
		Use:  "ctx",
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			seen = cmd.Context()
		},
	})

	ctx := context.WithValue(context.Background(), ctxKey{}, "value")
	_, _, code := executeContextForExit(ctx, "ctx")

	assert.Equal(t, ExitOK, code)
	assert.Equal(t, "value", seen.Value(ctxKey{}))
}

func TestUnknownSubcommandReportsTheOffendingArg(t *testing.T) {
	initExitTestCLI(t)

	_, stderr, code := executeForExit("group definitely-not-a-command")

	assert.Equal(t, ExitUsage, code)
	// Root.Use is the binary name, which differs under `go test`.
	assert.Contains(t, stderr, `unknown command "definitely-not-a-command" for "`+Root.Name()+` group"`)
	// The usage block is reprinted on stderr rather than stdout.
	assert.Contains(t, stderr, "Available Commands:")
}

func TestExitCodeForClassifiesErrors(t *testing.T) {
	assert.Equal(t, ExitOK, ExitCodeFor(nil))
	assert.Equal(t, ExitError, ExitCodeFor(errors.New("boom")))
	assert.Equal(t, ExitUsage, ExitCodeFor(NewUsageError(errors.New("bad flag"))))
	assert.Equal(t, ExitUsage, ExitCodeFor(fmt.Errorf(`unknown command "x" for "test"`)))

	// A usage error stays one after being wrapped on the way out.
	wrapped := fmt.Errorf("context: %w", NewUsageError(errors.New("bad flag")))
	assert.Equal(t, ExitUsage, ExitCodeFor(wrapped))
}

func TestUsageErrorUnwrapsToItsCause(t *testing.T) {
	cause := errors.New("unknown shorthand flag: 'q' in -q")
	err := NewUsageError(cause)

	assert.Equal(t, cause.Error(), err.Error())
	assert.True(t, errors.Is(err, cause))
}

// Group commands are still allowed to show their help when invoked bare.
func TestGroupWithoutArgsShowsHelp(t *testing.T) {
	initExitTestCLI(t)

	stdout, _, code := executeForExit("group")

	assert.Equal(t, ExitOK, code)
	assert.Contains(t, stdout, "leaf")
}

// A bad body-flag value is the user's mistake, so it exits 2 (usage) like any
// other bad input, not 1 (operation failure). Generated commands reach this
// through GetBodyWithFlags, which is where the classification lives.
func TestBodyFlagErrorIsUsageError(t *testing.T) {
	viper.Reset()
	Init(&Config{AppName: "test"})

	fields := []BodyField{{
		Name:     "status",
		FlagName: "status",
		Type:     "enum-string",
		Enum:     []string{"active", "archived"},
	}}

	cmd := &cobra.Command{
		Use: "create",
		RunE: func(cmd *cobra.Command, args []string) error {
			params := viper.New()
			_, err := GetBodyWithFlags(cmd, "application/json", args, params, fields)
			if err != nil {
				// The generated template wraps rather than classifies.
				return fmt.Errorf("unable to get body: %w", err)
			}

			return nil
		},
	}
	AddBodyFieldFlags(cmd, fields)
	Root.AddCommand(cmd)
	defer Root.RemoveCommand(cmd)

	_, stderr, code := executeForExit("create --status bogus")

	assert.Equal(t, ExitUsage, code, "stderr: %s", stderr)
	assert.Contains(t, stderr, "is not one of")
}

// "error calling operation" would name something that never happened when the
// value was rejected before the request was built.
func TestOperationErrorLeavesUsageErrorsUnlabelled(t *testing.T) {
	usage := NewUsageError(errors.New(`--kind: "external" is not one of [internal, a2a]`))
	if got := OperationError(usage); got.Error() != usage.Error() {
		t.Errorf("OperationError(usage) = %q, want it unchanged", got)
	}

	failure := errors.New("connection refused")
	if got := OperationError(failure); got.Error() != "error calling operation: connection refused" {
		t.Errorf("OperationError(failure) = %q, want it labelled", got)
	}

	if OperationError(nil) != nil {
		t.Error("OperationError(nil) should stay nil")
	}
}
