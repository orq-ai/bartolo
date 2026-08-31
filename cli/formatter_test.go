package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
)

func TestDefaultFormatterHandlesNilQueryResult(t *testing.T) {
	Init(&Config{
		AppName: "test",
	})

	viper.Set("jmespath", "missing")
	defer viper.Set("query", "")

	out := new(bytes.Buffer)
	Stdout = out
	defer func() {
		Stdout = os.Stdout
	}()

	formatter := NewDefaultFormatter(false)
	err := formatter.Format(map[string]interface{}{"hello": "world"})
	assert.NoError(t, err)
	assert.Equal(t, "null\n", out.String())
}

func TestDefaultFormatterRendersListAsTableOnTTY(t *testing.T) {
	viper.Reset()
	viper.Set("output-format", tableFormat)
	viper.Set("jmespath", "")
	viper.Set("raw", false)
	out := new(bytes.Buffer)
	original := Stdout
	Stdout = out
	t.Cleanup(func() { Stdout = original })

	err := NewDefaultFormatter(true).FormatList([]interface{}{
		map[string]interface{}{"id": "one", "active": true},
		map[string]interface{}{"id": "two", "active": false},
	}, "id", "active")
	assert.NoError(t, err)
	assert.Contains(t, out.String(), "│ ID  │ ACTIVE │")
	assert.Contains(t, out.String(), "│ one │ true   │")
	assert.Contains(t, out.String(), "│ two │ false  │")
}

func TestDefaultFormatterKeepsListJSONWhenNotInteractive(t *testing.T) {
	viper.Reset()
	viper.Set("output-format", tableFormat)
	viper.Set("jmespath", "")
	viper.Set("raw", false)
	out := new(bytes.Buffer)
	original := Stdout
	Stdout = out
	t.Cleanup(func() { Stdout = original })

	err := NewDefaultFormatter(false).FormatList([]interface{}{
		map[string]interface{}{"id": "one"},
	})
	assert.NoError(t, err)
	assert.JSONEq(t, `[{"id":"one"}]`, out.String())
}

func TestDefaultFormatterRendersListEnvelopeMetadata(t *testing.T) {
	viper.Reset()
	viper.Set("output-format", tableFormat)
	viper.Set("jmespath", "")
	viper.Set("raw", false)
	out := new(bytes.Buffer)
	original := Stdout
	Stdout = out
	t.Cleanup(func() { Stdout = original })

	err := NewDefaultFormatter(true).FormatList(map[string]interface{}{
		"selected_server": "https://example.test",
		"servers": []map[string]interface{}{
			{"url": "https://example.test", "selected": true},
		},
	})
	assert.NoError(t, err)
	assert.Contains(t, out.String(), "selected_server: https://example.test")
	assert.Contains(t, out.String(), "│ SELECTED │         URL          │")
}

func TestDefaultFormatterRendersEmptyListWithConfiguredColumns(t *testing.T) {
	viper.Reset()
	viper.Set("output-format", tableFormat)
	viper.Set("jmespath", "")
	viper.Set("raw", false)
	out := new(bytes.Buffer)
	original := Stdout
	Stdout = out
	t.Cleanup(func() { Stdout = original })

	err := NewDefaultFormatter(true).FormatList([]interface{}{}, "id", "name")
	assert.NoError(t, err)
	assert.Contains(t, out.String(), "│ ID │ NAME │")
}

func TestDefaultFormatterSkipsNestedColumnsAndTruncatesCells(t *testing.T) {
	viper.Reset()
	viper.Set("output-format", tableFormat)
	viper.Set("jmespath", "")
	viper.Set("raw", false)
	out := new(bytes.Buffer)
	original := Stdout
	Stdout = out
	t.Cleanup(func() { Stdout = original })

	err := NewDefaultFormatter(true).FormatList([]interface{}{
		map[string]interface{}{
			"id":       "one",
			"prompt":   map[string]interface{}{"messages": []interface{}{"long"}},
			"metadata": []interface{}{"a", "b"},
			"note":     strings.Repeat("x", 100),
		},
	})
	assert.NoError(t, err)
	assert.Contains(t, out.String(), "│ ID  │")
	assert.NotContains(t, out.String(), "PROMPT")
	assert.NotContains(t, out.String(), "METADATA")
	assert.Contains(t, out.String(), strings.Repeat("x", maxCellWidth-1)+"…")
	assert.NotContains(t, out.String(), strings.Repeat("x", maxCellWidth+1))
}

