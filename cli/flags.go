package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
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
// HelpSectionOther, so a CLI that uses sections at all does not also show
// cobra's "Additional Commands" block. It runs at execute time because that is
// the first point where every command is registered, including the ones a
// consuming CLI adds after Register.
func sweepIntoOtherHelpSection(root *cobra.Command) {
	if root == nil || len(root.Groups()) == 0 {
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
