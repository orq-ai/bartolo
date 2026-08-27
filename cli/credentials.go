package cli

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	survey "github.com/AlecAivazis/survey/v2"
	isatty "github.com/mattn/go-isatty"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"gopkg.in/h2non/gentleman.v2/context"
)

// Save persists the credentials file atomically at 0600. os.CreateTemp
// creates the temp file at 0600 from birth (no world-readable window), then viper
// writes into it, then rename replaces the target. An interrupted write leaves the
// existing file intact.
//
// The whole in-memory tree is written, so Save replaces the target rather than
// merging into it. Build through NewCredentialsFile, which loads what is
// already on disk; saving an instance that never read it drops every profile
// the caller did not set.
func (c *CredentialsFile) Save(filename string) error {
	if c == nil || c.viper == nil {
		return fmt.Errorf("credentials file is not initialized")
	}
	dir := filepath.Dir(filename)
	ext := filepath.Ext(filename)
	base := strings.TrimSuffix(filepath.Base(filename), ext)
	f, err := os.CreateTemp(dir, base+"-*"+ext)
	if err != nil {
		return err
	}
	tmp := f.Name()
	f.Close()
	if err := c.viper.WriteConfigAs(tmp); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, filename); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
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
		RunE: func(cmd *cobra.Command, args []string) error {
			profiles := Creds.GetStringMap("profiles")
			if len(profiles) == 0 {
				fmt.Printf("No profiles configured. Use `%s auth setup` to add one.\n", Root.CommandPath())
				return nil
			}

			listed := make([]map[string]interface{}, 0, len(profiles))
			for _, name := range sortedKeys(profiles) {
				profile, ok := profiles[name].(map[string]interface{})
				if !ok {
					continue
				}

				typeName, _ := profile["type"].(string)
				entry := map[string]interface{}{"name": name, "type": typeName}

				keys := []string{"server"}
				if handler := AuthHandlers[typeName]; handler != nil {
					keys = append(keys, handler.ProfileKeys()...)
				} else {
					keys = append(keys, sortedKeys(profile)...)
				}

				for _, key := range keys {
					field := strings.Replace(key, "-", "_", -1)
					if field == "type" {
						continue
					}
					if value, ok := profile[field]; ok {
						entry[field] = maskIfSecret(field, value)
					}
				}
				listed = append(listed, entry)
			}

			return Formatter.Format(map[string]interface{}{"profiles": listed})
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

// sortedKeys returns a map's keys in a stable order.
func sortedKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// maskIfSecret keeps a credential out of the output. Whether a field counts as
// a credential is decided by looksSensitiveKey, the same predicate that decides
// whether `auth setup` prompts for it without echo.
func maskIfSecret(name string, value interface{}) interface{} {
	if !looksSensitiveKey(name) {
		return value
	}

	s, ok := value.(string)
	if !ok || s == "" {
		return value
	}

	runes := []rune(s)
	// The middle is a fixed width rather than the real one, so the output does
	// not disclose the secret's length either.
	if len(runes) < 12 {
		return "********"
	}
	return string(runes[:4]) + "********" + string(runes[len(runes)-4:])
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

	run := func(cmd *cobra.Command, args []string) error {
		name := strings.Replace(args[0], ".", "-", -1)
		Creds.Set("profiles."+name+".type", typeName)

		for i, key := range keys {
			value, err := resolveProfileValue(cmd, key, args, i)
			if err != nil {
				return err
			}
			Creds.Set("profiles."+name+"."+strings.Replace(key, "-", "_", -1), value)
		}

		if server := explicitServer(cmd); server != "" {
			Creds.Set("profiles."+name+".server", server)
		}

		filename := filepath.Join(viper.GetString("config-directory"), "credentials.json")
		return Creds.Save(filename)
	}

	addKeyFileFlags := func(cmd *cobra.Command) {
		for _, key := range keys {
			label := strings.Replace(key, "_", "-", -1)
			cmd.Flags().String(label+"-file", "", fmt.Sprintf("Read %s from a file (use - for stdin)", label))
		}
	}

	if typeName == "" {
		// Backward-compatibility use-case without an explicit type. Set up the
		// `add-profile` command as the only way to authenticate.
		if authAddCommand.RunE != nil {
			// This fallback code path was already used, so we must be registering
			// a *second* anonymous auth type, which is not allowed.
			panic("register auth type names to use multi-auth")
		}

		authAddCommand.Use = "add-profile" + use
		authAddCommand.Short = "Add a new named authentication profile"
		authAddCommand.Args = cobra.RangeArgs(1, 1+len(keys))
		authAddCommand.RunE = run
		addKeyFileFlags(authAddCommand)
	} else {
		// Add a new type-specific `add-profile` subcommand.
		cmd := &cobra.Command{
			Use:   typeName + use,
			Short: "Add a new named " + typeName + " authentication profile",
			Args:  cobra.RangeArgs(1, 1+len(keys)),
			RunE:  run,
		}
		addKeyFileFlags(cmd)
		authAddCommand.AddCommand(cmd)
	}
}

// explicitServer returns `--server` only when passed on this invocation, so an
// env var or persisted default is never baked into a profile.
func explicitServer(cmd *cobra.Command) string {
	flag := cmd.Flag("server")
	if flag == nil || !flag.Changed {
		return ""
	}

	return strings.TrimSpace(flag.Value.String())
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
			return RunAuthSetup(profileName, typeName, explicitServer(cmd))
		},
	}

	cmd.Flags().StringVar(&profileName, "profile", "default", "Profile name to create or update")
	cmd.Flags().StringVar(&typeName, "type", "", "Authentication type to configure when multiple handlers exist")
	return cmd
}

