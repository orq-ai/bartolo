package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// Process exit codes used by generated CLIs. Scripts branch on these, so every
// failure must map to a non-zero code. Usage errors get their own code as
// recommended by https://clig.dev so wrappers can tell "you typed it wrong"
// apart from "the operation failed".
const (
	ExitOK    = 0
	ExitError = 1
	ExitUsage = 2
)

// UsageError marks an error as bad command-line input rather than a failed
// operation, so it exits ExitUsage. Whether the command's usage block is worth
// printing alongside it depends on which kind of bad input it is — see the two
// constructors.
type UsageError struct {
	err       error
	showUsage bool
}

// NewUsageError wraps err as an invocation the CLI could not make sense of: an
// unknown command, an unknown flag, a value cobra could not parse into the
// flag's type. The usage block is the answer to those, so it is printed.
func NewUsageError(err error) *UsageError {
	return &UsageError{err: err, showUsage: true}
}

// NewValueError wraps err as a well-formed invocation carrying a value the CLI
// will not accept — outside an enum, the wrong format, missing where it is
// required. The usage block is noise here: the reader knows how to call the
// command, and the message already names the offending value.
func NewValueError(err error) *UsageError {
	return &UsageError{err: err}
}

func (e *UsageError) Error() string {
	return e.err.Error()
}

func (e *UsageError) Unwrap() error {
	return e.err
}

// Execute runs the root command and returns the process exit code. Generated
// CLIs pass the result straight to os.Exit so that no error path reports
// success.
func Execute() int {
	return ExecuteContext(context.Background())
}

// ExecuteContext is Execute with a caller-supplied context, for CLIs that
// cancel in-flight requests on SIGINT/SIGTERM. It exists so those CLIs do not
// have to call Root.ExecuteContext themselves: doing that skips the exit-code
// contract below and lets cobra-level failures report success.
func ExecuteContext(ctx context.Context) int {
	rejectUnknownSubcommands(Root)
	sweepIntoOtherHelpSection(Root)

	// Cobra prints the usage block on failure with Println, which goes to
	// stdout and corrupts piped output. Silence it and reprint it on stderr
	// below, for usage errors only — a command that parsed fine and then
	// failed does not need its own usage repeated back.
	Root.SilenceUsage = true

	Root.SilenceErrors = true

	cmd, err := Root.ExecuteContextC(ctx)

	if err != nil && !errors.Is(err, ErrDestructiveRefused) {
		fmt.Fprintln(Stderr, "Error:", err)
	}

	var usage *UsageError
	if errors.As(err, &usage) && usage.showUsage && cmd != nil {
		fmt.Fprintln(Stderr, cmd.UsageString())
	}

	return ExitCodeFor(err)
}

// OperationError labels an error from a generated operation. A usage error is
// returned untouched: the value never reached the API, so "error calling
// operation" would name something that did not happen.
func OperationError(err error) error {
	var usage *UsageError
	if err == nil || errors.As(err, &usage) {
		return err
	}

	return fmt.Errorf("error calling operation: %w", err)
}

// ExitCodeFor maps an error returned by Root.Execute to a process exit code.
func ExitCodeFor(err error) int {
	if err == nil {
		return ExitOK
	}

	if errors.Is(err, ErrDestructiveRefused) {
		return ExitOK
	}

	var usage *UsageError
	if errors.As(err, &usage) {
		return ExitUsage
	}

	// Cobra resolves the command before any handler runs and reports an
	// unresolvable one with a plain fmt.Errorf, so there is nothing to type
	// assert on. The message is fixed in cobra's args.go.
	if strings.HasPrefix(err.Error(), "unknown command ") {
		return ExitUsage
	}

	return ExitError
}

// rejectUnknownSubcommands teaches group commands, which exist only to hold
// subcommands, to reject an unknown one. Cobra checks for unknown commands only
// at the root, and it short-circuits a command without a handler to "show help"
// before any argument validator runs — so `cli group bogus` otherwise prints the
// group's help and exits 0. Giving the group a handler is what puts the
// remaining arguments back within reach.
func rejectUnknownSubcommands(cmd *cobra.Command) {
	if cmd == nil {
		return
	}

	for _, child := range cmd.Commands() {
		rejectUnknownSubcommands(child)
	}

	if !cmd.HasParent() || !cmd.HasSubCommands() || cmd.Runnable() {
		return
	}

	cmd.RunE = func(c *cobra.Command, args []string) error {
		if len(args) == 0 {
			// Bare group invocation keeps cobra's default: show the help.
			return c.Help()
		}
		return NewUsageError(fmt.Errorf("unknown command %q for %q", args[0], c.CommandPath()))
	}
}
