package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/alecthomas/chroma"
	"github.com/alecthomas/chroma/quick"
	"github.com/alecthomas/chroma/styles"
	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/tw"
	"github.com/orq-ai/bartolo/internal/jmespathx"
	"github.com/spf13/viper"
	toon "github.com/toon-format/toon-go"
	"golang.org/x/term"
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

const (
	// maxCellWidth keeps one long value from pushing other columns off screen.
	maxCellWidth = 40

	// defaultTableWidth is used when the terminal size is unavailable.
	defaultTableWidth = 120
)

// ResponseFormatter will filter, prettify, and print out the results of a call.
type ResponseFormatter interface {
	Format(interface{}) error
}

// FormatList renders a collection through the formatter's collection-aware
// output, falling back to Format for custom ResponseFormatter implementations.
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

// FormatList renders a collection as a table for a human at a terminal and
// keeps the serialized output for pipes and explicit format flags.
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

	// An explicit --json or -o wins over the interactive table default.
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

// renderTable reports false for anything that is not a collection so callers
// can fall back to the requested serialization format.
func renderTable(data interface{}, requestedColumns []string) (bool, error) {
	rows, label, metadata, ok := tableRows(data, requestedColumns)
	if !ok {
		return false, nil
	}

	headers := append([]string(nil), requestedColumns...)
	if len(headers) == 0 {
		headers = autoColumns(rows)
	}
	if len(headers) == 0 && len(rows) > 0 {
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

	if len(rows) == 0 && len(headers) == 0 {
		fmt.Fprintln(Stdout, "No results.")
		return true, nil
	}

	values := make([][]string, 0, len(rows))
	for _, row := range rows {
		cells := make([]string, len(headers))
		for i, key := range headers {
			value, err := tableValue(row[key])
			if err != nil {
				return false, err
			}
			cells[i] = truncateCell(value)
		}
		values = append(values, cells)
	}

	// Columns the schema asked for are never dropped.
	if len(requestedColumns) == 0 {
		headers, values = fitColumns(headers, values, terminalWidth())
	}

	table := tablewriter.NewTable(Stdout,
		tablewriter.WithHeaderAutoWrap(tw.WrapNone),
		tablewriter.WithRowAutoWrap(tw.WrapNone),
	)
	table.Header(headers)
	for _, cells := range values {
		if err := table.Append(cells); err != nil {
			return false, err
		}
	}
	if err := table.Render(); err != nil {
		return false, err
	}
	return true, nil
}

// autoColumns picks columns for a collection without x-cli-list-fields:
// scalar values only, recognizable identifiers first.
func autoColumns(rows []map[string]interface{}) []string {
	scalar := make(map[string]bool)
	for _, row := range rows {
		for key, value := range row {
			switch value.(type) {
			case nil, map[string]interface{}, []interface{}, []map[string]interface{}:
			default:
				scalar[key] = true
			}
		}
	}

	headers := make([]string, 0, len(scalar))
	for _, key := range []string{"id", "key", "name", "display_name", "title", "slug", "type", "status", "state", "model", "created", "created_at", "updated", "updated_at"} {
		if scalar[key] {
			headers = append(headers, key)
			delete(scalar, key)
		}
	}

	rest := make([]string, 0, len(scalar))
	for key := range scalar {
		rest = append(rest, key)
	}
	sort.Strings(rest)
	return append(headers, rest...)
}

// fitColumns drops trailing columns that do not fit, always keeping the first.
func fitColumns(headers []string, values [][]string, width int) ([]string, [][]string) {
	total := 1
	keep := 0
	for i, header := range headers {
		column := len([]rune(header))
		for _, cells := range values {
			if size := len([]rune(cells[i])); size > column {
				column = size
			}
		}

		total += column + 3 // tablewriter pads each cell and draws a separator.
		if i > 0 && total > width {
			break
		}
		keep = i + 1
	}

	if keep == len(headers) {
		return headers, values
	}
	for i, cells := range values {
		values[i] = cells[:keep]
	}
	return headers[:keep], values
}

func terminalWidth() int {
	if width, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && width > 0 {
		return width
	}
	return defaultTableWidth
}

func truncateCell(value string) string {
	value = strings.ReplaceAll(value, "\n", " ")
	runes := []rune(value)
	if len(runes) <= maxCellWidth {
		return value
	}
	return string(runes[:maxCellWidth-1]) + "…"
}

func tableRows(data interface{}, requestedColumns []string) ([]map[string]interface{}, string, map[string]interface{}, bool) {
	if rows, ok := objectRowsValue(data, len(requestedColumns) > 0); ok {
		return rows, "", nil, true
	}

	object, ok := data.(map[string]interface{})
	if !ok {
		return nil, "", nil, false
	}

	// Conventional keys first, so a stray nested array is not mistaken for one.
	keys := []string{"items", "data", "results", "records", "entries", "servers"}
	for _, key := range keys {
		if result, valid := objectRowsValue(object[key], true); valid {
			return result, key, object, true
		}
	}

	// Otherwise accept a wrapper named after the resource, such as `schedules`,
	// as long as there is exactly one array of objects to choose from.
	found := ""
	var rows []map[string]interface{}
	for key, value := range object {
		result, valid := objectRowsValue(value, false)
		if !valid {
			continue
		}
		if found != "" {
			// Ambiguous: leave it to the serialized output.
			return nil, "", nil, false
		}
		found, rows = key, result
	}
	if found != "" {
		return rows, found, object, true
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
