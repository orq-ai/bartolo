package cli

import (
	"crypto/sha256"
	"encoding/hex"
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

// Save atomically replaces filename with the whole in-memory tree, so an
// instance built outside NewCredentialsFile drops every profile it never read.
func (c *CredentialsFile) Save(filename string) error {
	if c == nil || c.viper == nil {
		return fmt.Errorf("credentials file is not initialized")
	}
	return writeFileAtomic(filename, func(tmp string) error {
		return c.viper.WriteConfigAs(tmp)
	})
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

// RequiredKeysHandler narrows which of a handler's profile keys must be
// present for a profile to be saveable. A handler that does not implement
// this keeps every key ProfileKeys declares required.
type RequiredKeysHandler interface {
	RequiredProfileKeys() []string
}

// AuthHandlers is the map of registered auth type names to handlers
var AuthHandlers = make(map[string]AuthHandler)

var authInitialized bool
var authCommand *cobra.Command
var profileCommand *cobra.Command

// authAddCommands holds every registered spelling of the add-profile command,
// which `UseAuth` fills in (or hangs a typed subcommand off, for a named auth
// type).
var authAddCommands []*cobra.Command

// authAddDeprecationNotices marks which of authAddCommands is deprecated, so
// UseAuth can copy the notice onto each typed child it hangs off them.
var authAddDeprecationNotices = map[*cobra.Command]string{}

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

	profileCommand = &cobra.Command{
		Use:     "profile",
		Aliases: []string{"profiles"},
		Short:   "Manage authentication profiles",
	}
	authCommand.AddCommand(profileCommand)

	authAddCommands = []*cobra.Command{
		newAuthAddCommand(profileCommand, "add", ""),
		newAuthAddCommand(authCommand, "add-profile", "use `auth profile add` instead"),
	}
	// `add-profile` shipped on origin/main with an `add` alias
	// (`auth add-profile`/`auth add`); keep it working for the deprecated
	// spelling.
	authAddCommands[1].Aliases = []string{"add"}

	profileCommand.AddCommand(newProfileListCommand("list", ""))
	profileCommand.AddCommand(newProfileCurrentCommand())
	profileCommand.AddCommand(newProfileUseCommand("use"))
	profileCommand.AddCommand(newProfileClearCommand())

	authCommand.AddCommand(newProfileListCommand("list-profiles", "use `auth profile list` instead"))

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

// newAuthAddCommand registers one spelling of add-profile. A deprecated one
// prints its own notice to Stderr; cobra's Deprecated field goes through
// OutOrStderr, which lands on stdout and corrupts `--json`.
func newAuthAddCommand(parent *cobra.Command, use string, deprecationNotice string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   use,
		Short: "Add user profile for authentication",
	}
	if deprecationNotice != "" {
		cmd.Hidden = true
		cmd.PreRunE = deprecationPreRunE(deprecationNotice)
		authAddDeprecationNotices[cmd] = deprecationNotice
	}
	parent.AddCommand(cmd)
	return cmd
}

// deprecationPreRunE prints notice, naming whichever command cobra ran. Cobra
// runs only the leaf's hooks, so every typed child needs its own copy.
// deprecationPreRunEIf is deprecationPreRunE for a notice that may be empty,
// since cobra treats a nil PreRunE as "no hook" but calls a non-nil one.
func deprecationPreRunEIf(notice string) func(cmd *cobra.Command, args []string) error {
	if notice == "" {
		return nil
	}

	return deprecationPreRunE(notice)
}

func deprecationPreRunE(notice string) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		fmt.Fprintf(Stderr, "Command %q is deprecated, %s\n", cmd.Name(), notice)
		return nil
	}
}

