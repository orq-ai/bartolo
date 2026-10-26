package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func newServerCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Inspect or persist server defaults",
	}
	cmd.AddCommand(newServerListCommand())
	cmd.AddCommand(newServerCurrentCommand())
	cmd.AddCommand(newServerUseCommand())
	cmd.AddCommand(newServerSetCommand())
	cmd.AddCommand(newServerClearCommand())
	return cmd
}

func newServerListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List generated server options",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			servers := GetServers()
			items := make([]map[string]interface{}, 0, len(servers))
			currentURL := ResolveServer()
			// Anything not from this list means an override is in play.
			overridden := currentURL != SelectedServer()

			for i, server := range servers {
				items = append(items, map[string]interface{}{
					"index":       i,
					"description": server["description"],
					"url":         server["url"],
					"selected":    server["url"] == currentURL,
				})
			}

			if HumanOutput() {
				for _, item := range items {
					item["selected"] = activeMarker(item["selected"] == true)
				}
				if err := FormatList(items, "selected", "index", "description", "url"); err != nil {
					return err
				}
				if overridden {
					fmt.Fprintf(Stdout, "None of these is in use: %s (%s).\n", currentURL, serverSource())
				}
				return nil
			}

			return FormatList(map[string]interface{}{
				"selected_server": currentURL,
				"overridden":      overridden,
				"servers":         items,
			})
		},
	}
}

func newServerCurrentCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "current",
		Short: "Show the currently selected server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return formatResult(map[string]interface{}{
				"server":          ResolveServer(),
				"server_index":    viper.GetInt("server-index"),
				"server_override": viper.GetString("server"),
				"server_default":  viper.GetString("server-default"),
				"profile_server":  ProfileServer(),
			}, fmt.Sprintf("Using %s (%s).", ResolveServer(), serverSource()))
		},
	}
}

func newServerUseCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "use <index|url|description>",
		Short: "Persist one of the generated servers as the default",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			index, server, err := resolveServerSelection(args[0])
			if err != nil {
				return err
			}

			if err := saveJSONConfig(map[string]interface{}{
				"server":         nil,
				"server-default": nil,
				"server-index":   index,
			}); err != nil {
				return err
			}

			return formatResult(map[string]interface{}{
				"persisted": true,
				"server":    server["url"],
				"index":     index,
			}, "Default server is now "+server["url"]+"."+serverStillOverridden(server["url"]))
		},
	}
}

func newServerSetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "set <url>",
		Short: "Persist a custom server override URL",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			url, err := normalizeServerURLWarn(args[0])
			if err != nil {
				return NewValueError(err)
			}

			if err := saveJSONConfig(map[string]interface{}{
				"server":         nil,
				"server-default": url,
			}); err != nil {
				return err
			}

			return formatResult(map[string]interface{}{
				"persisted": true,
				"server":    url,
				"override":  true,
			}, "Default server is now "+url+"."+serverStillOverridden(url))
		},
	}
}

func newServerClearCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "clear",
		Short: "Clear any persisted custom server override",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := saveJSONConfig(map[string]interface{}{
				"server":         nil,
				"server-default": nil,
			}); err != nil {
				return err
			}

			return formatResult(map[string]interface{}{
				"persisted": true,
				"server":    ResolveServer(),
				"override":  false,
			}, fmt.Sprintf("Cleared the custom server. Using %s (%s).", ResolveServer(), serverSource()))
		},
	}
}

// serverSource says where ResolveServer's value comes from, in
// resolveServerRaw's order.
func serverSource() string {
	if override, _ := flagValueIfChanged(Root, "server"); override != "" {
		return "from --server"
	}
	if ProfileServer() != "" {
		return "from profile " + ActiveProfileName()
	}
	if strings.TrimSpace(viper.GetString("server")) != "" {
		return "from the environment or config"
	}
	if strings.TrimSpace(viper.GetString("server-default")) != "" {
		return fmt.Sprintf("custom default; `%s server clear` resets it", Root.CommandPath())
	}
	servers := GetServers()
	index := viper.GetInt("server-index")
	if index < 0 || index >= len(servers) {
		index = 0
	}
	if len(servers) == 0 || servers[index]["description"] == "" {
		return "default"
	}
	return "default: " + strings.TrimSuffix(servers[index]["description"], ".")
}

// serverStillOverridden warns that a persisted default is not what the next
// command uses, because --server, a profile or the environment outranks it.
func serverStillOverridden(persisted string) string {
	if current := ResolveServer(); current != persisted {
		return fmt.Sprintf(" Still using %s (%s).", current, serverSource())
	}
	return ""
}

func resolveServerSelection(input string) (int, map[string]string, error) {
	servers := GetServers()
	if len(servers) == 0 {
		return 0, nil, fmt.Errorf("no generated servers are available")
	}

	if index, err := strconv.Atoi(input); err == nil {
		if index < 0 || index >= len(servers) {
			return 0, nil, fmt.Errorf("server index %d is out of range", index)
		}
		return index, servers[index], nil
	}

	needle := strings.ToLower(strings.TrimSpace(input))
	for i, server := range servers {
		if strings.ToLower(server["url"]) == needle || strings.ToLower(server["description"]) == needle {
			return i, server, nil
		}
	}

	return 0, nil, fmt.Errorf("could not match server %q", input)
}
