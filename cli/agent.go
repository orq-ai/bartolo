package cli

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

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

// ResolveServer returns the active server URL from either the override flag or
// the registered OpenAPI server list.
func ResolveServer() string {
	if override := viper.GetString("server"); override != "" {
		return override
	}

	if len(registeredServers) == 0 {
		return ""
	}

	index := viper.GetInt("server-index")
	if index < 0 || index >= len(registeredServers) {
		index = 0
	}

	return registeredServers[index]["url"]
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
		if err := RunAuthSetup(viper.GetString("profile"), ""); err != nil {
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

			body, err := GetBody(params.GetString("content-type"), args[2:], params, nil)
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
