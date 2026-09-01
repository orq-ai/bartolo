package cli

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"strconv"
	"strings"
	"time"

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
//   - "datetime": `format: date-time` string, normalized to RFC 3339 before the
//     request is built. See NormalizeDateTime for the accepted forms.
//   - "datetime-nullable": the same, but the literal "null" sends JSON null.
//   - "json": fallback for nested objects, arrays of objects, and
//     polymorphic unions. Value is parsed as JSON before being merged into
//     the request body.
//   - "json-or-string": polymorphic union that includes a string branch (e.g.
//     `model: string | object`). A value starting with '{', '[' or '"' is
//     parsed as JSON; any other value is sent through verbatim as a string, so
//     bare scalars like "openai/gpt-4o" need no double-quoting.
type BodyField struct {
	Name        string
	FlagName    string
	Type        string
	Description string
	Enum        []string
}

// flagName returns the name this body field is actually registered under. A
// field whose name collides with a reserved flag (`raw`, `profile`, `force`, ...) is
// exposed as `--body-<name>` so the reserved one keeps working on the command. The
// generator already emits the resolved name; this repeats it so CLIs generated
// before the fix are corrected by a dependency bump alone.
func (f BodyField) flagName() string {
	return ResolveGeneratedFlagName("body", f.FlagName)
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
}

// AddExampleFlag installs --example on commands that have a generated example
// request body.
func AddExampleFlag(cmd *cobra.Command) {
	cmd.Flags().Bool("example", false, "Print an example request body for this command and exit without sending a request")
}

// PrintBodyExample prints the generated example request body when --example
// was passed and reports whether it did, so the caller can return without
// sending a request. It takes precedence over every other body input.
func PrintBodyExample(params *viper.Viper, example string) bool {
	if params == nil || !params.GetBool("example") || example == "" {
		return false
	}
	fmt.Fprintln(Stdout, example)
	return true
}

// AddBodyFieldFlags installs generated typed request-body flags for simple
// top-level body fields.
func AddBodyFieldFlags(cmd *cobra.Command, fields []BodyField) {
	for _, field := range fields {
		description := field.Description
		if strings.TrimSpace(description) == "" {
			description = field.Name
		}
		name := field.flagName()
		if name != field.FlagName {
			kind, _ := ReservedFlagName(field.FlagName)
			if kind == "" {
				kind = "reserved flag"
			}
			description += fmt.Sprintf(" (body field %q, renamed to keep the %s --%s available)", field.Name, kind, field.FlagName)
		}
		switch field.Type {
		case "bool":
			cmd.Flags().Bool(name, false, description)
		case "int64":
			cmd.Flags().Int64(name, 0, description)
		case "float64":
			cmd.Flags().Float64(name, 0, description)
		case "string-nullable", "bool-nullable", "int64-nullable", "float64-nullable":
			cmd.Flags().String(name, "", description+` (pass "null" to send JSON null)`)
		case "string-slice":
			cmd.Flags().StringSlice(name, nil, description+" (repeatable)")
		case "int64-slice":
			cmd.Flags().Int64Slice(name, nil, description+" (repeatable)")
		case "float64-slice":
			cmd.Flags().Float64Slice(name, nil, description+" (repeatable)")
		case "bool-slice":
			cmd.Flags().BoolSlice(name, nil, description+" (repeatable)")
		case "string-map":
			cmd.Flags().StringToString(name, nil, description+" (key=value, repeatable)")
		case "datetime":
			cmd.Flags().String(name, "", WithDateTimeHelp(description))
		case "datetime-nullable":
			cmd.Flags().String(name, "", WithDateTimeHelp(description)+` (pass "null" to send JSON null)`)
		case "json":
			cmd.Flags().String(name, "", description+" (JSON value, e.g. '{\"k\":1}' or '[1,2]')")
		case "json-or-string":
			cmd.Flags().String(name, "", description+" (plain string, or JSON for objects/arrays, e.g. '{\"k\":1}')")
		case "enum-string":
			if len(field.Enum) > 0 {
				description += fmt.Sprintf(" (one of: %s)", strings.Join(field.Enum, ", "))
			}
			cmd.Flags().String(name, "", description)
			if len(field.Enum) > 0 {
				values := append([]string{}, field.Enum...)
				_ = cmd.RegisterFlagCompletionFunc(name, func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
					return values, cobra.ShellCompDirectiveNoFileComp
				})
			}
		default:
			cmd.Flags().String(name, "", description)
		}
	}
}

