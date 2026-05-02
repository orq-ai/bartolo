package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"

	"github.com/orq-ai/bartolo/cli"
)

func deepAssign(target, source string) string {
	var targetMap map[string]interface{}
	if err := json.Unmarshal([]byte(target), &targetMap); err != nil {
		panic(err)
	}

	var sourceMap map[string]interface{}
	if err := json.Unmarshal([]byte(source), &sourceMap); err != nil {
		panic(err)
	}

	cli.DeepAssign(targetMap, sourceMap)

	marshalled, err := json.MarshalIndent(targetMap, "", "  ")
	if err != nil {
		panic(err)
	}

	return string(marshalled)
}

func TestDeepAssignMerge(t *testing.T) {
	target := `{
		"foo": {
			"bar": {
				"baz": 1
			}
		}
	}`

	source := `{
		"foo": {
			"bar": {
				"blarg": true
			}
		}
	}`

	expected := `{
		"foo": {
			"bar": {
				"baz": 1,
				"blarg": true
			}
		}
	}`

	result := deepAssign(target, source)

	assert.JSONEq(t, expected, result)
}

func TestDeepAssignOverwrite(t *testing.T) {
	target := `{
		"foo": {
			"bar": {
				"baz": 1
			}
		}
	}`

	source := `{
		"foo": [1, 2, 3]
	}`

	expected := `{
		"foo": [1, 2, 3]
	}`

	result := deepAssign(target, source)

	assert.JSONEq(t, expected, result)
}

func TestGetBodyUsesGeneratedExample(t *testing.T) {
	params := viper.New()
	params.Set("example", true)

	body, err := cli.GetBody("application/json", nil, params, []string{"hello: world"})
	if err != nil {
		t.Fatalf("GetBody with example: %v", err)
	}

	assert.JSONEq(t, `{"hello":"world"}`, body)
}

func TestGetBodyMergesFileAndShorthand(t *testing.T) {
	dir := t.TempDir()
	filename := filepath.Join(dir, "body.json")
	if err := os.WriteFile(filename, []byte(`{"hello":"world"}`), 0600); err != nil {
		t.Fatalf("write body file: %v", err)
	}

	params := viper.New()
	params.Set("from-file", filename)

	body, err := cli.GetBody("application/json", []string{"count:", "2"}, params, nil)
	if err != nil {
		t.Fatalf("GetBody with file: %v", err)
	}

	assert.JSONEq(t, `{"hello":"world","count":2}`, body)
}

func TestGetBodyRejectsExplicitStdinWithoutPipe(t *testing.T) {
	params := viper.New()
	params.Set("stdin", true)

	if _, err := cli.GetBody("application/json", nil, params, nil); err == nil {
		t.Fatal("expected stdin error")
	}
}

func applyBody(t *testing.T, fields []cli.BodyField, sets map[string][]string, base string) string {
	t.Helper()

	cmd := &cobra.Command{Use: "test"}
	cli.AddBodyFieldFlags(cmd, fields)

	for name, values := range sets {
		for _, value := range values {
			if err := cmd.Flags().Set(name, value); err != nil {
				t.Fatalf("set --%s=%s: %v", name, value, err)
			}
		}
	}

	params := viper.New()
	if err := params.BindPFlags(cmd.Flags()); err != nil {
		t.Fatalf("bind flags: %v", err)
	}

	body, err := cli.ApplyBodyFlags(cmd, params, "application/json", base, fields)
	if err != nil {
		t.Fatalf("ApplyBodyFlags: %v", err)
	}
	return body
}

func TestApplyBodyFlagsNullableScalars(t *testing.T) {
	fields := []cli.BodyField{
		{Name: "display_name", FlagName: "display-name", Type: "string-nullable"},
		{Name: "count", FlagName: "count", Type: "int64-nullable"},
	}

	body := applyBody(t, fields, map[string][]string{
		"display-name": {"null"},
		"count":        {"7"},
	}, ``)

	assert.JSONEq(t, `{"display_name":null,"count":7}`, body)
}