// RunAuthSetup interactively prompts for authentication details and persists
// them to the credentials profile store.
func RunAuthSetup(profileName string, preferredType string, server string) error {
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

	return saveAuthProfile(typeName, profileName, handler.ProfileKeys(), answers, server)
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
	}, &selected, surveyStderr()); err != nil {
		return "", nil, err
	}

	resolved := selected
	if resolved == "default" {
		resolved = ""
	}

	return resolved, AuthHandlers[resolved], nil
}

// resolveProfileValue returns one profile key's value, tried in order:
//  1. --<label>-file <path> (pass "-" for stdin)
//  2. positional argument (warns on stderr for secrets)
//  3. interactive prompt
func resolveProfileValue(cmd *cobra.Command, key string, args []string, i int) (string, error) {
	label := strings.Replace(key, "_", "-", -1)

	// 1. --<label>-file flag
	flagName := label + "-file"
	if path, _ := cmd.Flags().GetString(flagName); path != "" {
		return readKeyFile(path)
	}

	// 2. positional
	if i+1 < len(args) {
		if looksSensitiveKey(key) {
			fmt.Fprintf(Stderr, "warning: %s passed on the command line; prefer --%s-file or the interactive prompt to keep it out of shell history\n", label, label)
		}
		return args[i+1], nil
	}

	// 3. prompt
	if !isInteractive() {
		return "", NewUsageError(fmt.Errorf("no %s provided; use --%s-file <path> or run in an interactive terminal", label, label))
	}
	v, err := promptProfileValue(key)
	if err != nil {
		return "", fmt.Errorf("could not read %s: %w", label, err)
	}
	return v, nil
}

func readKeyFile(path string) (string, error) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return "", fmt.Errorf("reading key file: %w", err)
	}
	v := strings.TrimSpace(string(data))
	if v == "" {
		return "", fmt.Errorf("key file %q is empty", path)
	}
	return v, nil
}

// promptProfileValue is a var so tests can replace the prompt.
var promptProfileValue = func(key string) (string, error) {
	message := strings.ReplaceAll(key, "_", " ")
	message = strings.Title(message)
	if strings.Contains(message, "Api ") {
		message = strings.ReplaceAll(message, "Api ", "API ")
	}

	opts := []survey.AskOpt{surveyStderr(), survey.WithValidator(survey.Required)}

	if looksSensitiveKey(key) {
		password := ""
		if err := survey.AskOne(&survey.Password{Message: message + ":"}, &password, opts...); err != nil {
			return "", err
		}
		return password, nil
	}

	value := ""
	if err := survey.AskOne(&survey.Input{Message: message + ":"}, &value, opts...); err != nil {
		return "", err
	}
	return value, nil
}

// looksSensitiveKey reports whether a field name holds a credential. It is the one
// definition of "secret" in the package: it decides whether add-profile prompts without
// echo, whether `auth list-profiles` masks the stored value, and (through
// looksSensitiveHeader) whether `--verbose` redacts a header or query parameter.
// Substring matching over-redacts by design — a benign field is worth masking to keep a
// key out of a log.
func looksSensitiveKey(key string) bool {
	lower := strings.ToLower(key)
	for _, hint := range []string{"key", "token", "secret", "password", "passphrase", "credential", "signature"} {
		if strings.Contains(lower, hint) {
			return true
		}
	}
	return false
}

func sanitizeProfileName(value string) string {
	return strings.Replace(strings.TrimSpace(value), ".", "-", -1)
}

func saveAuthProfile(typeName string, profileName string, keys []string, values []string, server string) error {
	if len(keys) != len(values) {
		return fmt.Errorf("profile values do not match keys")
	}

	Creds.Set("profiles."+profileName+".type", typeName)
	for i, key := range keys {
		Creds.Set("profiles."+profileName+"."+strings.Replace(key, "-", "_", -1), values[i])
	}

	if server = strings.TrimSpace(server); server != "" {
		Creds.Set("profiles."+profileName+".server", server)
	}

	filename := filepath.Join(viper.GetString("config-directory"), "credentials.json")
	return Creds.Save(filename)
}

