package cli

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
)

func TestCustomFlags(t *testing.T) {
	root := &cobra.Command{
		Use: "main",
	}

	cmd := &cobra.Command{
		Use: "test",
	}

	root.AddCommand(cmd)

	AddFlag("test", "bool", "b", "description", false)
	AddFlag("test", "int", "i", "description", 0)
	AddFlag("test", "float", "f", "description", 0.0)
	AddFlag("test", "string", "s", "description", "")

	SetCustomFlags(cmd)

	assert.NotNil(t, cmd.Flags().Lookup("bool"))
	assert.NotNil(t, cmd.Flags().Lookup("int"))
	assert.NotNil(t, cmd.Flags().Lookup("float"))
	assert.NotNil(t, cmd.Flags().Lookup("string"))
}

func TestReservedFlagNamesCoverEveryGlobalFlag(t *testing.T) {
	viper.Reset()
	Cache = nil
	Client = nil
	Root = nil

	Init(&Config{AppName: "test-reserved"})
	InitCredentialsFile()

	Root.PersistentFlags().VisitAll(func(flag *pflag.Flag) {
		if _, reserved := ReservedFlagName(flag.Name); !reserved {
			t.Errorf("global flag --%s is not in reservedFlagNames, so a generated body field or parameter named %q would silently shadow it", flag.Name, flag.Name)
		}
	})
}

// `-j` is easy to read as short for `--json`. Since --jmespath takes a value, a
// stray `-j` swallows the following argument instead of failing cleanly, so
// --json must never grow a shorthand that makes the two look interchangeable.
func TestJSONFlagHasNoShorthand(t *testing.T) {
	viper.Reset()
	Cache = nil
	Client = nil
	Root = nil

	Init(&Config{AppName: "test-shorthand"})

	assert.Equal(t, "j", Root.PersistentFlags().Lookup("jmespath").Shorthand)
	assert.Empty(t, Root.PersistentFlags().Lookup("json").Shorthand, "--json must stay shorthand-free so -j is unambiguous")
}

func TestResolveGeneratedFlagName(t *testing.T) {
	// Untouched when there is nothing to collide with.
	assert.Equal(t, "limit", ResolveGeneratedFlagName("body", "limit"))
	assert.Equal(t, "page-token", ResolveGeneratedFlagName("param", "page-token"))

	// `query` is deliberately not reserved: the global JMESPath flag is named
	// `jmespath` precisely so endpoints can keep --query.
	assert.Equal(t, "query", ResolveGeneratedFlagName("body", "query"))
	assert.Equal(t, "query", ResolveGeneratedFlagName("param", "query"))

	// Reserved names get out of the way of the global flag.
	assert.Equal(t, "body-jmespath", ResolveGeneratedFlagName("body", "jmespath"))
	assert.Equal(t, "body-output-format", ResolveGeneratedFlagName("body", "output-format"))
	assert.Equal(t, "param-profile", ResolveGeneratedFlagName("param", "profile"))
	assert.Equal(t, "body-raw", ResolveGeneratedFlagName("body", "raw"))
	// pflag panics on a duplicate, so these matter even more than the globals.
	assert.Equal(t, "body-stdin", ResolveGeneratedFlagName("body", "stdin"))
	assert.Equal(t, "body-help", ResolveGeneratedFlagName("body", "help"))

	// Idempotent: re-resolving an already-renamed flag is a no-op, so the
	// generator and the runtime agree on the final name.
	assert.Equal(t, "body-raw", ResolveGeneratedFlagName("body", "body-raw"))
}

func TestHelpSectionCreatesSectionOnceAndSetsGroupID(t *testing.T) {
	root := &cobra.Command{Use: "root"}
	first := &cobra.Command{Use: "first"}
	second := &cobra.Command{Use: "second"}

	HelpSection(root, first, "Managed agents")
	root.AddCommand(first)
	HelpSection(root, second, "Managed agents")
	root.AddCommand(second)

	if len(root.Groups()) != 1 {
		t.Fatalf("expected 1 help group, got %d", len(root.Groups()))
	}
	if title := root.Groups()[0].Title; title != "Managed agents:" {
		t.Fatalf("expected trailing colon in title, got %q", title)
	}
	if first.GroupID != "Managed agents" || second.GroupID != "Managed agents" {
		t.Fatalf("expected both commands in the group, got %q and %q", first.GroupID, second.GroupID)
	}

	unsectioned := &cobra.Command{Use: "third"}
	HelpSection(root, unsectioned, "")
	root.AddCommand(unsectioned)
	if unsectioned.GroupID != "" {
		t.Fatalf("expected empty section to be a no-op, got %q", unsectioned.GroupID)
	}
}

func TestSweepIntoOtherHelpSection(t *testing.T) {
	root := &cobra.Command{Use: "root"}
	sectioned := &cobra.Command{Use: "files"}
	HelpSection(root, sectioned, "Storage")
	root.AddCommand(sectioned)
	loose := &cobra.Command{Use: "doctor"}
	root.AddCommand(loose)

	sweepIntoOtherHelpSection(root)

	if loose.GroupID != HelpSectionOther {
		t.Fatalf("expected ungrouped command in %q, got %q", HelpSectionOther, loose.GroupID)
	}
	if sectioned.GroupID != "Storage" {
		t.Fatalf("expected sectioned command to keep its section, got %q", sectioned.GroupID)
	}
	if !root.AllChildCommandsHaveGroup() {
		t.Fatal("expected no command left for cobra's Additional Commands block")
	}

	// Other must render last, after the sections the schema named.
	groups := root.Groups()
	if groups[len(groups)-1].ID != HelpSectionOther {
		t.Fatalf("expected %q to be the last section, got %q", HelpSectionOther, groups[len(groups)-1].ID)
	}
}

func TestSweepIntoOtherHelpSectionSkipsUnsectionedCLIs(t *testing.T) {
	root := &cobra.Command{Use: "root"}
	root.AddCommand(&cobra.Command{Use: "doctor"})

	sweepIntoOtherHelpSection(root)

	if len(root.Groups()) != 0 {
		t.Fatalf("expected no sections for a CLI that uses none, got %d", len(root.Groups()))
	}
}

// A flag the user typed is "set" even when its value is the zero value, so
// `--detailed=false` against a server-side default of true reaches the API.
// Body fields already worked this way via ApplyBodyFlags; parameters did not.
func TestFlagPassedDistinguishesZeroValueFromUnset(t *testing.T) {
	params := viper.New()
	cmd := &cobra.Command{Use: "list"}
	cmd.Flags().Bool("detailed", false, "")
	cmd.Flags().String("kind", "", "")
	if err := cmd.Flags().Parse([]string{"--detailed=false"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}

	MarkPassedFlags(cmd, params)

	if !FlagPassed(params, "detailed") {
		t.Error("an explicitly-passed --detailed=false should count as passed")
	}
	if FlagPassed(params, "kind") {
		t.Error("an untouched flag should not count as passed")
	}

	// The bookkeeping stays out of the settings map waiter matchers read.
	if _, found := RequestParams(params)["__passed_flags"]; found {
		t.Error("RequestParams should not expose the passed-flag bookkeeping")
	}

	// A params built by hand has no flags, so generated code falls back to sending non-zero values.
	if FlagPassed(viper.New(), "detailed") {
		t.Error("a hand-built params should report nothing as passed")
	}
}
