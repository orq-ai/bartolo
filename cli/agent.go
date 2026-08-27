package cli

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	gentleman "gopkg.in/h2non/gentleman.v2"
)

var registeredServers []map[string]string

// RegisterServers stores the generated server list so built-in commands like
// `doctor` and `request` can use the same endpoint defaults as generated API
// commands.
func RegisterServers(servers []map[string]string) {
	registeredServers = make([]map[string]string, 0, len(servers))
	for _, server := range servers {
		copyServer := make(map[string]string, len(server))
		for key, value := range server {
			copyServer[key] = value
		}
		registeredServers = append(registeredServers, copyServer)
	}
}

// GetServers returns a copy of the registered server list.
func GetServers() []map[string]string {
	servers := make([]map[string]string, 0, len(registeredServers))
	for _, server := range registeredServers {
		copyServer := make(map[string]string, len(server))
		for key, value := range server {
			copyServer[key] = value
		}
		servers = append(servers, copyServer)
	}

	return servers
}

// NormalizeServerURL turns a user-supplied server value into a usable API base
// URL, or explains why it cannot be one. Without it a typo like `htp://x` is
// persisted happily and only surfaces later as an opaque transport error.
//
// A value with no scheme gets `https://`, since that is what a remote API base
// URL almost always is. Loopback hosts are the exception and must say which
// scheme they mean: a local dev server on `localhost:8080` is usually plain
// HTTP, so guessing https there would be wrong more often than right.
//
// The bool reports whether a scheme was added, so callers can say so.
func NormalizeServerURL(raw string) (string, bool, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false, fmt.Errorf("server URL cannot be empty")
	}

	candidate, added, err := withScheme(trimmed)
	if err != nil {
		return "", false, err
	}

	parsed, err := url.Parse(candidate)
	if err != nil {
		return "", false, fmt.Errorf("server URL %q is not a valid URL: %w", trimmed, err)
	}

	switch {
	case !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https"):
		return "", false, fmt.Errorf("server URL %q must start with http:// or https://", trimmed)
	case parsed.Hostname() == "":
		return "", false, fmt.Errorf("server URL %q is missing a host", trimmed)
	case added && parsed.User != nil:
		// `user@host.com` reads as a bare host, and prepending a scheme would
		// silently turn the local part into URL credentials.
		return "", false, fmt.Errorf("server URL %q is ambiguous: write it as https://%s", trimmed, trimmed)
	case parsed.RawQuery != "" || parsed.Fragment != "":
		// The generated client builds request URLs as server+path, so a query or
		// fragment here would end up in the middle of the URL.
		return "", false, fmt.Errorf("server URL %q must not carry a query or fragment", trimmed)
	}

	// Same reason: a trailing slash would produce `https://host//things`.
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")

	// Scheme and host are case-insensitive, but `server list` and ResolveServer
	// compare URLs as plain strings, so an uppercase value would never match the
	// server it names.
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)

	return parsed.String(), added, nil
}

var schemePrefix = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.\-]*://`)

// withScheme prepends `https://` to a value that has none, and refuses to guess
// for loopback hosts. Testing for the `scheme://` prefix rather than asking
// url.Parse is deliberate: RFC 3986 reads `localhost:8080` as scheme
// `localhost` with opaque part `8080`, so url.Parse cannot tell a scheme-less
// `host:port` from a real opaque URI like `mailto:someone`.
func withScheme(trimmed string) (string, bool, error) {
	if schemePrefix.MatchString(trimmed) {
		return trimmed, false, nil
	}

	// Protocol-relative, e.g. `//api.example.com`. Strip the prefix so the
	// loopback rule below sees the same authority it would for a bare host.
	authority := strings.TrimPrefix(trimmed, "//")

	// `https:/api.example.com` is a mistyped scheme, not a host. Without this it
	// would be read as a bare host and become `https://https:/api.example.com`,
	// a syntactically valid URL pointing at the host `https`.
	if host, _, _ := strings.Cut(authority, "/"); schemeTypo.MatchString(host) {
		return "", false, fmt.Errorf("server URL %q is not a valid URL: write it as http://... or https://...", trimmed)
	}

	if host, _, _ := strings.Cut(authority, "/"); isLoopback(host) {
		return "", false, fmt.Errorf("server URL %q is ambiguous: write it as http://%s or https://%s", trimmed, authority, authority)
	}

	return "https://" + authority, true, nil
}

