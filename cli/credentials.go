package cli

import (
	"fmt"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"

	survey "github.com/AlecAivazis/survey/v2"
	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/tw"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/term"
	"gopkg.in/h2non/gentleman.v2/context"
)

// writeCredentials persists the credentials file at 0600, so the long-lived API keys
// it holds are never world-readable (viper.WriteConfigAs would create it 0644).
func writeCredentials(filename string) error {
	// Writes nothing: sets the mode before viper writes the key, so the secret is never
	// world-readable even briefly. The chmod narrows a file an older build left 0644.
	f, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(filename, 0o600); err != nil {
		return err
	}
	return Creds.WriteConfigAs(filename)
}

// AuthHandler describes a handler that can be called on a request to inject
// auth information and is agnostic to the type of auth.
type AuthHandler interface {
	// ProfileKeys returns the key names for fields to store in the profile.
	ProfileKeys() []string

	// OnRequest gets run before the request goes out on the wire.
	OnRequest(log *zerolog.Logger, request *http.Request) error
}

// AuthStatusHandler can describe whether authentication is configured.
type AuthStatusHandler interface {
	AuthStatus(profile map[string]string) map[string]interface{}
}

// AuthHandlers is the map of registered auth type names to handlers
var AuthHandlers = make(map[string]AuthHandler)

var authInitialized bool
var authCommand *cobra.Command
var authAddCommand *cobra.Command

// initAuth sets up basic commands and the credentials file so that new auth
// handlers can be registered. This is safe to call many times.
func initAuth() {
	if authInitialized {
		return
	}
	authInitialized = true

	// Set up the credentials file
	InitCredentialsFile()

	// Add base auth commands
	authCommand = &cobra.Command{
		Use:   "auth",
		Short: "Authentication settings",
	}
	Root.AddCommand(authCommand)

	authAddCommand = &cobra.Command{
		Use:     "add-profile",
		Aliases: []string{"add"},
		Short:   "Add user profile for authentication",
	}
	authCommand.AddCommand(authAddCommand)

	authCommand.AddCommand(&cobra.Command{
		Use:     "list-profiles",
		Aliases: []string{"ls"},
		Short:   "List available configured authentication profiles",
		Args:    cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			profiles := Creds.GetStringMap("profiles")

			if profiles != nil {
				// Use a map as a set to find the available auth type names.
				types := make(map[string]bool)
				for _, v := range profiles {
					if typeName := v.(map[string]interface{})["type"]; typeName != nil {
						types[typeName.(string)] = true
					}
				}

				// For each type name, draw a table with the relevant profile keys
				for typeName := range types {
					handler := AuthHandlers[typeName]
					if handler == nil {
						continue
					}

					listKeys := handler.ProfileKeys()

					table := tablewriter.NewTable(os.Stdout, tablewriter.WithHeaderAutoFormat(tw.Off))
					table.Header(append([]string{fmt.Sprintf("%s Profile Name", typeName)}, listKeys...))

					for name, p := range profiles {
						profile := p.(map[string]interface{})
						if ptype := profile["type"]; ptype == nil || ptype.(string) != typeName {
							continue
						}

						row := []string{name}
						for _, key := range listKeys {
							row = append(row, profile[strings.Replace(key, "-", "_", -1)].(string))
						}
						table.Append(row)
					}
					table.Render()
				}
			} else {
				fmt.Printf("No profiles configured. Use `%s auth setup` to add one.\n", Root.CommandPath())
			}
		},
	})
	authCommand.AddCommand(newAuthSetupCommand())

	// Install auth middleware
	Client.UseRequest(func(ctx *context.Context, h context.Handler) {
		profile := GetProfile()
		_, handler := resolveAuthHandler(profile)
		if handler == nil {
			h.Error(ctx, fmt.Errorf("no authentication handler configured"))
			return
		}

		if err := handler.OnRequest(ctx.Get("log").(*zerolog.Logger), ctx.Request); err != nil {
			h.Error(ctx, err)
			return
		}

		h.Next(ctx)
	})
}

func resolveAuthHandler(profile map[string]string) (string, AuthHandler) {
	typeName := profile["type"]
	if typeName != "" {
		return typeName, AuthHandlers[typeName]
	}

	if len(AuthHandlers) == 1 {
		for name, handler := range AuthHandlers {
			return name, handler
		}
	}

	return "", nil
}