// GetBody returns the request body if one was passed via stdin, a file, or
// shorthand CLI arguments.
func GetBody(mediaType string, args []string, params *viper.Viper) (string, error) {
	return GetBodyWithFlags(nil, mediaType, args, params, nil)
}

// GetBodyWithFlags resolves the request body from every supported source and
// overlays the generated typed body flags, lowest precedence first: stdin,
// --from-file, then shorthand CLI arguments, then body flags.
//
// Passing cmd and fields lets the resolver see whether the body is already
// satisfied before it decides to read stdin, which is what keeps a command
// from blocking on an idle pipe. See loadBaseBody for the stdin rules.
func GetBodyWithFlags(cmd *cobra.Command, mediaType string, args []string, params *viper.Viper, fields []BodyField) (string, error) {
	body, err := getBodyWithFlags(cmd, mediaType, args, params, fields)
	if err != nil {
		// Every failure here is the user's own input, so it exits 2, and classifying it once keeps the generated caller a plain wrap.
		return "", NewValueError(err)
	}

	return body, nil
}

func getBodyWithFlags(cmd *cobra.Command, mediaType string, args []string, params *viper.Viper, fields []BodyField) (string, error) {
	body, err := loadBaseBody(params, bodySuppliedElsewhere(cmd, params, args, fields))
	if err != nil {
		return "", err
	}

	if len(args) > 0 {
		result, err := shorthand.ParseAndBuild("stdin", strings.Join(args, " "))
		if err != nil {
			return "", err
		}

		if err := normalizeShorthandDateTimes(result, fields); err != nil {
			return "", err
		}

		body, err = mergeStructuredBody(mediaType, body, result)
		if err != nil {
			return "", err
		}
	}

	return ApplyBodyFlags(cmd, params, mediaType, body, fields)
}

// normalizeShorthandDateTimes applies date-time normalization to top-level
// shorthand values, so `from: 24h` means what `--from 24h` means. Shorthand is
// typed by hand at a prompt and deserves the same convenience as a flag; a body
// from --from-file or stdin is machine-written, so it is passed through as given
// rather than silently rewritten.
func normalizeShorthandDateTimes(body map[string]interface{}, fields []BodyField) error {
	for _, field := range fields {
		if field.Type != "datetime" && field.Type != "datetime-nullable" {
			continue
		}

		raw, ok := body[field.Name].(string)
		if !ok {
			// Absent, or already a non-string the server will judge.
			continue
		}
		if field.Type == "datetime-nullable" && strings.TrimSpace(raw) == nullableFlagSentinel {
			body[field.Name] = nil
			continue
		}

		normalized, err := NormalizeDateTime(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", field.Name, err)
		}
		body[field.Name] = normalized
	}

	return nil
}

// bodySuppliedElsewhere reports whether a source other than stdin has already
// produced request-body content.
func bodySuppliedElsewhere(cmd *cobra.Command, params *viper.Viper, args []string, fields []BodyField) bool {
	if len(args) > 0 {
		return true
	}

	if params != nil {
		if strings.TrimSpace(params.GetString("from-file")) != "" {
			return true
		}
	}

	if cmd == nil {
		return false
	}

	for _, field := range fields {
		if flag := cmd.Flags().Lookup(field.FlagName); flag != nil && flag.Changed {
			return true
		}
	}

	return false
}