func newProfileListCommand(use string, deprecationNotice string) *cobra.Command {
	return &cobra.Command{
		Use:     use,
		Aliases: []string{"ls"},
		Short:   "List available configured authentication profiles",
		Args:    cobra.NoArgs,
		Hidden:  deprecationNotice != "",
		PreRunE: deprecationPreRunEIf(deprecationNotice),
		RunE: func(cmd *cobra.Command, args []string) error {
			profiles := Creds.GetStringMap("profiles")
			if len(profiles) == 0 {
				return Formatter.Format(map[string]interface{}{
					"profiles": []map[string]interface{}{},
					"message":  fmt.Sprintf("No profiles configured. Use `%s auth setup` to add one.", Root.CommandPath()),
				})
			}

			active := ActiveProfileName()
			listed := make([]map[string]interface{}, 0, len(profiles))
			for _, name := range sortedKeys(profiles) {
				profile, ok := profiles[name].(map[string]interface{})
				if !ok {
					continue
				}
				listed = append(listed, profileListEntry(name, profile, active))
			}

			// "message" is always present, even when empty, so the object shape
			// does not change between the empty and non-empty cases above.
			return Formatter.Format(map[string]interface{}{"profiles": listed, "message": ""})
		},
	}
}

// profileListEntry renders one stored profile for `auth profile list`. A
// registered handler decides which fields are shown; without one every stored
// field is listed, so a profile saved by a handler that is no longer
// registered still shows what it holds.
func profileListEntry(name string, profile map[string]interface{}, active string) map[string]interface{} {
	typeName, _ := profile["type"].(string)
	entry := map[string]interface{}{"name": name, "type": typeName, "active": name == active}

	keys := []string{"server"}
	if handler := AuthHandlers[typeName]; handler != nil {
		keys = append(keys, handler.ProfileKeys()...)
	} else {
		keys = append(keys, sortedKeys(profile)...)
	}

	for _, key := range keys {
		field := normalizeProfileKeyName(key)
		if field == "type" {
			continue
		}
		if value, ok := profile[field]; ok {
			entry[field] = maskIfSecret(field, value)
		}
	}

	return entry
}

func newProfileUseCommand(use string) *cobra.Command {
	return &cobra.Command{
		Use:     use + " <name>",
		Aliases: []string{"switch"},
		Short:   "Set the active authentication profile",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := sanitizeProfileName(args[0])
			if !ProfileExists(name) {
				return fmt.Errorf("unknown profile %q", args[0])
			}

			if err := persistProfileSelection(name); err != nil {
				return err
			}

			return Formatter.Format(map[string]interface{}{"active_profile": name, "persisted": true})
		},
	}
}

// newProfileCurrentCommand surfaces a profile in force that is otherwise
// invisible: `--profile ghost`, or a selection whose profile was later
// removed, resolves to a name that appears nowhere in `auth profile list`.
func newProfileCurrentCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "current",
		Short: "Show the currently active authentication profile",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			name, source := activeProfileNameWithSource()

			return Formatter.Format(map[string]interface{}{
				"active_profile": name,
				"source":         source,
				"exists":         ProfileExists(name),
				"profile_server": ProfileServer(),
			})
		},
	}
}

func newProfileClearCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "clear",
		Short: "Stop using a persisted active profile",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := persistProfileSelection(""); err != nil {
				return err
			}

			return Formatter.Format(map[string]interface{}{"active_profile": ActiveProfileName(), "persisted": true})
		},
	}
}

// persistProfileSelection is the only writer of `profile-selected`, always
// pairing it with `profile-decided` so adoption can tell "never decided" from
// "deliberately cleared". An empty name removes the key rather than storing "".
func persistProfileSelection(name string) error {
	var selected interface{} = name
	if name == "" {
		selected = nil
	}
	return saveJSONConfig(map[string]interface{}{"profile-selected": selected, "profile-decided": true})
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

	if types := registeredAuthTypes(); len(types) > 0 {
		status["available_types"] = types
	}

	profile := GetProfile()
	status["profile"] = ActiveProfileName()

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
	status["source"] = "missing"
	if len(profile) > 0 {
		status["source"] = "profile"
	}

	return status
}

