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

// DefaultFormatter can apply JMESPath queries and can render a table or output
// prettyfied JSON, YAML, or TOON. If Stdout is a TTY, colorized output is provided.
type DefaultFormatter struct {
	// tty and terminal differ when color is forced onto a pipe.
	tty      bool
	terminal bool
}

// NewDefaultFormatter creates a new formatter. tty enables colorized output;
// terminal reports whether stdout really is a terminal. They are separate
// arguments because forcing color on makes the first true on a pipe, and a pipe
// must never render a table.
func NewDefaultFormatter(tty, terminal bool) *DefaultFormatter {
	return &DefaultFormatter{
		tty:      tty,
		terminal: terminal,
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

	override := ""
	if list {
		override = viper.GetString("columns")
	}

	if list && f.shouldRenderTable() {
		userColumns := false
		if override != "" {
			selected, err := splitColumns(override)
			if err != nil {
				return err
			}

			columns, userColumns = selected, true
		}

		if rendered, err := renderTable(data, columns, userColumns); err != nil {
			return err
		} else if rendered {
			return nil
		}

		// Falling back silently looks exactly like the bug this path exists to fix.
		fmt.Fprintf(Stderr, "Not shown as a table: this response is not a recognizable collection. Showing %s instead.\n", configuredSerialization)
	} else if override != "" {
		fmt.Fprintf(Stderr, "--columns was ignored: this output is not a table. Showing %s instead.\n", serializationFormat())
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
		switch serializationFormat() {
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

// tableFormat renders a list command as a table for a human at a terminal.
// tableFallbackFormat is what it serializes to everywhere else when the CLI
// configured nothing: a piped or redirected run, a non-list command, and a
// payload that is not a collection. It is JSON because whatever reads a pipe
// is far more likely to be `jq` than a model; a CLI that knows otherwise says
// so with Config.SerializationFormat.
const (
	tableFormat         = "table"
	tableFallbackFormat = "json"
)

// serializationFormat is the format to encode with. `table` is a rendering
// choice rather than a serialization, so the CLI's configured one stands in.
func serializationFormat() string {
	if format := OutputFormat(); format != tableFormat {
		return format
	}

	return configuredSerialization
}

// serializationOrDefault resolves Config.SerializationFormat. `table` cannot be
// what a table falls back to, so it is rejected like any unknown value.
func serializationOrDefault(value string) string {
	if format, ok := parseOutputFormat(value); ok && format != tableFormat {
		return format
	}

	return tableFallbackFormat
}

// splitColumns parses the `--columns` value. A JMESPath projection is the tool
// for reshaping data; this only picks which of the existing fields to show.
func splitColumns(value string) ([]string, error) {
	columns := make([]string, 0, 1)
	for _, column := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(column); trimmed != "" {
			columns = append(columns, trimmed)
		}
	}

	if len(columns) == 0 {
		return nil, NewValueError(fmt.Errorf("--columns: %q names no columns", value))
	}

	return columns, nil
}

func (f *DefaultFormatter) shouldRenderTable() bool {
	return f.terminal && !viper.GetBool("raw") && OutputFormat() == tableFormat
}

// checkColumns rejects a column no row has. A misspelled name would otherwise
// render as a blank column, which reads as missing data rather than a typo.
func checkColumns(requestedColumns []string, rows []map[string]interface{}) error {
	if len(requestedColumns) == 0 || len(rows) == 0 {
		return nil
	}

	for _, column := range requestedColumns {
		found := false
		for _, row := range rows {
			if _, ok := row[column]; ok {
				found = true
				break
			}
		}

		if !found {
			return NewValueError(fmt.Errorf("--columns: %q is not a field of the returned items", column))
		}
	}

	return nil
}

// renderTable reports false for anything that is not a collection so callers
// can fall back to the requested serialization format.
func renderTable(data interface{}, requestedColumns []string, userColumns bool) (bool, error) {
	rows, label, metadata, ok := tableRows(data, requestedColumns)
	if !ok {
		return false, nil
	}

	// Only a name someone typed can be a typo; the spec's own field list cannot.
	if userColumns {
		if err := checkColumns(requestedColumns, rows); err != nil {
			return false, err
		}
	}

	headers := append([]string(nil), requestedColumns...)
	if len(headers) == 0 {
		headers = autoColumns(rows)
	}
	if len(headers) == 0 && len(rows) > 0 {
		return false, nil
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

	// Columns someone asked for — by spec or on the command line — are never dropped.
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

	footer, err := tableFooter(len(rows), label, metadata)
	if err != nil {
		return false, err
	}
	if footer != "" {
		fmt.Fprintln(Stdout, footer)
	}
	return true, nil
}

// tableFooter summarizes the envelope in one line below the table: how many
// rows are shown out of how many exist, then whatever else the envelope holds.
// Pagination plumbing is for scripts reading `-o json`, not for a reader looking
// at a table.
func tableFooter(shown int, label string, metadata map[string]interface{}) (string, error) {
	if label == "" {
		return "", nil
	}

	parts := make([]string, 0, len(metadata))
	if total, ok := metadataInt(metadata, "total", "total_count"); ok {
		parts = append(parts, fmt.Sprintf("%d of %d", shown, total))
	} else if more, ok := metadata["has_more"].(bool); ok && more {
		parts = append(parts, fmt.Sprintf("%d shown, more available", shown))
	}

	keys := make([]string, 0, len(metadata))
	for key, value := range metadata {
		if _, isRows := objectRowsValue(value, true); isRows || isEnvelopePlumbing(key, value) {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value, err := tableValue(metadata[key])
		if err != nil {
			return "", err
		}
		parts = append(parts, fmt.Sprintf("%s: %s", key, truncateCell(value)))
	}
	return strings.Join(parts, " · "), nil
}

func metadataInt(metadata map[string]interface{}, keys ...string) (int, bool) {
	for _, key := range keys {
		switch value := metadata[key].(type) {
		case float64:
			return int(value), true
		case int:
			return value, true
		}
	}
	return 0, false
}

// isEnvelopePlumbing reports whether an envelope field is paging bookkeeping or
// only restates that the response is a collection, such as Stripe-style
// `"object": "list"`.
func isEnvelopePlumbing(key string, value interface{}) bool {
	switch key {
	case "object", "kind", "type":
		return value == "list" || value == "collection"
	case "has_more", "total", "total_count", "count", "limit", "offset", "page",
		"per_page", "cursor", "next_cursor", "prev_cursor", "next", "previous",
		"starting_after", "ending_before":
		return true
	}
	return false
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

// A var so a test can render at a width it chose rather than whatever the
// process happens to be attached to.
var terminalWidth = func() int {
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