func TestDefaultFormatterRendersResourceNamedWrapper(t *testing.T) {
	viper.Reset()
	viper.Set("output-format", tableFormat)
	viper.Set("jmespath", "")
	viper.Set("raw", false)
	out := new(bytes.Buffer)
	original := Stdout
	Stdout = out
	t.Cleanup(func() { Stdout = original })

	err := NewDefaultFormatter(true).FormatList(map[string]interface{}{
		"schedules": []interface{}{map[string]interface{}{"id": "one"}},
	})
	assert.NoError(t, err)
	assert.Contains(t, out.String(), "│ ID  │")
	assert.Contains(t, out.String(), "│ one │")
}

func TestDefaultFormatterKeepsAmbiguousObjectAsJSON(t *testing.T) {
	viper.Reset()
	viper.Set("output-format", tableFormat)
	viper.Set("jmespath", "")
	viper.Set("raw", false)
	out := new(bytes.Buffer)
	original := Stdout
	Stdout = out
	t.Cleanup(func() { Stdout = original })

	err := NewDefaultFormatter(true).FormatList(map[string]interface{}{
		"input":  []interface{}{map[string]interface{}{"id": "one"}},
		"output": []interface{}{map[string]interface{}{"id": "two"}},
	})
	assert.NoError(t, err)
	assert.Contains(t, out.String(), `"input"`)
	assert.NotContains(t, out.String(), "│ ID │")
}

func TestDefaultFormatterReportsEmptyCollection(t *testing.T) {
	viper.Reset()
	viper.Set("output-format", tableFormat)
	viper.Set("jmespath", "")
	viper.Set("raw", false)
	out := new(bytes.Buffer)
	original := Stdout
	Stdout = out
	t.Cleanup(func() { Stdout = original })

	err := NewDefaultFormatter(true).FormatList(map[string]interface{}{
		"data":     []interface{}{},
		"has_more": false,
	})
	assert.NoError(t, err)
	assert.Equal(t, "No results.\n", out.String())
}

func TestFitColumnsDropsTrailingColumns(t *testing.T) {
	headers := []string{"id", "name", "description"}
	values := [][]string{{"one", "first", "a long description"}}

	headers, values = fitColumns(headers, values, 20)
	assert.Equal(t, []string{"id", "name"}, headers)
	assert.Equal(t, [][]string{{"one", "first"}}, values)
}

func TestDefaultFormatterSummarizesEnvelopeInFooter(t *testing.T) {
	viper.Reset()
	viper.Set("output-format", tableFormat)
	viper.Set("jmespath", "")
	viper.Set("raw", false)
	out := new(bytes.Buffer)
	original := Stdout
	Stdout = out
	t.Cleanup(func() { Stdout = original })

	err := NewDefaultFormatter(true).FormatList(map[string]interface{}{
		"object":   "list",
		"has_more": true,
		"data":     []interface{}{map[string]interface{}{"id": "one"}},
	})
	assert.NoError(t, err)
	assert.NotContains(t, out.String(), "object")
	assert.Contains(t, out.String(), "1 shown, more available")
}

func TestDefaultFormatterFooterCountsAgainstTotal(t *testing.T) {
	viper.Reset()
	viper.Set("output-format", tableFormat)
	viper.Set("jmespath", "")
	viper.Set("raw", false)
	out := new(bytes.Buffer)
	original := Stdout
	Stdout = out
	t.Cleanup(func() { Stdout = original })

	err := NewDefaultFormatter(true).FormatList(map[string]interface{}{
		"data":     []interface{}{map[string]interface{}{"id": "one"}},
		"total":    float64(47),
		"limit":    float64(1),
		"offset":   float64(0),
		"has_more": true,
		"object":   "list",
	})
	assert.NoError(t, err)
	assert.Contains(t, out.String(), "1 of 47")
	assert.NotContains(t, out.String(), "limit")
	assert.NotContains(t, out.String(), "offset")
}