// registeredAuthTypes lists the named auth types, sorted. The anonymous ""
// type registered by single-auth CLIs has no name to report.
func registeredAuthTypes() []string {
	types := make([]string, 0, len(AuthHandlers))
	for name := range AuthHandlers {
		if name == "" {
			continue
		}
		types = append(types, name)
	}
	sort.Strings(types)

	return types
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
		use += " [<" + profileKeyLabel(name) + ">]"
	}

	run := addProfileRunE(typeName, handler, keys)

	addKeyFileFlags := func(cmd *cobra.Command) {
		for _, key := range keys {
			label := profileKeyLabel(key)
			cmd.Flags().String(label+"-file", "", fmt.Sprintf("Read %s from a file (use - for stdin)", label))
		}
	}

	for _, cmd := range authAddCommands {
		if typeName == "" {
			// Backward-compatibility use-case without an explicit type. Set up the
			// add command as the only way to authenticate.
			if cmd.RunE != nil {
				// This fallback code path was already used, so we must be registering
				// a *second* anonymous auth type, which is not allowed.
				panic("register auth type names to use multi-auth")
			}

			cmd.Use += use
			cmd.Short = "Add a new named authentication profile"
			cmd.Args = cobra.RangeArgs(1, 1+len(keys))
			cmd.RunE = run
			addKeyFileFlags(cmd)
			continue
		}

		typed := &cobra.Command{
			Use:   typeName + use,
			Short: "Add a new named " + typeName + " authentication profile",
			Args:  cobra.RangeArgs(1, 1+len(keys)),
			RunE:  run,
		}
		// Cobra runs only the leaf's hooks, so a typed child of a deprecated
		// spelling has to carry the notice itself.
		if notice, deprecated := authAddDeprecationNotices[cmd]; deprecated {
			typed.PreRunE = deprecationPreRunE(notice)
		}
		addKeyFileFlags(typed)
		cmd.AddCommand(typed)
	}
}

// addProfileRunE builds the RunE shared by every spelling of add-profile:
// resolve each declared key, then save the profile under the sanitized name.
func addProfileRunE(typeName string, handler AuthHandler, keys []string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		values := make([]string, 0, len(keys))
		for i, key := range keys {
			value, err := resolveProfileValue(cmd, key, args, i, isKeyRequired(handler, keys, key))
			if err != nil {
				return err
			}
			values = append(values, value)
		}

		return saveAuthProfile(typeName, sanitizeProfileName(args[0]), keys, values, explicitServer(cmd))
	}
}

// requireProfileValues rejects a profile saved without a required field: a
// profile in force is authoritative, so a missing key must error rather than
// fall back to the environment. See requiredProfileKeys for what counts.
func requireProfileValues(handler AuthHandler, keys []string, values []string) error {
	required := requiredProfileKeys(handler, keys)

	for i, key := range keys {
		if !keyRequired(required, key) {
			continue
		}
		if strings.TrimSpace(values[i]) == "" {
			return fmt.Errorf("%s cannot be empty", strings.Replace(key, "_", " ", -1))
		}
	}

	return nil
}

// normalizeProfileKeyName makes `-` and `_` compare equal: handlers spell the
// same key both ways (`api_key` in apikey, `client-id` in oauth), and a raw
// comparison would make a key required under one spelling optional under the other.
func normalizeProfileKeyName(key string) string {
	return strings.Replace(key, "-", "_", -1)
}

// profileKeyLabel spells a profile key the way the command line does, which is
// the inverse of the stored spelling normalizeProfileKeyName produces.
func profileKeyLabel(key string) string {
	return strings.Replace(key, "_", "-", -1)
}

// keyRequired reports whether key is among required, normalizing both sides.
func keyRequired(required []string, key string) bool {
	needle := normalizeProfileKeyName(key)
	for _, candidate := range required {
		if normalizeProfileKeyName(candidate) == needle {
			return true
		}
	}
	return false
}

// requiredProfileKeys narrows a handler's declared profile keys to those that
// must be non-empty, consulting RequiredKeysHandler when the handler
// implements it and requiring every declared key otherwise.
func requiredProfileKeys(handler AuthHandler, keys []string) []string {
	if narrower, ok := handler.(RequiredKeysHandler); ok {
		return narrower.RequiredProfileKeys()
	}
	return keys
}