// schemeTypo matches an authority that is really a scheme with too few slashes,
// e.g. `https:/host` or the bare `https:`.
var schemeTypo = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.\-]*:$`)

// isLoopback reports whether a scheme-less authority points at this machine,
// where http is at least as likely as https.
func isLoopback(authority string) bool {
	host := authority
	if h, _, err := net.SplitHostPort(authority); err == nil {
		host = h
	}
	host = strings.ToLower(strings.Trim(host, "[]"))

	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}

	ip := net.ParseIP(host)

	return ip != nil && ip.IsLoopback()
}

// normalizeServerURLWarn is NormalizeServerURL plus the one place the
// scheme-was-guessed warning is worded.
func normalizeServerURLWarn(raw string) (string, error) {
	normalized, added, err := NormalizeServerURL(raw)
	if err != nil {
		return "", err
	}

	if added {
		log.Warn().Msgf("No scheme detected in %q, using %s", strings.TrimSpace(raw), normalized)
	}

	return normalized, nil
}

// ResolveServer returns the active server URL, most specific source first:
// `--server`, `<PREFIX>_SERVER` or viper.Set, the active profile's server, the
// `server set` default (kept off the flag's key so viper cannot rank it as
// explicit), then the selected OpenAPI server.
//
// Normalization happens here rather than only where a value is written, because
// a config or credentials file can be written by an older bartolo, edited by
// hand, or supplied as `<PREFIX>_SERVER_DEFAULT` — all of which bypass every
// write path. A value that cannot be normalized is returned as-is: this is the
// request path, and a transport error naming the bad URL beats swallowing it.
func ResolveServer() string {
	return normalizeResolved(resolveServerRaw())
}

func resolveServerRaw() string {
	if override := strings.TrimSpace(viper.GetString("server")); override != "" {
		return override
	}

	if server := ProfileServer(); server != "" {
		return server
	}

	if persisted := strings.TrimSpace(viper.GetString("server-default")); persisted != "" {
		return persisted
	}

	return SelectedServer()
}

// normalizeResolved normalizes a server URL on the read path, warning at most
// once per distinct value so a per-request resolve does not spam stderr.
func normalizeResolved(raw string) string {
	if raw == "" {
		return raw
	}

	normalized, added, err := NormalizeServerURL(raw)
	if err != nil {
		warnServerOnce(raw, fmt.Sprintf("Server URL %q is not usable: %v", raw, err))

		return raw
	}

	if added {
		warnServerOnce(raw, fmt.Sprintf("No scheme detected in %q, using %s", raw, normalized))
	}

	return normalized
}

var warnedServers sync.Map

func warnServerOnce(key, msg string) {
	if _, seen := warnedServers.LoadOrStore(key, struct{}{}); !seen {
		log.Warn().Msg(msg)
	}
}

// SelectedServer returns the registered OpenAPI server at `server-index`,
// ignoring overrides. An out-of-range index falls back to the first entry.
func SelectedServer() string {
	if len(registeredServers) == 0 {
		return ""
	}

	index := viper.GetInt("server-index")
	if index < 0 || index >= len(registeredServers) {
		index = 0
	}

	return registeredServers[index]["url"]
}

// ProfileServer returns the server bound to the active credentials profile.
func ProfileServer() string {
	if Creds == nil {
		return ""
	}

	return strings.TrimSpace(GetProfile()["server"])
}

func initAgentCommands() {
	Root.AddCommand(newDoctorCommand())
	Root.AddCommand(newRequestCommand())
	Root.AddCommand(newServerCommand())
}

func newDoctorCommand() *cobra.Command {
	var fix bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Show CLI health, auth, and server configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			status := doctorStatus()
			if fix {
				if fixed, err := runDoctorFixes(status); err != nil {
					return err
				} else if fixed {
					status = doctorStatus()
				}
			}
			return Formatter.Format(status)
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false, "Attempt safe local fixes such as interactive auth setup")
	return cmd
}

func doctorStatus() map[string]interface{} {
	auth := GetAuthStatus()
	fixable := make([]string, 0, 1)
	if configured, _ := auth["configured"].(bool); !configured {
		fixable = append(fixable, "auth")
	}

	return map[string]interface{}{
		"app": map[string]interface{}{
			"name":    viper.GetString("app-name"),
			"version": Root.Version,
		},
		"config": map[string]interface{}{
			"directory":       viper.GetString("config-directory"),
			"profile":         viper.GetString("profile"),
			"server_index":    viper.GetInt("server-index"),
			"server_override": viper.GetString("server"),
			"profile_server":  ProfileServer(),
			"server_default":  viper.GetString("server-default"),
			"selected_server": ResolveServer(),
		},
		"servers": GetServers(),
		"auth":    auth,
		"checks": map[string]interface{}{
			"reachability": map[string]interface{}{
				"checked": false,
			},
			"fixable": fixable,
		},
	}
}

func runDoctorFixes(status map[string]interface{}) (bool, error) {
	auth, _ := status["auth"].(map[string]interface{})
	if configured, _ := auth["configured"].(bool); !configured {
		if err := RunAuthSetup(viper.GetString("profile"), "", ""); err != nil {
			return false, err
		}
		return true, nil
	}

	return false, nil
}

func newRequestCommand() *cobra.Command {
	params := viper.New()

	cmd := &cobra.Command{
		Use:   "request <method> <path-or-url> [body]",
		Short: "Make a raw API request using configured auth and server defaults",
		Long:  "Use an absolute URL or a path like /v1/me. Request bodies can be passed via stdin or CLI shorthand.",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			method := strings.ToUpper(args[0])
			target := args[1]

			if method == "DELETE" {
				if err := ConfirmDestructive(cmd, args); err != nil {
					return err
				}
			}

			url := target
			if !strings.Contains(target, "://") {
				server := ResolveServer()
				if server == "" {
					return fmt.Errorf("no server configured; pass --server or generate commands from an OpenAPI spec with servers")
				}

				url = strings.TrimRight(server, "/") + "/" + strings.TrimLeft(target, "/")
			}

			req := Client.Request().Method(method).URL(url)

			for _, header := range params.GetStringSlice("header") {
				name, value, ok := strings.Cut(header, ":")
				if !ok {
					return fmt.Errorf("invalid header %q, expected 'Name: Value'", header)
				}

				req = req.AddHeader(strings.TrimSpace(name), strings.TrimSpace(value))
			}

			body, err := GetBody(params.GetString("content-type"), args[2:], params)
			if err != nil {
				return err
			}

			if body != "" {
				req = req.AddHeader("Content-Type", params.GetString("content-type")).BodyString(body)
			}

			handlerPath := "request " + strings.ToLower(method)
			HandleBefore(handlerPath, params, req)

			resp, err := req.Do()
			if err != nil {
				return err
			}

			decoded, err := decodeRawResponse(resp)
			if err != nil {
				return err
			}

			return Formatter.Format(HandleAfter(handlerPath, params, resp, decoded))
		},
	}

	cmd.Flags().StringSlice("header", nil, "Additional request header in 'Name: Value' form")
	cmd.Flags().String("content-type", "application/json", "Content type to use when sending a request body")
	AddForceFlag(cmd)
	AddBodyFlags(cmd)

	if cmd.Flags().HasFlags() {
		params.BindPFlags(cmd.Flags())
	}

	return cmd
}

func decodeRawResponse(resp *gentleman.Response) (map[string]interface{}, error) {
	result := map[string]interface{}{
		"ok":     resp.StatusCode < 400,
		"status": resp.StatusCode,
		"body":   nil,
	}

	headers := make(map[string]interface{}, len(resp.Header))
	for key, values := range resp.Header {
		copyValues := append([]string{}, values...)
		sort.Strings(copyValues)
		headers[key] = copyValues
	}
	result["headers"] = headers

	data := resp.Bytes()
	if len(data) == 0 {
		return result, nil
	}

	var body interface{}
	if err := unmarshalBody(http.Header(resp.Header), data, &body); err == nil {
		result["body"] = body
		return result, nil
	}

	result["body_text"] = string(data)
	return result, nil
}