// ApplyBodyFlags overlays generated typed body flags on top of the parsed
// request body. Only explicitly-set flags are applied.
func ApplyBodyFlags(cmd *cobra.Command, params *viper.Viper, mediaType string, body string, fields []BodyField) (string, error) {
	if cmd == nil || params == nil || len(fields) == 0 {
		return body, nil
	}

	overrides := map[string]interface{}{}
	for _, field := range fields {
		name := field.flagName()
		flag := cmd.Flags().Lookup(name)
		if flag == nil || !flag.Changed {
			continue
		}

		switch field.Type {
		case "bool":
			overrides[field.Name] = params.GetBool(name)
		case "int64":
			overrides[field.Name] = params.GetInt64(name)
		case "float64":
			overrides[field.Name] = params.GetFloat64(name)
		case "string-nullable":
			raw := params.GetString(name)
			if raw == nullableFlagSentinel {
				overrides[field.Name] = nil
			} else {
				overrides[field.Name] = raw
			}
		case "bool-nullable":
			raw := strings.TrimSpace(params.GetString(name))
			if raw == nullableFlagSentinel {
				overrides[field.Name] = nil
				break
			}
			value, err := strconv.ParseBool(raw)
			if err != nil {
				return "", fmt.Errorf("--%s: %w", name, err)
			}
			overrides[field.Name] = value
		case "int64-nullable":
			raw := strings.TrimSpace(params.GetString(name))
			if raw == nullableFlagSentinel {
				overrides[field.Name] = nil
				break
			}
			value, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return "", fmt.Errorf("--%s: %w", name, err)
			}
			overrides[field.Name] = value
		case "float64-nullable":
			raw := strings.TrimSpace(params.GetString(name))
			if raw == nullableFlagSentinel {
				overrides[field.Name] = nil
				break
			}
			value, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				return "", fmt.Errorf("--%s: %w", name, err)
			}
			overrides[field.Name] = value
		case "string-slice":
			values, err := cmd.Flags().GetStringSlice(name)
			if err != nil {
				return "", fmt.Errorf("--%s: %w", name, err)
			}
			overrides[field.Name] = values
		case "int64-slice":
			values, err := cmd.Flags().GetInt64Slice(name)
			if err != nil {
				return "", fmt.Errorf("--%s: %w", name, err)
			}
			overrides[field.Name] = values
		case "float64-slice":
			values, err := cmd.Flags().GetFloat64Slice(name)
			if err != nil {
				return "", fmt.Errorf("--%s: %w", name, err)
			}
			overrides[field.Name] = values
		case "bool-slice":
			values, err := cmd.Flags().GetBoolSlice(name)
			if err != nil {
				return "", fmt.Errorf("--%s: %w", name, err)
			}
			overrides[field.Name] = values
		case "string-map":
			values, err := cmd.Flags().GetStringToString(name)
			if err != nil {
				return "", fmt.Errorf("--%s: %w", name, err)
			}
			overrides[field.Name] = values
		case "datetime":
			value, err := NormalizeDateTimeFlag(name, params.GetString(name))
			if err != nil {
				return "", err
			}
			overrides[field.Name] = value
		case "datetime-nullable":
			// The sentinel is checked first, which is safe because
			// NormalizeDateTime rejects "null": neither behaviour can shadow the
			// other.
			if strings.TrimSpace(params.GetString(name)) == nullableFlagSentinel {
				overrides[field.Name] = nil
				break
			}
			value, err := NormalizeDateTimeFlag(name, params.GetString(name))
			if err != nil {
				return "", err
			}
			overrides[field.Name] = value
		case "json":
			raw := strings.TrimSpace(params.GetString(name))
			if raw == "" {
				continue
			}
			var value interface{}
			if err := json.Unmarshal([]byte(raw), &value); err != nil {
				return "", fmt.Errorf("--%s: invalid JSON: %w", name, err)
			}
			overrides[field.Name] = value
		case "json-or-string":
			raw := params.GetString(name)
			trimmed := strings.TrimSpace(raw)
			// Structured JSON (object/array) and an explicitly-quoted JSON
			// string are parsed as JSON; any other value is sent through as a
			// bare string. This lets `--model openai/gpt-4o` work without
			// double-quoting, while `--model '"openai/gpt-4o"'` (a JSON string
			// literal) still decodes to the same string for backward
			// compatibility.
			if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[' || trimmed[0] == '"') {
				var value interface{}
				if err := json.Unmarshal([]byte(trimmed), &value); err != nil {
					return "", fmt.Errorf("--%s: invalid JSON: %w", name, err)
				}
				overrides[field.Name] = value
			} else {
				overrides[field.Name] = raw
			}
		case "enum-string":
			value := params.GetString(name)
			if err := ValidateEnum("--"+name, value, field.Enum); err != nil {
				return "", err
			}
			overrides[field.Name] = value
		default:
			overrides[field.Name] = params.GetString(name)
		}
	}

	if len(overrides) == 0 {
		return body, nil
	}

	return mergeStructuredBody(mediaType, body, overrides)
}