// isKeyRequired reports whether a single profile key must be non-empty for
// the given handler, per requiredProfileKeys.
func isKeyRequired(handler AuthHandler, keys []string, key string) bool {
	return keyRequired(requiredProfileKeys(handler, keys), key)
}

// explicitServer returns `--server` only when passed on this invocation, so an
// env var or persisted default is never baked into a profile.
func explicitServer(cmd *cobra.Command) string {
	value, _ := flagValueIfChanged(cmd, "server")
	return value
}

// flagValueIfChanged reports whether a flag was passed on this invocation, as
// opposed to carrying an environment, config or default value (viper cannot
// answer this: it merges all of those into one value), and returns its
// trimmed value when it was.
func flagValueIfChanged(cmd *cobra.Command, name string) (value string, changed bool) {
	if cmd == nil {
		return "", false
	}

	flag := cmd.Flag(name)
	if flag == nil || !flag.Changed {
		return "", false
	}

	return strings.TrimSpace(flag.Value.String()), true
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

	cmd.Flags().StringVar(&profileName, "profile", "", "Profile name to create or update (defaults to the active profile)")
	cmd.Flags().StringVar(&typeName, "type", "", "Authentication type to configure when multiple handlers exist")
	return cmd
}

// RunAuthSetup interactively prompts for authentication details and persists
// them to the credentials profile store. When profileName is empty, it targets
// whichever profile is already in force (ActiveProfileName) so that rotating a
// key never silently writes a different profile than the one every other
// command is using. When no profile is in force either, it prompts for a name
// rather than inventing one: there is no implicit `default` profile anymore.
func RunAuthSetup(profileName string, preferredType string, server string) error {
	if !isInteractive() {
		return fmt.Errorf("auth setup requires an interactive terminal")
	}

	typeName, handler, err := pickAuthHandler(preferredType)
	if err != nil {
		return err
	}

	profileName, err = resolveSetupProfileName(profileName)
	if err != nil {
		return err
	}

	keys := handler.ProfileKeys()
	answers := make([]string, 0, len(keys))
	for _, key := range keys {
		value, err := promptProfileValue(key, isKeyRequired(handler, keys, key))
		if err != nil {
			return err
		}
		answers = append(answers, value)
	}

	return saveAuthProfile(typeName, profileName, keys, answers, server)
}

// resolveSetupProfileName picks the profile `auth setup` writes to: the name
// passed in, else the one in force, else a prompt. It never invents a name —
// writing to an unchosen `default` left the profile in use holding a stale key.
func resolveSetupProfileName(profileName string) (string, error) {
	if name := sanitizeProfileName(profileName); name != "" {
		return name, nil
	}

	if name := ActiveProfileName(); name != "" {
		return name, nil
	}

	answer, err := promptProfileValue("profile_name", true)
	if err != nil {
		return "", err
	}

	name := sanitizeProfileName(answer)
	if name == "" {
		return "", fmt.Errorf("profile name is required")
	}

	return name, nil
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

	name, err := promptAuthType()
	if err != nil {
		return "", nil, err
	}

	return name, AuthHandlers[name], nil
}

// promptAuthType asks which registered auth type to set up. The anonymous ""
// type is offered as "default", since a menu cannot show an empty option.
func promptAuthType() (string, error) {
	names := make([]string, 0, len(AuthHandlers))
	for name := range AuthHandlers {
		if name == "" {
			name = "default"
		}
		names = append(names, name)
	}
	sort.Strings(names)

	selected := names[0]
	if err := survey.AskOne(&survey.Select{
		Message: "Auth type:",
		Options: names,
		Default: selected,
	}, &selected, surveyStderr()); err != nil {
		return "", err
	}

	if selected == "default" {
		return "", nil
	}

	return selected, nil
}

// resolveProfileValue returns one profile key's value, tried in order:
//  1. --<label>-file <path> (pass "-" for stdin)
//  2. positional argument (warns on stderr for secrets)
//  3. interactive prompt
//
// required reports whether key must end up non-empty; it is threaded through
// to the prompt so pressing Enter on an optional field (e.g. a generator-
// supplied region) succeeds instead of being rejected by survey.Required.
func resolveProfileValue(cmd *cobra.Command, key string, args []string, i int, required bool) (string, error) {
	label := profileKeyLabel(key)

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
		// An omitted optional key is unset, as pressing Enter would leave it.
		if !required {
			return "", nil
		}
		return "", NewUsageError(fmt.Errorf("no %s provided; use --%s-file <path> or run in an interactive terminal", label, label))
	}
	v, err := promptProfileValue(key, required)
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