// GetAuthStatus returns machine-readable auth diagnostics for `doctor`.
func GetAuthStatus() map[string]interface{} {
	status := map[string]interface{}{
		"required": len(AuthHandlers) > 0,
	}

	if len(AuthHandlers) == 0 {
		status["configured"] = true
		status["source"] = "none"
		return status
	}

	types := make([]string, 0, len(AuthHandlers))
	for name := range AuthHandlers {
		if name == "" {
			continue
		}
		types = append(types, name)
	}
	sort.Strings(types)
	if len(types) > 0 {
		status["available_types"] = types
	}

	profile := GetProfile()
	status["profile"] = viper.GetString("profile")

	typeName, handler := resolveAuthHandler(profile)
	if typeName != "" {
		status["type"] = typeName
	}

	if handler == nil {
		status["configured"] = false
		status["source"] = "missing"
		status["message"] = "configure a profile with `auth setup`"
		return status
	}

	if provider, ok := handler.(AuthStatusHandler); ok {
		for key, value := range provider.AuthStatus(profile) {
			status[key] = value
		}
		return status
	}

	status["configured"] = len(profile) > 0
	if len(profile) > 0 {
		status["source"] = "profile"
	} else {
		status["source"] = "missing"
	}

	return status
}

// UseAuth registers a new auth handler for a given type name. For backward-
// compatibility, the auth type name can be a blank string. It is recommended
// to always pass a value for the type name.
func UseAuth(typeName string, handler AuthHandler) {
	// Initialize auth system if it isn't already set up.
	initAuth()

	// Register the handler by its type.
	AuthHandlers[typeName] = handler

	// Set up the add-profile command.
	keys := handler.ProfileKeys()

	// Key values are OPTIONAL: a secret passed on the command line leaks into shell
	// history and `ps`, so the default is to prompt for it.
	use := " [flags] <name>"
	for _, name := range keys {
		use += " [<" + strings.Replace(name, "_", "-", -1) + ">]"
	}

	run := func(cmd *cobra.Command, args []string) {
		name := strings.Replace(args[0], ".", "-", -1)
		Creds.Set("profiles."+name+".type", typeName)

		for i, key := range keys {
			label := strings.Replace(key, "_", "-", -1)
			value, ok := resolveProfileValue(key, label, args, i)
			if !ok {
				exitFunc(ExitUsage)
				return
			}
			// Replace periods in the name since Viper will create nested structures
			// in the config and this isn't what we want!
			Creds.Set("profiles."+name+"."+strings.Replace(key, "-", "_", -1), value)
		}

		filename := path.Join(viper.GetString("config-directory"), "credentials.json")
		if err := writeCredentials(filename); err != nil {
			panic(err)
		}
	}

	if typeName == "" {
		// Backward-compatibility use-case without an explicit type. Set up the
		// `add-profile` command as the only way to authenticate.
		if authAddCommand.Run != nil {
			// This fallback code path was already used, so we must be registering
			// a *second* anonymous auth type, which is not allowed.
			panic("register auth type names to use multi-auth")
		}

		authAddCommand.Use = "add-profile" + use
		authAddCommand.Short = "Add a new named authentication profile"
		authAddCommand.Args = cobra.RangeArgs(1, 1+len(keys))
		authAddCommand.Run = run
	} else {
		// Add a new type-specific `add-profile` subcommand.
		authAddCommand.AddCommand(&cobra.Command{
			Use:   typeName + use,
			Short: "Add a new named " + typeName + " authentication profile",
			Args:  cobra.RangeArgs(1, 1+len(keys)),
			Run:   run,
		})
	}
}

func newAuthSetupCommand() *cobra.Command {
	var profileName string
	var typeName string

	cmd := &cobra.Command{
		Use:     "setup",
		Aliases: []string{"login"},
		Short:   "Interactively configure authentication",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return RunAuthSetup(profileName, typeName)
		},
	}

	cmd.Flags().StringVar(&profileName, "profile", "default", "Profile name to create or update")
	cmd.Flags().StringVar(&typeName, "type", "", "Authentication type to configure when multiple handlers exist")
	return cmd
}

// RunAuthSetup interactively prompts for authentication details and persists
// them to the credentials profile store.
func RunAuthSetup(profileName string, preferredType string) error {
	if !hasInteractiveInput() {
		return fmt.Errorf("auth setup requires an interactive terminal")
	}

	typeName, handler, err := pickAuthHandler(preferredType)
	if err != nil {
		return err
	}

	profileName = sanitizeProfileName(profileName)
	if profileName == "" {
		profileName = "default"
	}

	answers := make([]string, 0, len(handler.ProfileKeys()))
	for _, key := range handler.ProfileKeys() {
		value, err := promptProfileValue(key)
		if err != nil {
			return err
		}
		answers = append(answers, value)
	}

	return saveAuthProfile(typeName, profileName, handler.ProfileKeys(), answers)
}