func TestTableFooterKeepsUnrecognizedEnvelopeFields(t *testing.T) {
	footer, err := tableFooter(2, "evaluators", map[string]interface{}{
		"enabled": true,
		"id":      "pol_1",
		"name":    "PII guard",
	})
	assert.NoError(t, err)
	assert.Equal(t, "enabled: true · id: pol_1 · name: PII guard", footer)
}

func TestDefaultFormatterSerializesTableFormatWhenNotATerminal(t *testing.T) {
	viper.Reset()
	viper.Set("output-format", tableFormat)
	viper.Set("jmespath", "")
	viper.Set("raw", false)
	originalSerialization := defaultSerialization
	defaultSerialization = "toon"
	t.Cleanup(func() { defaultSerialization = originalSerialization })

	out := new(bytes.Buffer)
	original := Stdout
	Stdout = out
	t.Cleanup(func() { Stdout = original })

	err := NewDefaultFormatter(false).FormatList([]interface{}{
		map[string]interface{}{"id": "one"},
	})
	assert.NoError(t, err)
	assert.NotContains(t, out.String(), "│")
	assert.Contains(t, out.String(), "id")
}

// Forced color makes tty true on a pipe, which must not be enough to turn a
// redirect into a table.
func TestDefaultFormatterSerializesForColorForcedOntoAPipe(t *testing.T) {
	viper.Reset()
	viper.Set("output-format", tableFormat)
	viper.Set("jmespath", "")
	viper.Set("raw", false)

	out := new(bytes.Buffer)
	original := Stdout
	Stdout = out
	t.Cleanup(func() { Stdout = original })

	formatter := &DefaultFormatter{tty: true, terminal: false}
	err := formatter.FormatList([]interface{}{map[string]interface{}{"id": "one"}})
	assert.NoError(t, err)
	assert.NotContains(t, out.String(), "│")
}

func TestDefaultFormatterKeepsExplicitFormatOnATerminal(t *testing.T) {
	for _, format := range []string{"json", "yaml", "toon"} {
		t.Run(format, func(t *testing.T) {
			viper.Reset()
			viper.Set("output-format", format)
			viper.Set("jmespath", "")
			viper.Set("raw", false)

			out := new(bytes.Buffer)
			original := Stdout
			Stdout = out
			t.Cleanup(func() { Stdout = original })

			err := NewDefaultFormatter(true).FormatList([]interface{}{
				map[string]interface{}{"id": "one"},
			})
			assert.NoError(t, err)
			assert.NotContains(t, out.String(), "│")
		})
	}
}

func TestDefaultFormatterRawWinsOverTable(t *testing.T) {
	viper.Reset()
	viper.Set("output-format", tableFormat)
	viper.Set("jmespath", "[].id")
	viper.Set("raw", true)

	out := new(bytes.Buffer)
	original := Stdout
	Stdout = out
	t.Cleanup(func() { Stdout = original })

	err := NewDefaultFormatter(true).FormatList([]interface{}{
		map[string]interface{}{"id": "one"},
	})
	assert.NoError(t, err)
	assert.Equal(t, "one\n", out.String())
}

func TestDefaultFormatterColumnsFlagSelectsColumns(t *testing.T) {
	viper.Reset()
	viper.Set("output-format", tableFormat)
	viper.Set("jmespath", "")
	viper.Set("raw", false)
	viper.Set("columns", " name , id ")

	out := new(bytes.Buffer)
	original := Stdout
	Stdout = out
	t.Cleanup(func() { Stdout = original })

	err := NewDefaultFormatter(true).FormatList([]interface{}{
		map[string]interface{}{"id": "one", "name": "first", "note": "hidden"},
	}, "id")
	assert.NoError(t, err)
	assert.Contains(t, out.String(), "│ NAME  │ ID  │")
	assert.NotContains(t, out.String(), "NOTE")
}