// promptProfileValue is a var so tests can replace the prompt. required
// decides whether the prompt rejects an empty answer: survey.Required must
// only apply to a key the handler in play actually requires, or an optional
// field (e.g. a generator-supplied region) could never be skipped by
// pressing Enter.
var promptProfileValue = func(key string, required bool) (string, error) {
	message := strings.ReplaceAll(key, "_", " ")
	message = strings.Title(message)
	if strings.Contains(message, "Api ") {
		message = strings.ReplaceAll(message, "Api ", "API ")
	}

	opts := []survey.AskOpt{surveyStderr()}
	if required {
		opts = append(opts, survey.WithValidator(survey.Required))
	}

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

// looksSensitiveKey is the package's one definition of "secret", deciding
// echo-less prompts, masked listings and redacted headers alike. Substring
// matching over-redacts by design: a benign field is worth masking to keep a
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

// sanitizeProfileName canonicalizes a profile name. Viper lower-cases config
// keys, so a name that is not lower-cased here matches the stored profile on
// lookup but not on comparison, and periods would nest the profile instead of
// naming it.
func sanitizeProfileName(value string) string {
	return strings.ToLower(strings.Replace(strings.TrimSpace(value), ".", "-", -1))
}

func saveAuthProfile(typeName string, profileName string, keys []string, values []string, server string) error {
	if len(keys) != len(values) {
		return fmt.Errorf("profile values do not match keys")
	}

	handler := AuthHandlers[typeName]
	if err := requireProfileValues(handler, keys, values); err != nil {
		return err
	}

	Creds.Set("profiles."+profileName+".type", typeName)
	setProfileValues(profileName, requiredProfileKeys(handler, keys), keys, values)

	if server = strings.TrimSpace(server); server != "" {
		Creds.Set("profiles."+profileName+".server", server)
	}

	filename := filepath.Join(viper.GetString("config-directory"), "credentials.json")
	if err := Creds.Save(filename); err != nil {
		return err
	}

	// A first profile nobody has chosen yet would otherwise sit unused until the
	// next `auth profile use`.
	if !ProfileSelected() {
		if err := persistProfileSelection(profileName); err != nil {
			return fmt.Errorf("profile %q was saved but could not be selected: %w; run `auth profile use %s` or pass --profile %s", profileName, err, profileName, profileName)
		}
	}

	return nil
}

// setProfileValues stores one profile's fields. An omitted optional field
// (e.g. a generator-supplied region) is left out entirely rather than stored
// as "", so a later read cannot tell it apart from one never supplied.
func setProfileValues(profileName string, required []string, keys []string, values []string) {
	for i, key := range keys {
		if strings.TrimSpace(values[i]) == "" && !keyRequired(required, key) {
			continue
		}
		Creds.Set("profiles."+profileName+"."+normalizeProfileKeyName(key), values[i])
	}
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
func (c *CredentialsFile) IsSet(key string) bool     { return c.viper.IsSet(key) }
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
	name := ActiveProfileName()
	if name == "" {
		return nil
	}

	return Creds.GetStringMapString("profiles." + name)
}

// ProfileExists reports whether a profile of that name is configured. name is
// sanitized, as elsewhere, so a differently-cased spelling still matches.
func ProfileExists(name string) bool {
	name = sanitizeProfileName(name)
	return Creds != nil && name != "" && Creds.IsSet("profiles."+name)
}

// ActiveProfileName ranks the profile sources, most specific first:
// `--profile`, then `<PREFIX>_PROFILE` or viper.Set, then the profile chosen
// with `auth profile use`. That last one is kept off the flag's viper key — as
// `server-default` is kept off `server` — because viper ranks a persisted
// override above a bound flag.
//
// It returns "" when no profile has been chosen. There is no fallback profile
// name: a CLI with credentials only in the environment has no profile in force,
// and `--profile ""` puts it back in that state for one command.
func ActiveProfileName() string {
	name, _ := activeProfileNameWithSource()
	return name
}

// activeProfileNameWithSource is the one precedence chain behind
// ActiveProfileName, reported as-is by `auth profile current` so no second
// copy can drift. source is "flag", "env", "selected" or "none".
func activeProfileNameWithSource() (name string, source string) {
	if value, changed := flagValueIfChanged(Root, "profile"); changed {
		return sanitizeProfileName(value), "flag"
	}

	if name := sanitizeProfileName(viper.GetString("profile")); name != "" {
		return name, "env"
	}

	if name := sanitizeProfileName(viper.GetString("profile-selected")); name != "" {
		return name, "selected"
	}

	return "", "none"
}

// SelectProfile puts a profile in force for this process — the supported way
// for an embedder to switch profiles without the flag, which still beats it.
func SelectProfile(name string) {
	viper.Set("profile", sanitizeProfileName(name))
}

// ProfileSelected reports whether a profile is in force.
func ProfileSelected() bool {
	return ActiveProfileName() != ""
}

// CredentialScope returns a stable, non-empty namespace to key per-credential
// caches (e.g. OAuth tokens) on. A profile name is the whole identity, since
// the profile holds the credential and records its auth type.
//
// An environment credential has no name, so callers pass whatever tells theirs
// apart — token endpoint, client id, scopes — to be hashed with the resolved
// server. Passing nothing keys on the server alone.
func CredentialScope(identity ...string) string {
	if name := ActiveProfileName(); name != "" {
		return name
	}

	sum := sha256.Sum256([]byte(strings.Join(append([]string{ResolveServer()}, identity...), "\x00")))
	return "env-" + hex.EncodeToString(sum[:])[:12]
}

// InitCredentialsFile sets up the creds file and `profile` global parameter.
func InitCredentialsFile() {
	// Setup a credentials file, kept separate from configuration which might
	// get checked into source control.
	Creds = &CredentialsFile{viper: viper.New()}

	Creds.SetConfigName("credentials")
	Creds.AddConfigPath("$HOME/." + viper.GetString("app-name") + "/")
	Creds.ReadInConfig()

	// Register a new `--profile` flag. It defaults to empty: no profile is in
	// force until one is named or chosen with `auth profile use`.
	AddGlobalFlag("profile", "", "Credentials profile to use for authentication", "")

	adoptLegacyDefaultProfile()
}

// adoptLegacyDefaultProfile records a profile named `default` as the chosen
// one on first run, so an install that relied on the old implicit resolution
// does not come up with no profile at all.
//
// It guards on `profile-decided`, not on `profile-selected` being empty:
// `auth profile clear` empties the latter too, so without the marker adoption
// would re-run and undo the user's own clear.
//
// Adoption also reverses which credential goes on the wire — `default` becomes
// authoritative and the environment key is no longer consulted — hence the
// one-time notice.
func adoptLegacyDefaultProfile() {
	if viper.GetBool("profile-decided") {
		return
	}

	// The `profile-selected` half is reachable only through a hand-edited
	// config.json; every writer pairs it with `profile-decided`.
	if !Creds.IsSet("profiles.default") || viper.GetString("profile-selected") != "" {
		return
	}

	if err := persistProfileSelection("default"); err != nil {
		// A failed disk write can still have set the selection in memory, so
		// the reversal warning has to reach the user in that case too.
		if ActiveProfileName() == "default" {
			fmt.Fprintf(Stderr, "warning: could not record profile adoption: %v; %q is nonetheless the active authentication profile for this run, and the environment API key is not consulted while a profile is in force.\n", err, "default")
		} else {
			fmt.Fprintf(Stderr, "warning: could not record profile adoption: %v\n", err)
		}
		return
	}

	fmt.Fprintf(Stderr, "Profile %q is the active authentication profile. The environment API key is not consulted while a profile is in force; run `auth profile clear` to opt out.\n", "default")
}