func pickAuthHandler(preferredType string) (string, AuthHandler, error) {
	if len(AuthHandlers) == 0 {
		return "", nil, fmt.Errorf("no authentication handler is configured for this CLI")
	}

	if preferredType != "" {
		handler := AuthHandlers[preferredType]
		if handler == nil {
			return "", nil, fmt.Errorf("unknown auth type %q", preferredType)
		}
		return preferredType, handler, nil
	}

	if len(AuthHandlers) == 1 {
		for name, handler := range AuthHandlers {
			return name, handler, nil
		}
	}

	names := make([]string, 0, len(AuthHandlers))
	for name := range AuthHandlers {
		display := name
		if display == "" {
			display = "default"
		}
		names = append(names, display)
	}
	sort.Strings(names)

	selected := names[0]
	if err := survey.AskOne(&survey.Select{
		Message: "Auth type:",
		Options: names,
		Default: selected,
	}, &selected); err != nil {
		return "", nil, err
	}

	resolved := selected
	if resolved == "default" {
		resolved = ""
	}

	return resolved, AuthHandlers[resolved], nil
}

// resolveProfileValue returns one profile key's value: the positional argument if given,
// else a prompt. ok is false when there is neither, and the caller exits ExitUsage.
func resolveProfileValue(key, label string, args []string, i int) (value string, ok bool) {
	if i+1 < len(args) {
		// Backward-compat: positional value (discouraged for secrets).
		return args[i+1], true
	}
	v, err := promptProfileValue(key)
	if err != nil {
		// No positional value and no usable TTY: a missing argument is a usage error.
		fmt.Fprintf(os.Stderr, "no %s provided; pass it as an argument or run in an interactive terminal\n", label)
		return "", false
	}
	return v, true
}

// promptProfileValue is a var so tests can replace the prompt, like isInteractive below.
var promptProfileValue = func(key string) (string, error) {
	message := strings.ReplaceAll(key, "_", " ")
	message = strings.Title(message)
	if strings.Contains(message, "Api ") {
		message = strings.ReplaceAll(message, "Api ", "API ")
	}

	prompt := &survey.Input{Message: message + ":"}
	if looksSensitiveKey(key) {
		password := ""
		if err := survey.AskOne(&survey.Password{Message: message + ":"}, &password); err != nil {
			return "", err
		}
		return password, nil
	}

	value := ""
	if err := survey.AskOne(prompt, &value); err != nil {
		return "", err
	}
	return value, nil
}

func looksSensitiveKey(key string) bool {
	lower := strings.ToLower(key)
	return strings.Contains(lower, "key") || strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "password")
}

func sanitizeProfileName(value string) string {
	return strings.Replace(strings.TrimSpace(value), ".", "-", -1)
}

func saveAuthProfile(typeName string, profileName string, keys []string, values []string) error {
	if len(keys) != len(values) {
		return fmt.Errorf("profile values do not match keys")
	}

	Creds.Set("profiles."+profileName+".type", typeName)
	for i, key := range keys {
		Creds.Set("profiles."+profileName+"."+strings.Replace(key, "-", "_", -1), values[i])
	}

	filename := path.Join(viper.GetString("config-directory"), "credentials.json")
	return writeCredentials(filename)
}