func TestApplyBodyFlagsRepeatableSlices(t *testing.T) {
	fields := []cli.BodyField{
		{Name: "tags", FlagName: "tag", Type: "string-slice"},
		{Name: "scores", FlagName: "score", Type: "int64-slice"},
	}

	body := applyBody(t, fields, map[string][]string{
		"tag":   {"alpha", "beta"},
		"score": {"1", "2", "3"},
	}, ``)

	assert.JSONEq(t, `{"tags":["alpha","beta"],"scores":[1,2,3]}`, body)
}

func TestApplyBodyFlagsStringMap(t *testing.T) {
	fields := []cli.BodyField{
		{Name: "metadata", FlagName: "metadata", Type: "string-map"},
	}

	body := applyBody(t, fields, map[string][]string{
		"metadata": {"region=eu", "tier=gold"},
	}, ``)

	assert.JSONEq(t, `{"metadata":{"region":"eu","tier":"gold"}}`, body)
}

func TestApplyBodyFlagsJSONFallback(t *testing.T) {
	fields := []cli.BodyField{
		{Name: "documents", FlagName: "documents", Type: "json"},
		{Name: "invoke_options", FlagName: "invoke-options", Type: "json"},
	}

	body := applyBody(t, fields, map[string][]string{
		"documents":      {`[{"id":"a"},{"id":"b"}]`},
		"invoke-options": {`{"timeout":30}`},
	}, ``)

	assert.JSONEq(t, `{"documents":[{"id":"a"},{"id":"b"}],"invoke_options":{"timeout":30}}`, body)
}

func TestApplyBodyFlagsJSONRejectsInvalid(t *testing.T) {
	fields := []cli.BodyField{
		{Name: "documents", FlagName: "documents", Type: "json"},
	}

	cmd := &cobra.Command{Use: "test"}
	cli.AddBodyFieldFlags(cmd, fields)
	if err := cmd.Flags().Set("documents", "not json"); err != nil {
		t.Fatalf("set documents: %v", err)
	}
	params := viper.New()
	if err := params.BindPFlags(cmd.Flags()); err != nil {
		t.Fatalf("bind flags: %v", err)
	}

	if _, err := cli.ApplyBodyFlags(cmd, params, "application/json", ``, fields); err == nil {
		t.Fatal("expected JSON parse error")
	}
}

func TestApplyBodyFlagsEnumStringRejectsInvalid(t *testing.T) {
	fields := []cli.BodyField{
		{Name: "color", FlagName: "color", Type: "enum-string", Enum: []string{"red", "green", "blue"}},
	}

	cmd := &cobra.Command{Use: "test"}
	cli.AddBodyFieldFlags(cmd, fields)
	if err := cmd.Flags().Set("color", "purple"); err != nil {
		t.Fatalf("set color: %v", err)
	}
	params := viper.New()
	if err := params.BindPFlags(cmd.Flags()); err != nil {
		t.Fatalf("bind flags: %v", err)
	}

	_, err := cli.ApplyBodyFlags(cmd, params, "application/json", ``, fields)
	if err == nil {
		t.Fatal("expected enum validation error")
	}
}

func TestApplyBodyFlagsOverridesStructuredBody(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	cli.AddBodyFieldFlags(cmd, []cli.BodyField{
		{
			Name:        "instructions",
			FlagName:    "instructions",
			Type:        "string",
			Description: "Agent instructions",
		},
	})

	if err := cmd.Flags().Set("instructions", "updated"); err != nil {
		t.Fatalf("set flag: %v", err)
	}

	params := viper.New()
	if err := params.BindPFlags(cmd.Flags()); err != nil {
		t.Fatalf("bind flags: %v", err)
	}

	body, err := cli.ApplyBodyFlags(cmd, params, "application/json", `{"instructions":"original"}`, []cli.BodyField{
		{
			Name:     "instructions",
			FlagName: "instructions",
			Type:     "string",
		},
	})
	if err != nil {
		t.Fatalf("ApplyBodyFlags: %v", err)
	}

	assert.JSONEq(t, `{"instructions":"updated"}`, body)
}
