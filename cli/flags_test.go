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
