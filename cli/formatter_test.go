package cli

import (
	"bytes"
	"os"
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
	viper.Set("output-format", "json")
	viper.Set("jmespath", "")
	viper.Set("raw", false)
	originalRoot := Root
	Root = nil
	t.Cleanup(func() { Root = originalRoot })

	out := new(bytes.Buffer)
	original := Stdout
	Stdout = out
	t.Cleanup(func() { Stdout = original })

	err := NewDefaultFormatter(true).FormatList([]interface{}{
		map[string]interface{}{"id": "one", "active": true},
		map[string]interface{}{"id": "two", "active": false},
	}, "id", "active")
	assert.NoError(t, err)
	assert.Contains(t, out.String(), "| ID  | ACTIVE |")
	assert.Contains(t, out.String(), "| one | true   |")
	assert.Contains(t, out.String(), "| two | false  |")
}

func TestDefaultFormatterKeepsListJSONWhenNotInteractive(t *testing.T) {
	viper.Reset()
	viper.Set("output-format", "json")
	viper.Set("jmespath", "")
	viper.Set("raw", false)
	originalRoot := Root
	Root = nil
	t.Cleanup(func() { Root = originalRoot })

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
	viper.Set("output-format", "json")
	viper.Set("jmespath", "")
	viper.Set("raw", false)
	originalRoot := Root
	Root = nil
	t.Cleanup(func() { Root = originalRoot })

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
	assert.Contains(t, out.String(), "| SELECTED |         URL          |")
}

func TestDefaultFormatterRendersEmptyListWithConfiguredColumns(t *testing.T) {
	viper.Reset()
	viper.Set("output-format", "json")
	viper.Set("jmespath", "")
	viper.Set("raw", false)
	originalRoot := Root
	Root = nil
	t.Cleanup(func() { Root = originalRoot })

	out := new(bytes.Buffer)
	original := Stdout
	Stdout = out
	t.Cleanup(func() { Stdout = original })

	err := NewDefaultFormatter(true).FormatList([]interface{}{}, "id", "name")
	assert.NoError(t, err)
	assert.Contains(t, out.String(), "| ID | NAME |")
}
