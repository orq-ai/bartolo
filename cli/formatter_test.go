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

	viper.Set("query", "missing")
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
