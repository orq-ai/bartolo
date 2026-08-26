package cli

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"github.com/alecthomas/chroma"
	"github.com/alecthomas/chroma/quick"
	"github.com/alecthomas/chroma/styles"
	"github.com/olekukonko/tablewriter"
	"github.com/orq-ai/bartolo/internal/jmespathx"
	"github.com/spf13/viper"
	toon "github.com/toon-format/toon-go"
	"gopkg.in/yaml.v2"
)

func init() {
	// Simple 256-color theme for JSON/YAML output in a terminal.
	styles.Register(chroma.MustNewStyle("cli-dark", chroma.StyleEntries{
		// Used for JSON/YAML
		chroma.Comment:     "#9e9e9e",
		chroma.Keyword:     "#ff5f87",
		chroma.Punctuation: "#9e9e9e",
		chroma.NameTag:     "#5fafd7",
		chroma.Number:      "#d78700",
		chroma.String:      "#afd787",

		// Used for HTTP
		chroma.Name:          "#5fafd7",
		chroma.NameFunction:  "#ff5f87",
		chroma.NameNamespace: "#b2b2b2",

		// Used for Markdown
		chroma.GenericHeading:    "#5fafd7",
		chroma.GenericSubheading: "#5fafd7",
		chroma.GenericEmph:       "italic #875fd7",
		chroma.GenericStrong:     "bold #ffd787",
		chroma.GenericDeleted:    "#3a3a3a",
		chroma.NameAttribute:     "underline",
	}))
}

// ResponseFormatter will filter, prettify, and print out the results of a call.
type ResponseFormatter interface {
	Format(interface{}) error
}

// FormatList asks the configured formatter for its collection-aware output.
// The fallback keeps custom ResponseFormatter implementations source
// compatible while allowing the built-in formatter to render terminal tables.
func FormatList(data interface{}, columns ...string) error {
	if listFormatter, ok := Formatter.(interface {
		FormatList(interface{}, ...string) error
	}); ok {
		return listFormatter.FormatList(data, columns...)
	}
	return Formatter.Format(data)
}

// DefaultFormatter can apply JMESPath queries and can output prettyfied JSON,
// YAML, or TOON output. If Stdout is a TTY, then colorized output is provided.
// The default formatter uses the `jmespath` and `output-format` configuration
// values to perform JMESPath queries and set JSON (default), YAML, or TOON
// output.
type DefaultFormatter struct {
	tty bool
}

// NewDefaultFormatter creates a new formatted with autodetected TTY
// capabilities.
func NewDefaultFormatter(tty bool) *DefaultFormatter {
	return &DefaultFormatter{
		tty: tty,
	}
}

// Format will filter, prettify, colorize and output the data.
func (f *DefaultFormatter) Format(data interface{}) error {
	return f.format(data, false, nil)
}

// FormatList formats a collection response for a human at a terminal while
// retaining the normal serialized output for pipes and explicit format flags.
// Generated collection commands use this method so unrelated object
// responses (for example `doctor`) remain JSON by default.
func (f *DefaultFormatter) FormatList(data interface{}, columns ...string) error {
	return f.format(data, true, columns)
}

func (f *DefaultFormatter) format(data interface{}, list bool, columns []string) error {
	if !list {
		columns = nil
	}
	if data == nil {
		data = nil
	}

	if viper.GetString("jmespath") != "" {
		result, err := jmespathx.Search(viper.GetString("jmespath"), data)

		if err != nil {
			return err
		}

		data = result
	}

	if list && f.shouldRenderTable() {
		if rendered, err := renderTable(data, columns); err != nil {
			return err
		} else if rendered {
			return nil
		}
	}

	// Encode to the requested output format using nice formatting.
	var encoded []byte
	var err error
	var lexer string

	handled := false
	if data == nil {
		handled = true
		encoded = []byte("null")
		lexer = "json"
	}

	var kind reflect.Kind
	if !handled {
		kind = reflect.TypeOf(data).Kind()
	}

	if !handled && viper.GetBool("raw") && kind == reflect.String {
		handled = true
		dStr := data.(string)
		encoded = []byte(dStr)
		lexer = ""

		if len(dStr) != 0 && (dStr[0] == '{' || dStr[0] == '[') {
			// Looks like JSON to me!
			lexer = "json"
		}
	} else if !handled && viper.GetBool("raw") && kind == reflect.Slice {
		scalars := true

		for _, item := range data.([]interface{}) {
			switch item.(type) {
			case nil, bool, int, int64, float64, string:
				// The above are scalars used by decoders
			default:
				scalars = false
			}
		}

		if scalars {
			handled = true
			for _, item := range data.([]interface{}) {
				if item == nil {
					encoded = append(encoded, []byte("null\n")...)
				} else {
					encoded = append(encoded, []byte(fmt.Sprintf("%v\n", item))...)
				}
			}
		}
	}

	if !handled {
		switch viper.GetString("output-format") {
		case "yaml":
			encoded, err = yaml.Marshal(data)

			if err != nil {
				return err
			}

			lexer = "yaml"
		case "toon":
			encoded, err = toon.Marshal(data, toon.WithIndent(2))

			if err != nil {
				return err
			}

			lexer = ""
		default:
			encoded, err = json.MarshalIndent(data, "", "  ")

			if err != nil {
				return err
			}

			lexer = "json"
		}
	}

	// Make sure we end with a newline, otherwise things won't look right
	// in the terminal.
	if len(encoded) > 0 && encoded[len(encoded)-1] != '\n' {
		encoded = append(encoded, '\n')
	}

	// Only colorize if we are a TTY.
	if f.tty {
		if err = quick.Highlight(Stdout, string(encoded), lexer, "terminal256", "cli-dark"); err != nil {
			return err
		}
	} else {
		fmt.Fprint(Stdout, string(encoded))
	}

	return nil
}

