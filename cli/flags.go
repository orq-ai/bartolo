package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// reservedFlagNames are the flag names a generated command must not reuse for
// one of its own parameter or request-body flags.
//
// Cobra merges a parent's persistent flags into a command's flag set with
// pflag's AddFlagSet, which skips any flag whose name is already taken. A
// generated local `--raw` therefore does not just win the long name, it removes
// the global `--raw` from that command entirely — and where the global has a
// shorthand, that stops resolving too. The body-input and help entries are
// worse still: registering a duplicate makes pflag panic at startup.
//
// The JMESPath filter is deliberately called `jmespath` rather than `query`, so
// the many endpoints with a `query` field can keep the obvious flag name.
//
// The list applies to every generated command, whatever its HTTP method. `force` is
// only registered on Delete commands, so a `force` parameter on a GET is renamed
// without anything to collide with — that is deliberate. One name resolves to one flag
// across the whole CLI, so `--param-force` means the same thing on every command, and
// the generator and the runtime (which redoes this resolution for CLIs generated before
// a fix) cannot disagree about a name without knowing each command's method.
var reservedFlagNames = map[string]string{
	// Global flags, registered by Init and InitCredentialsFile.
	"jmespath":      "global flag",
	"json":          "global flag",
	"output-format": "global flag",
	"profile":       "global flag",
	"raw":           "global flag",
	"server":        "global flag",
	"verbose":       "global flag",

	// Cobra installs this on every command that does not already have it.
	"help": "built-in flag",

	// Request-body input flags, registered by AddBodyFlags — except
	// `example`, which AddExampleFlag installs on commands that have one.
	"example":   "request-body flag",
	"from-file": "request-body flag",
	"stdin":     "request-body flag",

	// Registered on Delete commands by the templates; a colliding spec field would
	// register `--force` twice and panic pflag at startup.
	"force": "reserved flag name",
}

// AddForceFlag registers the `--force` flag that ConfirmDestructive reads on a
// destructive command. No shorthand: `-f` is commonly claimed by consumers through
// cli.AddFlag, and pflag panics on a duplicate shorthand at startup.
func AddForceFlag(cmd *cobra.Command) {
	cmd.Flags().Bool("force", false, "Skip the confirmation prompt; required when not running on a terminal")
}

// ReservedFlagName reports whether name is reserved for a global or built-in
// flag, along with a short description of what claims it. Generated CLIs use
// this to rename colliding parameter and body-field flags.
func ReservedFlagName(name string) (string, bool) {
	kind, ok := reservedFlagNames[name]
	return kind, ok
}

// ResolveGeneratedFlagName returns the flag name a generated command should use
// for an operation parameter or request-body field whose natural name is name.
// Reserved names are prefixed (`raw` -> `body-raw`) so the global flag they
// would otherwise shadow keeps working on every command.
//
// This is applied by the generator and again at runtime, so a CLI that picks up
// a newer bartolo/cli without being regenerated is fixed too. It is idempotent:
// an already-prefixed name is left alone.
func ResolveGeneratedFlagName(prefix, name string) string {
	if _, reserved := ReservedFlagName(name); !reserved {
		return name
	}
	return prefix + "-" + name
}

