// Package apikey provides authentication profile support for APIs that require
// a pre-generated constant authenticationn key passed via a header, query
// parameter, or cookie value in each request.
package apikey

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/orq/bartolo/cli"
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

// OnRequest gets run before the request goes out on the wire.
func (h *Handler) OnRequest(log *zerolog.Logger, request *http.Request) error {
	profile := cli.GetProfile()
	key, source := h.lookupKey(profile)
	if key == "" {
		return fmt.Errorf("missing API key; configure a profile with `auth add-profile` or set one of %s", strings.Join(h.EnvVars, ", "))
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
	key, source := h.lookupKey(profile)
	status := map[string]interface{}{
		"configured": key != "",
		"source":     source,
	}

	if len(h.EnvVars) > 0 {
		status["env_vars"] = h.EnvVars
	}

	return status
}

func (h *Handler) lookupKey(profile map[string]string) (string, string) {
	for _, envVar := range h.EnvVars {
		if value := strings.TrimSpace(os.Getenv(envVar)); value != "" {
			return h.applyPrefix(value), "env"
		}
	}

	if value := strings.TrimSpace(profile[apiKey]); value != "" {
		return h.applyPrefix(value), "profile"
	}

	return "", "missing"
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

	envVars := []string{envPrefix + "_API_KEY"}
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
