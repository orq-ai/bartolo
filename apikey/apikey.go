// Package apikey provides authentication profile support for APIs that require
// a pre-generated constant authenticationn key passed via a header, query
// parameter, or cookie value in each request.
package apikey

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/orq-ai/bartolo/cli"
	"github.com/rs/zerolog"
	"github.com/spf13/viper"
)

// Location defines how a parameter is sent.
type Location int

// API key parameter locations, which correspond to the OpenAPI `in` parameter
// values for the `apikey` security type.
const (
	LocationHeader Location = iota
	LocationQuery
	LocationCookie
)

const apiKey = "api_key"

// Handler sets up the API key authentication flow.
type Handler struct {
	Name    string
	In      Location
	Keys    []string
	EnvVars []string
	Prefix  string
}

// ProfileKeys returns the key names for fields to store in the profile.
func (h *Handler) ProfileKeys() []string {
	return append([]string{apiKey}, h.Keys...)
}

// RequiredProfileKeys narrows the fields a profile must supply to just the
// credential; generator-supplied extras in h.Keys (e.g. a region or an
// organisation id) are optional.
func (h *Handler) RequiredProfileKeys() []string {
	return []string{apiKey}
}

// OnRequest gets run before the request goes out on the wire.
func (h *Handler) OnRequest(log *zerolog.Logger, request *http.Request) error {
	profile := cli.GetProfile()
	activeProfile := cli.ActiveProfileName()
	key, source := h.lookupKey(profile, activeProfile)
	if key == "" {
		return h.missingKeyError(activeProfile, source)
	}

	log.Debug().Str("auth-source", source).Msg("Using API key authentication")

	switch h.In {
	case LocationHeader:
		if request.Header.Get(h.Name) == "" {
			request.Header.Add(h.Name, key)
		}
	case LocationQuery:
		if request.URL.Query().Get(h.Name) == "" {
			query := request.URL.Query()
			query.Set(h.Name, key)
			request.URL.RawQuery = query.Encode()
		}
	case LocationCookie:
		if c, err := request.Cookie(h.Name); err != nil || c == nil {
			request.AddCookie(&http.Cookie{
				Name:  h.Name,
				Value: key,
			})
		}
	}

	return nil
}

// AuthStatus describes whether auth is configured for `doctor`.
func (h *Handler) AuthStatus(profile map[string]string) map[string]interface{} {
	key, source := h.lookupKey(profile, cli.ActiveProfileName())
	status := map[string]interface{}{
		"configured": key != "",
		"source":     source,
	}

	if len(h.EnvVars) > 0 {
		status["env_vars"] = h.EnvVars
	}

	return status
}

func (h *Handler) lookupKey(profile map[string]string, activeProfile string) (string, string) {
	if key := strings.TrimSpace(profile[apiKey]); key != "" {
		return h.applyPrefix(key), "profile"
	}

	// A chosen profile is authoritative. Falling back to an ambient key here is
	// how `--profile staging` ends up sending the production key it was passed
	// to avoid, so an incomplete or unknown profile is an error instead.
	if activeProfile != "" {
		if cli.ProfileExists(activeProfile) {
			return "", "profile-incomplete"
		}
		return "", "profile-unknown"
	}

	for _, envVar := range h.EnvVars {
		if value := strings.TrimSpace(os.Getenv(envVar)); value != "" {
			return h.applyPrefix(value), "env"
		}
	}

	return "", "missing"
}

func (h *Handler) missingKeyError(name string, source string) error {
	switch source {
	case "profile-unknown":
		return fmt.Errorf("profile %q is not configured; run `auth setup --profile %s` or pick one with `auth profile list`", name, name)
	case "profile-incomplete":
		return fmt.Errorf("profile %q has no API key; run `auth setup --profile %s`", name, name)
	}

	remedies := []string{"configure a profile with `auth setup`"}
	if len(h.EnvVars) > 0 {
		remedies = append(remedies, "set one of "+strings.Join(h.EnvVars, ", "))
	}
	return fmt.Errorf("missing API key; %s", strings.Join(remedies, " or "))
}

func (h *Handler) applyPrefix(value string) string {
	if h.Prefix == "" || strings.HasPrefix(value, h.Prefix) {
		return value
	}

	return h.Prefix + value
}

func defaultEnvVars(name string, prefix string) []string {
	envPrefix := strings.TrimSpace(viper.GetString("env-prefix"))
	if envPrefix == "" {
		return nil
	}

	envVars := make([]string, 0, 4)
	if custom := strings.TrimSpace(viper.GetString("api-key-env-var")); custom != "" {
		envVars = append(envVars, custom)
	}
	envVars = append(envVars, envPrefix+"_API_KEY")
	if name == "Authorization" || prefix != "" {
		envVars = append(envVars, envPrefix+"_TOKEN")
	}

	normalizedName := strings.ToUpper(strings.NewReplacer("-", "_", " ", "_").Replace(name))
	if normalizedName != "" {
		envVars = append(envVars, envPrefix+"_"+normalizedName)
	}

	seen := make(map[string]bool)
	unique := make([]string, 0, len(envVars))
	for _, envVar := range envVars {
		if envVar == "" || seen[envVar] {
			continue
		}
		seen[envVar] = true
		unique = append(unique, envVar)
	}

	return unique
}

// Init sets up the API key client authentication. Must be called *after* you
// have called `cli.Init()`. Passing `extra` values will set additional custom
// keys to store for each profile.
func Init(name string, in Location, extra ...string) {
	initWithPrefix(name, in, "", extra...)
}

// InitBearer configures a standard Authorization: Bearer <token> flow.
func InitBearer(name string, extra ...string) {
	initWithPrefix(name, LocationHeader, "Bearer ", extra...)
}

func initWithPrefix(name string, in Location, prefix string, extra ...string) {
	cli.UseAuth("", &Handler{
		Name:    name,
		In:      in,
		Keys:    extra,
		EnvVars: defaultEnvVars(name, prefix),
		Prefix:  prefix,
	})
}
