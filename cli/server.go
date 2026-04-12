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
			override := viper.GetString("server")

			for i, server := range servers {
				items = append(items, map[string]interface{}{
					"index":       i,
					"description": server["description"],
					"url":         server["url"],
					"selected":    server["url"] == currentURL,
					"override":    override != "" && override == server["url"],
				})
			}

			return Formatter.Format(map[string]interface{}{
				"selected_server": currentURL,
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
			return Formatter.Format(map[string]interface{}{
				"server":          ResolveServer(),
				"server_index":    viper.GetInt("server-index"),
				"server_override": viper.GetString("server"),
			})
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
				"server":       "",
				"server-index": index,
			}); err != nil {
				return err
			}

			return Formatter.Format(map[string]interface{}{
				"persisted": true,
				"server":    server["url"],
				"index":     index,
			})
		},
	}
}

func newServerSetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "set <url>",
		Short: "Persist a custom server override URL",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			url := strings.TrimSpace(args[0])
			if url == "" {
				return fmt.Errorf("server URL cannot be empty")
			}

			if err := saveJSONConfig(map[string]interface{}{
				"server": url,
			}); err != nil {
				return err
			}

			return Formatter.Format(map[string]interface{}{
				"persisted": true,
				"server":    url,
				"override":  true,
			})
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
				"server": "",
			}); err != nil {
				return err
			}

			return Formatter.Format(map[string]interface{}{
				"persisted": true,
				"server":    ResolveServer(),
				"override":  false,
			})
		},
	}
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