// stdinPipeGrace is how long an implicit stdin read waits for a writer that
// may still be starting up before it gives up and falls back to the body that
// was already supplied by flags, --from-file or shorthand.
const stdinPipeGrace = 250 * time.Millisecond

func loadBaseBody(params *viper.Viper, bodyElsewhere bool) (string, error) {
	if params != nil {
		if filename := strings.TrimSpace(params.GetString("from-file")); filename != "" {
			input, err := ioutil.ReadFile(filename)
			if err != nil {
				return "", err
			}
			return string(input), nil
		}
	}

	// Resolve stdin once: an abandoned read below outlives this call, and it
	// must not race with anything that reassigns os.Stdin afterwards.
	stdin := os.Stdin

	info, err := stdin.Stat()
	if err != nil {
		return "", err
	}

	isTerminal := (info.Mode() & os.ModeCharDevice) != 0

	// --stdin means the caller insists on piped input, so wait for it however
	// long it takes.
	if params != nil && params.GetBool("stdin") {
		if isTerminal {
			return "", fmt.Errorf("stdin requested but no piped input was detected")
		}
		return readStdin(stdin)
	}

	if isTerminal {
		return "", nil
	}

	// A redirect from a regular file (`cmd < body.json`) always reaches EOF, so
	// it can be read unconditionally. Shorthand is documented to layer on top of
	// exactly that form.
	if info.Mode().IsRegular() {
		return readStdin(stdin)
	}

	// stdin is a pipe, FIFO or socket. Reading one that nobody ever writes to
	// blocks forever, and an open idle stdin is precisely what CI runners, task
	// runners and subprocess.Popen / child_process.spawn hand a child by
	// default. Only wait on it indefinitely when it is the sole possible source
	// of the body.
	if !bodyElsewhere {
		return readStdin(stdin)
	}

	body, ok, err := readStdinWithin(stdin, stdinPipeGrace)
	if err != nil {
		return "", err
	}
	if !ok {
		log.Debug().Msg("stdin is a pipe with no data pending; using the body from flags, --from-file or shorthand instead")
		return "", nil
	}

	return body, nil
}

func readStdin(stdin *os.File) (string, error) {
	input, err := ioutil.ReadAll(stdin)
	if err != nil {
		return "", err
	}

	body := string(input)
	log.Debug().Msgf("Body from stdin is: %s", body)
	return body, nil
}

// readStdinWithin reads stdin but reports ok=false if nothing has arrived
// within the grace period. An abandoned read stays parked in its goroutine
// until the process exits, which is harmless for a short-lived CLI.
func readStdinWithin(stdin *os.File, grace time.Duration) (string, bool, error) {
	type result struct {
		body string
		err  error
	}

	done := make(chan result, 1)
	go func() {
		body, err := readStdin(stdin)
		done <- result{body: body, err: err}
	}()

	timer := time.NewTimer(grace)
	defer timer.Stop()

	select {
	case r := <-done:
		return r.body, true, r.err
	case <-timer.C:
		return "", false, nil
	}
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