func hasInteractiveInput() bool {
	// term.IsTerminal, not os.ModeCharDevice: /dev/null is a character device, so
	// ModeCharDevice read as interactive under docker without -i, systemd and cron.
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// isInteractive and askConfirm are vars so ConfirmDestructive is testable without a TTY.
var isInteractive = hasInteractiveInput

var askConfirm = func(action string) (bool, error) {
	proceed := false
	err := survey.AskOne(&survey.Confirm{
		Message: fmt.Sprintf("This will run %q and cannot be undone. Continue?", action),
		Default: false,
	}, &proceed)
	return proceed, err
}

// exitFunc is a var so the refusal exit can be tested without ending the test process.
var exitFunc = os.Exit

// ConfirmDestructive gates a destructive command behind a confirmation: --force
// proceeds, a non-interactive shell without it refuses and exits ExitUsage so a script
// cannot read a skipped delete as a success, otherwise it prompts and defaults to No.
func ConfirmDestructive(cmd *cobra.Command, action string) bool {
	if force, err := cmd.Flags().GetBool("force"); err == nil && force {
		return true
	}
	if !isInteractive() {
		fmt.Fprintf(os.Stderr, "Refusing to run %q without --force in a non-interactive shell.\n", action)
		exitFunc(ExitUsage)
		return false // unreachable: exitFunc does not return
	}
	proceed, err := askConfirm(action)
	if err != nil {
		return false
	}
	return proceed
}

// CredentialsFile holds credential-related information.
type CredentialsFile struct {
	*viper.Viper
	keys     []string
	listKeys []string
}

// Creds represents a configuration file storing credential-related information.
// Use this only after `InitCredentials` has been called.
var Creds *CredentialsFile

// GetProfile returns the current profile's configuration.
func GetProfile() map[string]string {
	return Creds.GetStringMapString("profiles." + strings.Replace(viper.GetString("profile"), ".", "-", -1))
}

// ProfileKeys lets you specify authentication profile keys to be used in
// the credentials file.
// This is deprecated and you should use `cli.UseAuth` instead.
func ProfileKeys(keys ...string) func(*CredentialsFile) error {
	return func(cf *CredentialsFile) error {
		cf.keys = keys
		return nil
	}
}

// ProfileListKeys sets which keys will be shown in the table when calling
// the `auth list-profiles` command.
// This is deprecated and you should use `cli.UseAuth` instead.
func ProfileListKeys(keys ...string) func(*CredentialsFile) error {
	return func(cf *CredentialsFile) error {
		cf.listKeys = keys
		return nil
	}
}

// InitCredentialsFile sets up the creds file and `profile` global parameter.
func InitCredentialsFile() {
	// Setup a credentials file, kept separate from configuration which might
	// get checked into source control.
	Creds = &CredentialsFile{viper.New(), []string{}, []string{}}

	Creds.SetConfigName("credentials")
	Creds.AddConfigPath("$HOME/." + viper.GetString("app-name") + "/")
	Creds.ReadInConfig()

	// Register a new `--profile` flag.
	AddGlobalFlag("profile", "", "Credentials profile to use for authentication", "default")
}

// InitCredentials sets up the profile/auth commands. Must be called *after* you
// have called `cli.Init()`.
//
//	// Initialize an API key
//	cli.InitCredentials(cli.ProfileKeys("api-key"))
//
// This is deprecated and you should use `cli.UseAuth` instead.
func InitCredentials(options ...func(*CredentialsFile) error) {
	InitCredentialsFile()

	for _, option := range options {
		option(Creds)
	}

	// Register auth management commands to create and list profiles.
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Authentication settings",
	}
	Root.AddCommand(cmd)

	use := "add-profile [flags] <name>"
	for _, name := range Creds.keys {
		use += " [<" + strings.Replace(name, "_", "-", -1) + ">]"
	}

	cmd.AddCommand(&cobra.Command{
		Use:     use,
		Aliases: []string{"add"},
		Short:   "Add a new named authentication profile",
		Args:    cobra.RangeArgs(1, 1+len(Creds.keys)),
		Run: func(cmd *cobra.Command, args []string) {
			for i, key := range Creds.keys {
				// Replace periods in the name since Viper will create nested structures
				// in the config and this isn't what we want!
				name := strings.Replace(args[0], ".", "-", -1)
				label := strings.Replace(key, "_", "-", -1)
				value, ok := resolveProfileValue(key, label, args, i)
				if !ok {
					exitFunc(ExitUsage)
					return
				}
				Creds.Set("profiles."+name+"."+strings.Replace(key, "-", "_", -1), value)
			}

			filename := path.Join(viper.GetString("config-directory"), "credentials.json")
			if err := writeCredentials(filename); err != nil {
				panic(err)
			}
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:     "list-profiles",
		Aliases: []string{"ls"},
		Short:   "List available configured authentication profiles",
		Args:    cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			profiles := Creds.GetStringMap("profiles")
			if profiles != nil {
				table := tablewriter.NewTable(os.Stdout, tablewriter.WithHeaderAutoFormat(tw.Off))
				table.Header(append([]string{"Profile Name"}, Creds.listKeys...))

				for name, profile := range profiles {
					row := []string{name}
					for _, key := range Creds.listKeys {
						row = append(row, profile.(map[string]interface{})[strings.Replace(key, "-", "_", -1)].(string))
					}
					table.Append(row)
				}
				table.Render()
			} else {
				fmt.Printf("No profiles configured. Use `%s auth setup` to add one.\n", Root.CommandPath())
			}
		},
	})
}