func (f *DefaultFormatter) shouldRenderTable() bool {
	if !f.tty || viper.GetBool("raw") || viper.GetString("output-format") != "json" {
		return false
	}

	// --json is an explicit request for the serialized representation. An
	// explicitly supplied -o/--output-format should also win over the
	// interactive default, even when the value happens to be json.
	if viper.GetBool("json") {
		return false
	}
	if Root != nil {
		if flag := Root.PersistentFlags().Lookup("output-format"); flag != nil && flag.Changed {
			return false
		}
	}

	return true
}

// renderTable recognizes the common shapes returned by collection endpoints:
// either an array of objects or an object containing an items/data/results
// array. It returns false for scalar arrays and ordinary objects so callers
// can fall back to the requested serialization format.
func renderTable(data interface{}, requestedColumns []string) (bool, error) {
	rows, label, metadata, ok := tableRows(data, requestedColumns)
	if !ok {
		return false, nil
	}

	headers := append([]string(nil), requestedColumns...)
	if len(headers) == 0 {
		columns := make(map[string]bool)
		for _, row := range rows {
			for key := range row {
				columns[key] = true
			}
		}
		for key := range columns {
			headers = append(headers, key)
		}
		sort.Strings(headers)
	}
	if len(headers) == 0 {
		return false, nil
	}

	if label != "" {
		keys := make([]string, 0, len(metadata))
		for key, value := range metadata {
			if _, isRows := objectRowsValue(value, true); !isRows {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			value, err := tableValue(metadata[key])
			if err != nil {
				return false, err
			}
			fmt.Fprintf(Stdout, "%s: %s\n", key, value)
		}
		if len(keys) > 0 {
			fmt.Fprintln(Stdout)
		}
	}

	table := tablewriter.NewWriter(Stdout)
	table.SetHeader(headers)
	table.SetAutoWrapText(false)
	for _, row := range rows {
		values := make([]string, len(headers))
		for i, key := range headers {
			value, err := tableValue(row[key])
			if err != nil {
				return false, err
			}
			values[i] = value
		}
		table.Append(values)
	}
	table.Render()
	return true, nil
}

func tableRows(data interface{}, requestedColumns []string) ([]map[string]interface{}, string, map[string]interface{}, bool) {
	if rows, ok := objectRowsValue(data, len(requestedColumns) > 0); ok {
		return rows, "", nil, true
	}

	object, ok := data.(map[string]interface{})
	if !ok {
		return nil, "", nil, false
	}

	// Prefer conventional collection keys so an unrelated nested array does
	// not accidentally turn an ordinary object response into a table.
	keys := []string{"items", "data", "results", "records", "entries", "servers"}
	for _, key := range keys {
		if result, valid := objectRowsValue(object[key], len(requestedColumns) > 0); valid {
			if len(result) > 0 || len(requestedColumns) > 0 {
				return result, key, object, true
			}
		}
	}
	return nil, "", nil, false
}

func objectRowsValue(value interface{}, allowEmpty bool) ([]map[string]interface{}, bool) {
	if values, ok := value.([]interface{}); ok {
		return objectRows(values, allowEmpty)
	}
	if values, ok := value.([]map[string]interface{}); ok {
		return objectRowsFromMaps(values, allowEmpty)
	}
	return nil, false
}

func objectRows(values []interface{}, allowEmpty bool) ([]map[string]interface{}, bool) {
	rows := make([]map[string]interface{}, 0, len(values))
	for _, value := range values {
		row, ok := value.(map[string]interface{})
		if !ok {
			return nil, false
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 && !allowEmpty {
		return nil, false
	}
	return rows, true
}

func objectRowsFromMaps(values []map[string]interface{}, allowEmpty bool) ([]map[string]interface{}, bool) {
	if len(values) == 0 && !allowEmpty {
		return nil, false
	}
	return values, true
}

func tableValue(value interface{}) (string, error) {
	if value == nil {
		return "", nil
	}
	if stringValue, ok := value.(string); ok {
		return stringValue, nil
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}
