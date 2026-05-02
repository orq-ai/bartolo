package cli

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"strconv"
	"strings"

	"github.com/orq-ai/bartolo/shorthand"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	yaml "gopkg.in/yaml.v2"
)

// Sentinel value users can pass to nullable scalar flags to send an explicit
// JSON null (e.g. --display-name null).
const nullableFlagSentinel = "null"

// BodyField describes a generated typed body flag.
//
// Type is one of:
//   - "string", "bool", "int64", "float64": plain scalar.
//   - "string-nullable", "bool-nullable", "int64-nullable", "float64-nullable":
//     scalar that also accepts null. Pass the literal "null" to send JSON null.
//   - "string-slice", "int64-slice", "float64-slice", "bool-slice":
//     repeatable scalar list (`--tag a --tag b` or `--tag a,b`).
//   - "string-map": map of string→string (`--metadata key=value`, repeatable).
//   - "enum-string": string flag whose value is validated against Enum.
//   - "json": fallback for nested objects, arrays of objects, and
//     polymorphic unions. Value is parsed as JSON before being merged into
//     the request body.
type BodyField struct {
	Name        string
	FlagName    string
	Type        string
	Description string
	Enum        []string
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
		case "string-nullable", "bool-nullable", "int64-nullable", "float64-nullable":
			cmd.Flags().String(field.FlagName, "", description+` (pass "null" to send JSON null)`)
		case "string-slice":
			cmd.Flags().StringSlice(field.FlagName, nil, description+" (repeatable)")
		case "int64-slice":
			cmd.Flags().Int64Slice(field.FlagName, nil, description+" (repeatable)")
		case "float64-slice":
			cmd.Flags().Float64Slice(field.FlagName, nil, description+" (repeatable)")
		case "bool-slice":
			cmd.Flags().BoolSlice(field.FlagName, nil, description+" (repeatable)")
		case "string-map":
			cmd.Flags().StringToString(field.FlagName, nil, description+" (key=value, repeatable)")
		case "json":
			cmd.Flags().String(field.FlagName, "", description+" (JSON value, e.g. '{\"k\":1}' or '[1,2]')")
		case "enum-string":
			cmd.Flags().String(field.FlagName, "", description)
			if len(field.Enum) > 0 {
				values := append([]string{}, field.Enum...)
				_ = cmd.RegisterFlagCompletionFunc(field.FlagName, func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
					return values, cobra.ShellCompDirectiveNoFileComp
				})
			}
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
		case "string-nullable":
			raw := params.GetString(field.FlagName)
			if raw == nullableFlagSentinel {
				overrides[field.Name] = nil
			} else {
				overrides[field.Name] = raw
			}
		case "bool-nullable":
			raw := strings.TrimSpace(params.GetString(field.FlagName))
			if raw == nullableFlagSentinel {
				overrides[field.Name] = nil
				break
			}
			value, err := strconv.ParseBool(raw)
			if err != nil {
				return "", fmt.Errorf("--%s: %w", field.FlagName, err)
			}
			overrides[field.Name] = value
		case "int64-nullable":
			raw := strings.TrimSpace(params.GetString(field.FlagName))
			if raw == nullableFlagSentinel {
				overrides[field.Name] = nil
				break
			}
			value, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return "", fmt.Errorf("--%s: %w", field.FlagName, err)
			}
			overrides[field.Name] = value
		case "float64-nullable":
			raw := strings.TrimSpace(params.GetString(field.FlagName))
			if raw == nullableFlagSentinel {
				overrides[field.Name] = nil
				break
			}
			value, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				return "", fmt.Errorf("--%s: %w", field.FlagName, err)
			}
			overrides[field.Name] = value
		case "string-slice":
			values, err := cmd.Flags().GetStringSlice(field.FlagName)
			if err != nil {
				return "", fmt.Errorf("--%s: %w", field.FlagName, err)
			}
			overrides[field.Name] = values
		case "int64-slice":
			values, err := cmd.Flags().GetInt64Slice(field.FlagName)
			if err != nil {
				return "", fmt.Errorf("--%s: %w", field.FlagName, err)
			}
			overrides[field.Name] = values
		case "float64-slice":
			values, err := cmd.Flags().GetFloat64Slice(field.FlagName)
			if err != nil {
				return "", fmt.Errorf("--%s: %w", field.FlagName, err)
			}
			overrides[field.Name] = values
		case "bool-slice":
			values, err := cmd.Flags().GetBoolSlice(field.FlagName)
			if err != nil {
				return "", fmt.Errorf("--%s: %w", field.FlagName, err)
			}
			overrides[field.Name] = values
		case "string-map":
			values, err := cmd.Flags().GetStringToString(field.FlagName)
			if err != nil {
				return "", fmt.Errorf("--%s: %w", field.FlagName, err)
			}
			overrides[field.Name] = values
		case "json":
			raw := strings.TrimSpace(params.GetString(field.FlagName))
			if raw == "" {
				continue
			}
			var value interface{}
			if err := json.Unmarshal([]byte(raw), &value); err != nil {
				return "", fmt.Errorf("--%s: invalid JSON: %w", field.FlagName, err)
			}
			overrides[field.Name] = value
		case "enum-string":
			value := params.GetString(field.FlagName)
			if len(field.Enum) > 0 {
				allowed := false
				for _, candidate := range field.Enum {
					if candidate == value {
						allowed = true
						break
					}
				}
				if !allowed {
					return "", fmt.Errorf("--%s: %q is not one of [%s]", field.FlagName, value, strings.Join(field.Enum, ", "))
				}
			}
			overrides[field.Name] = value
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
