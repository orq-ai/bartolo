package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/orq/bartolo/shorthand"
	"github.com/spf13/cobra"
	toon "github.com/toon-format/toon-go"
	yaml "gopkg.in/yaml.v2"
)

func main() {
	var format *string

	cmd := &cobra.Command{
		Use:     fmt.Sprintf("%s [flags] key1: value1, key2: value2, ...", os.Args[0]),
		Short:   "Generate shorthand structured data",
		Example: fmt.Sprintf("%s foo.bar: 1, .baz: true", os.Args[0]),
		Args:    cobra.MinimumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			result, err := shorthand.ParseAndBuild("stdin", strings.Join(args, " "))
			if err != nil {
				panic(err)
			}

			var marshalled []byte

			switch *format {
			case "json":
				marshalled, err = json.MarshalIndent(result, "", "  ")
			case "yaml":
				marshalled, err = yaml.Marshal(result)
			case "toon":
				marshalled, err = toon.Marshal(result, toon.WithIndent(2))
			}

			if err != nil {
				panic(err)
			}

			fmt.Println(string(marshalled))
		},
	}

	format = cmd.Flags().StringP("format", "f", "json", "Output format [json, yaml, toon]")

	cmd.Execute()
}
