package cli

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"strings"

	"github.com/orq-ai/bartolo/shorthand"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	yaml "gopkg.in/yaml.v2"
)

// BodyField describes a generated typed body flag.
type BodyField struct {
	Name        string
	FlagName    string
	Type        string
	Description string
}

// DeepAssign recursively merges a source map into the target.
func DeepAssign(target, source map[string]interface{}) {
	for k, v := range source {
		if vm, ok := v.(map[string]interface{}); ok {
			if _, ok := target[k]; ok {
				if tkm, ok := target[k].(map[string]interface{}); ok {
					DeepAssign(tkm, vm)
				} else {
					target[k] = vm
				}
			} else {
				target[k] = vm
			}
		} else {
			target[k] = v
		}
	}
}

// AddBodyFlags installs the shared request-body flags for commands that accept
// structured input.
func AddBodyFlags(cmd *cobra.Command) {
	cmd.Flags().String("from-file", "", "Read the request body from a file path")
	cmd.Flags().Bool("stdin", false, "Require request body input from stdin")
	cmd.Flags().Bool("example", false, "Use the first generated body example as the request body")
}

// AddBodyFieldFlags installs generated typed request-body flags for simple
// top-level body fields.
func AddBodyFieldFlags(cmd *cobra.Command, fields []BodyField) {
	for _, field := range fields {
		description := field.Description
		if strings.TrimSpace(description) == "" {
			description = field.Name
		}
		switch field.Type {
		case "bool":
			cmd.Flags().Bool(field.FlagName, false, description)
		case "int64":
			cmd.Flags().Int64(field.FlagName, 0, description)
		case "float64":
			cmd.Flags().Float64(field.FlagName, 0, description)
		default:
			cmd.Flags().String(field.FlagName, "", description)
		}
	}
}

// GetBody returns the request body if one was passed via stdin, a file, a
// generated example, or shorthand CLI arguments.
func GetBody(mediaType string, args []string, params *viper.Viper, examples []string) (string, error) {
	body, err := loadBaseBody(params, mediaType, examples)
	if err != nil {
		return "", err
	}

	if len(args) == 0 {
		return body, nil
	}

	result, err := shorthand.ParseAndBuild("stdin", strings.Join(args, " "))
	if err != nil {
		return "", err
	}

	return mergeStructuredBody(mediaType, body, result)
}

// ApplyBodyFlags overlays generated typed body flags on top of the parsed
// request body. Only explicitly-set flags are applied.
func ApplyBodyFlags(cmd *cobra.Command, params *viper.Viper, mediaType string, body string, fields []BodyField) (string, error) {
	if cmd == nil || params == nil || len(fields) == 0 {
		return body, nil
	}

	overrides := map[string]interface{}{}
	for _, field := range fields {
		flag := cmd.Flags().Lookup(field.FlagName)
		if flag == nil || !flag.Changed {
			continue
		}

		switch field.Type {
		case "bool":
			overrides[field.Name] = params.GetBool(field.FlagName)
		case "int64":
			overrides[field.Name] = params.GetInt64(field.FlagName)
		case "float64":
			overrides[field.Name] = params.GetFloat64(field.FlagName)
		default:
			overrides[field.Name] = params.GetString(field.FlagName)
		}
	}

	if len(overrides) == 0 {
		return body, nil
	}

	return mergeStructuredBody(mediaType, body, overrides)
}

func loadBaseBody(params *viper.Viper, mediaType string, examples []string) (string, error) {
	if params != nil {
		if filename := strings.TrimSpace(params.GetString("from-file")); filename != "" {
			input, err := ioutil.ReadFile(filename)
			if err != nil {
				return "", err
			}
			return string(input), nil
		}

		if params.GetBool("example") {
			if len(examples) == 0 {
				return "", fmt.Errorf("no generated body example is available for this command")
			}

			result, err := shorthand.ParseAndBuild("example", examples[0])
			if err != nil {
				return "", err
			}
			return mergeStructuredBody(mediaType, "", result)
		}
	}

	info, err := os.Stdin.Stat()
	if err != nil {
		return "", err
	}

	if params != nil && params.GetBool("stdin") {
		if (info.Mode() & os.ModeCharDevice) != 0 {
			return "", fmt.Errorf("stdin requested but no piped input was detected")
		}
	}

	if (info.Mode() & os.ModeCharDevice) != 0 {
		return "", nil
	}

	input, err := ioutil.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}

	body := string(input)
	log.Debug().Msgf("Body from stdin is: %s", body)
	return body, nil
}

func mergeStructuredBody(mediaType string, body string, result map[string]interface{}) (string, error) {
	if strings.Contains(mediaType, "json") {
		if body != "" {
			var curBody map[string]interface{}
			if err := json.Unmarshal([]byte(body), &curBody); err != nil {
				return "", err
			}

			DeepAssign(curBody, result)
			result = curBody
		}

		marshalled, err := json.Marshal(result)
		if err != nil {
			return "", err
		}

		return string(marshalled), nil
	}

	if strings.Contains(mediaType, "yaml") {
		if body != "" {
			var curBody map[string]interface{}
			if err := yaml.Unmarshal([]byte(body), &curBody); err != nil {
				return "", err
			}

			DeepAssign(curBody, result)
			result = curBody
		}

		marshalled, err := yaml.Marshal(result)
		if err != nil {
			return "", err
		}

		return string(marshalled), nil
	}

	return "", fmt.Errorf("not sure how to marshal %s", mediaType)
}