// ReservedFlagNames returns the reserved flag names in sorted order.
func ReservedFlagNames() []string {
	names := make([]string, 0, len(reservedFlagNames))
	for name := range reservedFlagNames {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// AddGlobalFlag will make a new global flag on the root command.
func AddGlobalFlag(name, short, description string, defaultValue interface{}) {
	viper.SetDefault(name, defaultValue)

	flags := Root.PersistentFlags()
	switch v := defaultValue.(type) {
	case bool:
		flags.BoolP(name, short, viper.GetBool(name), description)
	case int, int16, int32, int64, uint16, uint32, uint64:
		flags.IntP(name, short, viper.GetInt(name), description)
	case float32, float64:
		flags.Float64P(name, short, viper.GetFloat64(name), description)
	default:
		flags.StringP(name, short, fmt.Sprintf("%v", v), description)
	}
	viper.BindPFlag(name, flags.Lookup(name))
}

type flagDef struct {
	name         string
	short        string
	description  string
	defaultValue interface{}
}

var flagRegistry = make(map[string][]*flagDef)

// AddFlag registers a new custom flag for the command path. Use the
// `RegisterBefore` and `RegisterAfter` functions to register a handler that
// can check the value of this flag.
func AddFlag(path, name, short, description string, defaultValue interface{}) {
	if _, ok := flagRegistry[path]; !ok {
		flagRegistry[path] = make([]*flagDef, 0, 1)
	}

	flagRegistry[path] = append(flagRegistry[path], &flagDef{
		name:         name,
		short:        short,
		description:  description,
		defaultValue: defaultValue,
	})
}

// SetCustomFlags sets up the command with additional registered flags.
func SetCustomFlags(cmd *cobra.Command) {
	path := commandPath(cmd)

	if flags, ok := flagRegistry[path]; ok {
		for _, f := range flags {
			switch v := f.defaultValue.(type) {
			case bool:
				cmd.Flags().BoolP(f.name, f.short, v, f.description)
			case int:
				cmd.Flags().Int64P(f.name, f.short, int64(v), f.description)
			case int32:
				cmd.Flags().Int64P(f.name, f.short, int64(v), f.description)
			case int64:
				cmd.Flags().Int64P(f.name, f.short, v, f.description)
			case float32:
				cmd.Flags().Float64P(f.name, f.short, float64(v), f.description)
			case float64:
				cmd.Flags().Float64P(f.name, f.short, v, f.description)
			default:
				cmd.Flags().StringP(f.name, f.short, fmt.Sprintf("%v", v), f.description)
			}
		}
	}
}

// HelpSectionOther is the heading every command without its own section ends
// up under, once at least one command has one. Cobra's own fallback bucket is
// titled "Additional Commands", which reads as leftovers next to named
// sections — and it is what an unsectioned new command would silently land in.
const HelpSectionOther = "Other"

// HelpSection places cmd under a titled section in parent's help output,
// creating the section on first use. Cobra panics in AddCommand when a command
// carries a GroupID the parent does not know, so both happen here.
func HelpSection(parent *cobra.Command, cmd *cobra.Command, title string) {
	if parent == nil || cmd == nil || title == "" {
		return
	}

	ensureHelpSection(parent, title)
	cmd.GroupID = title
}

// sweepIntoOtherHelpSection puts every remaining ungrouped command under
// HelpSectionOther, so a command tree that uses sections at all does not also
// show cobra's "Additional Commands" block. It runs at execute time because
// that is the first point where every command is registered, including the ones
// a consuming CLI adds after Register.
//
// It recurses because operations inside a generated group get their sections
// from the same x-cli-help-section extension as top-level ones, so a group can
// hold a mix of sectioned and unsectioned subcommands.
func sweepIntoOtherHelpSection(root *cobra.Command) {
	if root == nil {
		return
	}

	for _, cmd := range root.Commands() {
		sweepIntoOtherHelpSection(cmd)
	}

	if len(root.Groups()) == 0 {
		return
	}

	ensureHelpSection(root, HelpSectionOther)

	for _, cmd := range root.Commands() {
		if cmd.GroupID == "" {
			cmd.GroupID = HelpSectionOther
		}
	}

	// Cobra creates these two lazily during Execute, after the loop above has
	// already run, so they need their group set rather than assigned.
	root.SetHelpCommandGroupID(HelpSectionOther)
	root.SetCompletionCommandGroupID(HelpSectionOther)
}

func ensureHelpSection(parent *cobra.Command, title string) {
	for _, group := range parent.Groups() {
		if group.ID == title {
			return
		}
	}

	heading := title
	if !strings.HasSuffix(heading, ":") {
		heading += ":"
	}

	parent.AddGroup(&cobra.Group{ID: title, Title: heading})
}

// passedFlagsKey holds the set of flags the user actually typed, so a generated
// operation can tell "unset" from "set to the zero value". It lives in the
// operation's viper rather than in the operation function's signature so that
// signature stays free of cobra, and it is spelled with underscores because
// slug() cannot produce those — no parameter can collide with it.
const passedFlagsKey = "__passed_flags"

// MarkPassedFlags records which flags were given on the command line. Generated
// commands call it before building a request; body fields get the same
// treatment directly from cmd in ApplyBodyFlags.
func MarkPassedFlags(cmd *cobra.Command, params *viper.Viper) {
	if cmd == nil || params == nil {
		return
	}

	passed := map[string]bool{}
	cmd.Flags().Visit(func(flag *pflag.Flag) {
		passed[flag.Name] = true
	})

	params.Set(passedFlagsKey, passed)
}

// RequestParams is params.AllSettings without the passed-flag bookkeeping, for
// callers that treat the settings map as the user's own parameters — waiter
// matchers, for one.
func RequestParams(params *viper.Viper) map[string]interface{} {
	if params == nil {
		return map[string]interface{}{}
	}

	settings := params.AllSettings()
	delete(settings, passedFlagsKey)

	return settings
}

// FlagPassed reports whether name was given on the command line. It is false
// for a params built by hand, which is why generated code also sends any
// non-zero value: a library caller sets values rather than passing flags.
func FlagPassed(params *viper.Viper, name string) bool {
	if params == nil {
		return false
	}

	passed, _ := params.Get(passedFlagsKey).(map[string]bool)

	return passed[name]
}