func hasInteractiveInput() bool {
	return isatty.IsTerminal(os.Stdin.Fd()) || isatty.IsCygwinTerminal(os.Stdin.Fd())
}

// surveyStderr routes survey prompts to stderr so they never corrupt piped stdout.
func surveyStderr() survey.AskOpt {
	return survey.WithStdio(os.Stdin, os.Stderr, os.Stderr)
}

// isInteractive is a var so ConfirmDestructive is testable without a TTY.
var isInteractive = hasInteractiveInput

var askConfirm = func(action string) (bool, error) {
	proceed := false
	err := survey.AskOne(&survey.Confirm{
		Message: fmt.Sprintf("This will run %q and cannot be undone. Continue?", action),
		Default: false,
	}, &proceed, surveyStderr())
	return proceed, err
}

// ErrDestructiveRefused is returned when the user declines a destructive confirmation.
var ErrDestructiveRefused = fmt.Errorf("operation cancelled")

// ConfirmDestructive gates a destructive command behind a confirmation. Returns nil
// to proceed, a *UsageError when a non-interactive shell lacks --force, or an error
// when the prompt fails or the user declines. Every refusal flows through
// ExecuteContext/ExitCodeFor, so no failure path can report exit 0.
func ConfirmDestructive(cmd *cobra.Command, args []string) error {
	if force, err := cmd.Flags().GetBool("force"); err == nil && force {
		return nil
	}

	action := strings.TrimSpace(cmd.CommandPath() + " " + strings.Join(args, " "))

	if !isInteractive() {
		return NewUsageError(fmt.Errorf("refusing to run %q without --force in a non-interactive shell", action))
	}
	proceed, err := askConfirm(action)
	if err != nil {
		return fmt.Errorf("could not read a confirmation for %q: %w", action, err)
	}
	if !proceed {
		return ErrDestructiveRefused
	}
	return nil
}

// CredentialsFile holds credential-related information. The viper instance is
// unexported so callers cannot reach viper's own WriteConfigAs, which creates
// the file 0644; Save is the only write.
type CredentialsFile struct {
	viper *viper.Viper
}

func (c *CredentialsFile) Set(key string, value interface{}) { c.viper.Set(key, value) }
func (c *CredentialsFile) GetString(key string) string       { return c.viper.GetString(key) }
func (c *CredentialsFile) GetStringMap(key string) map[string]interface{} {
	return c.viper.GetStringMap(key)
}
func (c *CredentialsFile) GetStringMapString(key string) map[string]string {
	return c.viper.GetStringMapString(key)
}
func (c *CredentialsFile) SetConfigName(name string) { c.viper.SetConfigName(name) }
func (c *CredentialsFile) AddConfigPath(path string) { c.viper.AddConfigPath(path) }
func (c *CredentialsFile) ReadInConfig() error       { return c.viper.ReadInConfig() }

// NewCredentialsFile opens the credentials file in dir, loading whatever is
// already stored there so that a later Save does not drop the profiles this
// caller never set. A missing file is not an error; the first Save creates it.
//
// dir is taken rather than defaulted because a caller that does not own the
// process-wide file (a test, or a CLI with its own config root) must be able to
// say where it writes. InitCredentialsFile remains the process-wide setup.
func NewCredentialsFile(dir string) (*CredentialsFile, error) {
	c := &CredentialsFile{viper: viper.New()}
	c.SetConfigName("credentials")
	c.AddConfigPath(dir)
	var notFound viper.ConfigFileNotFoundError
	if err := c.ReadInConfig(); err != nil && !errors.As(err, &notFound) {
		return nil, err
	}
	return c, nil
}

// Creds represents a configuration file storing credential-related
// information. Use this only after `InitCredentialsFile` has been called.
var Creds *CredentialsFile

// GetProfile returns the current profile's configuration.
func GetProfile() map[string]string {
	return Creds.GetStringMapString("profiles." + strings.Replace(viper.GetString("profile"), ".", "-", -1))
}

// InitCredentialsFile sets up the creds file and `profile` global parameter.
func InitCredentialsFile() {
	// Setup a credentials file, kept separate from configuration which might
	// get checked into source control.
	Creds = &CredentialsFile{viper: viper.New()}

	Creds.SetConfigName("credentials")
	Creds.AddConfigPath("$HOME/." + viper.GetString("app-name") + "/")
	Creds.ReadInConfig()

	// Register a new `--profile` flag.
	AddGlobalFlag("profile", "", "Credentials profile to use for authentication", "default")
}
