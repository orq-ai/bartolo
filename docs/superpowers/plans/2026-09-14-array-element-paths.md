# Array Element Paths Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let `x-cli-list-fields` and `--columns` project a named value from every object in an array with selectors such as `settings.tools[].key`.

**Architecture:** Extend the shared runtime `tableField` resolver with one narrowly parsed `[]` projection while preserving its exact-key-first and dotted-object behavior. Return the projection as the existing `[]interface{}` representation so list abbreviation, truncation, headers, validation, and output-mode boundaries remain owned by current formatter code. The generator continues carrying opaque selector strings verbatim; tests pin that handoff and exercise the resulting generated CLI.

**Tech Stack:** Go 1.25+, Cobra/Viper, tablewriter, testify, Bartolo's OpenAPI generator and generated-CLI integration harness.

> **Status:** implemented. Review rounds after this plan was written changed
> four behaviors; the design spec is authoritative wherever the two differ.
> 1. A present-but-null projected value proves the path exists (the column
>    validates and renders blank) instead of being dropped like a missing key.
> 2. A `null` array behaves exactly like an empty one.
> 3. An unsupported selector still resolves as an ordinary property name at any
>    depth, and when nothing resolves the error additionally names the grammar
>    rule. A non-array prefix is reported as such.
> 4. Header rendering moved into `tableHeader` with
>    `tablewriter.WithHeaderAutoFormat(tw.Off)`, which this plan does not
>    describe. The embedded resolver code in Task 1 Step 3 is superseded by
>    `cli/formatter.go`.

## Global Constraints

- Both `x-cli-list-fields` and `--columns` accept `settings.tools[].key` with identical resolution and rendering.
- A selector supports exactly one `[]`, appended to a non-empty object-path segment, and requires a non-empty dotted object path after it.
- Object paths may have arbitrary depth before and after `[]`; source array order is preserved.
- Non-object array elements and elements lacking the projected child are omitted; a present-but-null child renders nothing but proves the path.
- An empty or null source array renders as an empty list and is indeterminate for suffix validation; a non-empty array in which no element has the projected path is unresolved and prevents an empty row from masking a typo.
- The complete literal top-level key wins before path syntax is interpreted.
- `[0]`, `[1]`, `[*]`, and `[ ]` are ordinary property-name text, not selectors.
- Projected values reuse the existing three-entry limit, `…` fourth marker, and 40-rune cell truncation.
- Existing any-row column validation and its `--columns` versus `declared column` error prefixes remain unchanged.
- Headers retain the supplied selector; automatic columns, JMESPath, list roots, list classification, terminal fitting, and non-table output remain unchanged.
- Do not add count expressions, indexed access, multiple array projections, filters, slices, wildcard projections, schema-time selector validation, or new dependencies.
- Do not edit `CHANGELOG.md`; release notes come from the pull request.

---

### Task 1: Resolve and render one array projection

**Files:**
- Modify: `cli/formatter.go:304-358`
- Test: `cli/formatter_test.go:159-300`

**Interfaces:**
- Consumes: normalized table rows containing `map[string]interface{}` objects and `[]interface{}` arrays; existing `checkColumns(requestedColumns, rows, userColumns)` and `tableValue(interface{})` behavior.
- Produces: `tableField(row map[string]interface{}, column string) (interface{}, bool)` resolves a plain dotted path or one `[]` projection and returns projected values as `[]interface{}`; `objectField(row map[string]interface{}, column string) (interface{}, bool)` owns exact and dotted object traversal.

- [x] **Step 1: Write failing resolver and rendering tests**

Add these tests beside the current nested-column tests in `cli/formatter_test.go`:

```go
func TestTableFieldProjectsArrayElementPaths(t *testing.T) {
	row := map[string]interface{}{
		"settings": map[string]interface{}{
			"tools": []interface{}{
				map[string]interface{}{
					"key": "lookup-order",
					"configuration": map[string]interface{}{"name": "orders"},
				},
				"not-an-object",
				map[string]interface{}{"id": "missing-key"},
				map[string]interface{}{"key": nil},
				map[string]interface{}{"key": "issue-refund"},
			},
			"tools[0]": map[string]interface{}{"key": "zero"},
			"tools[1]": map[string]interface{}{"key": "one"},
			"tools[*]": map[string]interface{}{"key": "wildcard"},
			"tools[ ]": map[string]interface{}{"key": "space"},
		},
		"empty": []interface{}{},
		"groups": []interface{}{
			map[string]interface{}{
				"tools": []interface{}{map[string]interface{}{"key": "nested"}},
			},
		},
	}

	tests := []struct {
		name   string
		column string
		want   interface{}
		found  bool
	}{
		{
			name:   "projects present values and omits unusable elements",
			column: "settings.tools[].key",
			want:   []interface{}{"lookup-order", "issue-refund"},
			found:  true,
		},
		{
			name:   "walks a dotted suffix",
			column: "settings.tools[].configuration.name",
			want:   []interface{}{"orders"},
			found:  true,
		},
		{
			name:   "empty array is resolved",
			column: "empty[].key",
			want:   []interface{}{},
			found:  true,
		},
		{
			name:   "non-empty projection with no values is unresolved",
			column: "settings.tools[].missing",
			want:   nil,
			found:  false,
		},
		{
			name:   "terminal projection is unsupported",
			column: "settings.tools[]",
			want:   nil,
			found:  false,
		},
		{
			name:   "multiple projections are unsupported",
			column: "groups[].tools[].key",
			want:   nil,
			found:  false,
		},
		{
			name:   "index syntax stays an ordinary property name",
			column: "settings.tools[0].key",
			want:   "zero",
			found:  true,
		},
		{
			name:   "another index stays an ordinary property name",
			column: "settings.tools[1].key",
			want:   "one",
			found:  true,
		},
		{
			name:   "wildcard stays an ordinary property name",
			column: "settings.tools[*].key",
			want:   "wildcard",
			found:  true,
		},
		{
			name:   "spaced brackets stay an ordinary property name",
			column: "settings.tools[ ].key",
			want:   "space",
			found:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := tableField(row, tt.column)
			assert.Equal(t, tt.found, found)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestTableFieldPrefersLiteralArrayElementPath(t *testing.T) {
	row := map[string]interface{}{
		"settings.tools[].key": "literal",
		"settings": map[string]interface{}{
			"tools": []interface{}{map[string]interface{}{"key": "projected"}},
		},
	}

	got, found := tableField(row, "settings.tools[].key")

	assert.True(t, found)
	assert.Equal(t, "literal", got)
}

func TestDefaultFormatterRendersDeclaredArrayElementColumn(t *testing.T) {
	viper.Reset()
	viper.Set("output-format", tableFormat)
	viper.Set("jmespath", "")
	viper.Set("raw", false)
	out := new(bytes.Buffer)
	original := Stdout
	Stdout = out
	t.Cleanup(func() { Stdout = original })

	err := NewDefaultFormatter(true, true).FormatList([]interface{}{
		map[string]interface{}{
			"settings": map[string]interface{}{
				"tools": []interface{}{
					map[string]interface{}{"key": "lookup"},
					map[string]interface{}{"key": "refund"},
					map[string]interface{}{"key": "policy"},
					map[string]interface{}{"key": "escalate"},
				},
			},
		},
	}, "settings.tools[].key")

	assert.NoError(t, err)
	assert.Contains(t, out.String(), "SETTINGS . TOOLS[] . KEY")
	assert.Contains(t, out.String(), "lookup, refund, policy, …")
}

func TestDefaultFormatterRendersExplicitArrayElementColumn(t *testing.T) {
	viper.Reset()
	viper.Set("output-format", tableFormat)
	viper.Set("jmespath", "")
	viper.Set("raw", false)
	viper.Set("columns", "settings.tools[].key")
	out := new(bytes.Buffer)
	original := Stdout
	Stdout = out
	t.Cleanup(func() { Stdout = original })

	err := NewDefaultFormatter(true, true).FormatList([]interface{}{
		map[interface{}]interface{}{
			"settings": map[interface{}]interface{}{
				"tools": []interface{}{
					map[interface{}]interface{}{"key": "lookup-order"},
				},
			},
		},
	})

	assert.NoError(t, err)
	assert.Contains(t, out.String(), "lookup-order")
}

func TestDefaultFormatterValidatesArrayElementColumnsAcrossRows(t *testing.T) {
	rows := []map[string]interface{}{
		{
			"settings": map[string]interface{}{
				"tools": []interface{}{map[string]interface{}{"id": "missing-key"}},
			},
		},
		{
			"settings": map[string]interface{}{
				"tools": []interface{}{map[string]interface{}{"key": "lookup-order"}},
			},
		},
	}

	assert.NoError(t, checkColumns([]string{"settings.tools[].key"}, rows, true))
	assert.ErrorContains(t,
		checkColumns([]string{"settings.tools[].nmae"}, rows, true),
		`--columns: "settings.tools[].nmae" is not a field of the returned items`,
	)
	assert.ErrorContains(t,
		checkColumns([]string{"settings.tools[]"}, rows, false),
		`declared column: "settings.tools[]" is not a field of the returned items`,
	)
}
```

- [x] **Step 2: Run the focused tests to verify RED**

Run:

```sh
go test ./cli -run 'Test(TableField|DefaultFormatter).*(ArrayElement|ArrayElementPath)'
```

Expected: FAIL because the current resolver treats `tools[]` as an object key and cannot project through the array; the rendering tests report the declared/explicit column as missing.

- [x] **Step 3: Implement the narrow projection resolver**

Replace the current `tableField` function in `cli/formatter.go` with these two functions:

```go
// tableField resolves dotted object paths and one array projection while
// preserving literal top-level keys.
func tableField(row map[string]interface{}, column string) (interface{}, bool) {
	if value, ok := row[column]; ok {
		return value, true
	}

	parts := strings.Split(column, ".")
	if len(parts) < 2 {
		return nil, false
	}

	projection := -1
	for i, part := range parts {
		if part == "" {
			return nil, false
		}
		if !strings.HasSuffix(part, "[]") {
			continue
		}
		if projection >= 0 || i == len(parts)-1 || strings.Count(part, "[]") != 1 {
			return nil, false
		}
		parts[i] = strings.TrimSuffix(part, "[]")
		if parts[i] == "" {
			return nil, false
		}
		projection = i
	}

	if projection < 0 {
		return objectField(row, column)
	}

	value, ok := objectField(row, strings.Join(parts[:projection+1], "."))
	if !ok {
		return nil, false
	}
	items, ok := value.([]interface{})
	if !ok {
		return nil, false
	}
	if len(items) == 0 {
		return []interface{}{}, true
	}

	suffix := strings.Join(parts[projection+1:], ".")
	projected := make([]interface{}, 0, len(items))
	for _, item := range items {
		object, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		child, ok := objectField(object, suffix)
		if !ok || child == nil {
			continue
		}
		projected = append(projected, child)
	}
	if len(projected) == 0 {
		return nil, false
	}
	return projected, true
}

// objectField resolves exact or dotted keys through objects only.
func objectField(row map[string]interface{}, column string) (interface{}, bool) {
	if value, ok := row[column]; ok {
		return value, true
	}

	parts := strings.Split(column, ".")
	if len(parts) < 2 {
		return nil, false
	}

	var value interface{} = row
	for _, part := range parts {
		if part == "" {
			return nil, false
		}
		object, ok := value.(map[string]interface{})
		if !ok {
			return nil, false
		}
		value, ok = object[part]
		if !ok {
			return nil, false
		}
	}
	return value, true
}
```

- [x] **Step 4: Run focused and package tests to verify GREEN**

Run:

```sh
gofmt -w cli/formatter.go cli/formatter_test.go
go test ./cli -run 'Test(TableField|DefaultFormatter).*(ArrayElement|ArrayElementPath)'
go test ./cli
```

Expected: every command exits 0; the focused tests prove projection, sparse/mixed handling, empty arrays, exact-key precedence, header preservation, YAML normalization, and error ownership; all existing formatter tests remain green.

- [x] **Step 5: Commit Task 1**

```sh
git add cli/formatter.go cli/formatter_test.go
git commit -m "feat(cli): project table columns through arrays"
```

### Task 2: Pin generator handoff and generated-CLI behavior

**Files:**
- Modify: `templates_golden_test.go:76-96`
- Modify: `testdata/golden/group_widgets_commands.go:205-220`
- Modify: `main_internal_test.go:740-838`

**Interfaces:**
- Consumes: Task 1's `tableField` projection behavior through the public `bartolocli.FormatList` call emitted by generated commands.
- Produces: golden evidence that `x-cli-list-fields` preserves `tools[].key` verbatim and an integration test that builds and runs a generated CLI with table mode forced through its supported custom registration hook.

- [x] **Step 1: Write the generated-CLI regression test and update the golden input**

In `templates_golden_test.go`, change the search declaration and row schema to:

```yaml
      x-cli-list-fields: [name, "tools[].key"]
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                properties:
                  matches:
                    type: array
                    items:
                      type: object
                      properties:
                        name: {type: string}
                        tools:
                          type: array
                          items:
                            type: object
                            properties:
                              key: {type: string}
```

Replace `TestGeneratedListCommandRendersNestedResponse` in `main_internal_test.go` with this test. It overwrites only the temporary generated project's user-owned custom hook so the subprocess uses the public formatter with `terminal=true`; production terminal detection is unchanged.

```go
func TestGeneratedListCommandRendersArrayElementPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/files" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"file_1","settings":{"tools":[{"key":"lookup"},{"key":"refund"},{"key":"policy"},{"key":"escalate"}]}}]`))
	}))
	defer server.Close()

	repoRoot, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	tmp := t.TempDir()
	oldWD := repoRoot
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir tempdir: %v", err)
	}
	defer os.Chdir(oldWD)

	config := &ProjectConfig{
		AppName:             "nested-cli",
		AppVersion:          "0.1.0",
		ModulePath:          "github.com/acme/nested-cli",
		BartoloReplacePath:  repoRoot,
		BartoloVersion:      bartoloVersion,
		EnvPrefix:           "NESTED",
		SerializationFormat: "json",
	}
	if err := writeProjectScaffold(config, false); err != nil {
		t.Fatalf("writeProjectScaffold: %v", err)
	}

	specPath := filepath.Join(tmp, "openapi.yaml")
	spec := fmt.Sprintf(`openapi: 3.0.3
info:
  title: Nested API
  version: "1"
servers:
  - url: %s
paths:
  /files:
    get:
      operationId: listFiles
      x-cli-list-fields:
        - id
        - settings.tools[].key
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: array
                items:
                  type: object
`, server.URL)
	if err := os.WriteFile(specPath, []byte(spec), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	api := ProcessAPI("nested", loadTestSpec(t, spec))
	var commandPath string
	if len(api.Operations) == 1 {
		commandPath = api.Operations[0].CommandPath
	} else if len(api.Groups) == 1 && len(api.Groups[0].Operations) == 1 {
		commandPath = api.Groups[0].Operations[0].CommandPath
	} else {
		t.Fatalf("expected one generated operation, got %d root operations and %d groups", len(api.Operations), len(api.Groups))
	}
	if err := generateFromSpec(specPath); err != nil {
		t.Fatalf("generateFromSpec: %v", err)
	}

	customRegister := `package custom

import (
	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
)

func Register(root *cobra.Command) {
	_ = root
	bartolocli.Formatter = bartolocli.NewDefaultFormatter(true, true)
}
`
	customPath := filepath.Join(tmp, "cli", "custom", "register.go")
	if err := os.WriteFile(customPath, []byte(customRegister), 0o644); err != nil {
		t.Fatalf("write custom table hook: %v", err)
	}

	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = tmp
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Fatalf("go mod tidy: %v\n%s", err, string(out))
	}

	cliPath := filepath.Join(tmp, "nested-cli")
	build := exec.Command("go", "build", "-o", cliPath, "./cmd/nested-cli")
	build.Dir = tmp
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build generated CLI: %v\n%s", err, string(out))
	}

	run := exec.Command(cliPath, strings.Fields(commandPath)...)
	run.Dir = tmp
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("run generated list command: %v\n%s", err, string(out))
	}
	if !strings.Contains(string(out), "SETTINGS . TOOLS[] . KEY") ||
		!strings.Contains(string(out), "lookup, refund, policy, …") {
		t.Fatalf("generated list command returned unexpected table: %s", out)
	}
}
```

- [x] **Step 2: Run the generated tests to verify RED**

Run:

```sh
go test . -run 'TestGenerated(ListCommandRendersArrayElementPath|OutputMatchesGolden)'
```

Expected: FAIL before updating the golden file because the intentionally changed render differs. The integration test already consumes Task 1 and may pass independently.

- [x] **Step 3: Regenerate and inspect the golden output**

Run:

```sh
go test . -run TestGeneratedOutputMatchesGolden -update
rg -n -F 'FormatList(decoded, "name", "tools[].key")' testdata/golden/group_widgets_commands.go
```

Expected: the update command exits 0, and `rg` prints the generated `FormatList` call with the selector unchanged.

- [x] **Step 4: Run root and full tests**

Run:

```sh
gofmt -w main_internal_test.go templates_golden_test.go
go test . -run 'TestGenerated(ListCommandRendersArrayElementPath|OutputMatchesGolden)'
go test ./...
```

Expected: all commands exit 0; the generated binary prints a table containing the selector header and the abbreviated projected values.

- [x] **Step 5: Commit Task 2**

```sh
git add main_internal_test.go templates_golden_test.go testdata/golden/group_widgets_commands.go
git commit -m "test: cover generated array element columns"
```

### Task 3: Document the selector boundary and verify the repository

**Files:**
- Modify: `README.md:142-146`
- Modify: `templates/readme.tmpl:153-159`

**Interfaces:**
- Consumes: the Task 1 selector contract and unchanged `--jmespath` escape hatch.
- Produces: matching root and generated-CLI documentation for `object.field`, `array[].field`, sparse-element behavior, unsupported indexes/multiple projections, and JMESPath's role.

- [x] **Step 1: Update the root README**

Replace the final paragraph of the collection-output section beginning `Without x-cli-list-fields` with:

```markdown
Without `x-cli-list-fields` the columns are inferred from the response: nested
objects are skipped, a list shows its first three entries followed by `…`, long
values are truncated, and columns that do not fit the terminal are dropped from
the right. Declared or explicit columns can reach into nested objects with a
dotted path such as `model.id`. They can also project a field from each object
in one nested array, such as `settings.tools[].key`; missing, null, and
non-object elements are omitted, and the surviving values use the same
three-entry list abbreviation. A selector supports one `[]` and requires a
field after it. Array indexes and multiple projections are not column syntax;
use `--jmespath` when a response needs indexing, filtering, aggregation, or
other restructuring. Because `[]` contains shell metacharacters, quote the
complete argument when selecting a projected column, for example
`--columns 'key,settings.tools[].key'`.
```

- [x] **Step 2: Update the generated README template**

Replace the corresponding collection clause in `templates/readme.tmpl` with this exact text:

```markdown
- GET collections are inferred automatically, as is any method whose 2xx JSON response is a conventional page of rows: an array of objects under `data`, `items`, `results`, `records`, `entries` or `servers`, next to a field that describes the collection — `has_more`, `next_page_token`, a cursor, `total_count`, `count`. An echoed `limit` or `offset` is hidden from the table footer but does not classify an operation. Only immediate properties are read, so an envelope or a row schema composed with `allOf`/`oneOf` is not inferred. Anything else can opt in with `x-cli-list: true`, and `x-cli-list: false` disables inference. A non-empty `x-cli-list-fields` list both marks the operation as a collection and sets the table's ordered default columns (an empty `x-cli-list-fields: []` marks nothing on its own); otherwise columns are inferred from the response (nested objects skipped, lists abbreviated to their first three entries) and trimmed to fit the terminal. Use `--columns id,name` to pick and order them for one invocation. Declared or explicit columns can reach through nested objects with `model.id` and can project a field from one nested array with `settings.tools[].key`; missing, null, and non-object elements are omitted before the usual three-entry list abbreviation. A selector supports one `[]` and requires a field after it. Because `[]` contains shell metacharacters, quote the complete argument when selecting a projected column, for example `--columns 'key,settings.tools[].key'`. Use `--jmespath` for indexing, filtering, aggregation, multiple projections, or another one-off restructuring (its result is tabled with columns inferred from the projected rows), and `-o json` for the complete response.
```

- [x] **Step 3: Verify documentation parity and repository health**

Run:

```sh
rg -n -F 'settings.tools[].key' README.md templates/readme.tmpl
rg -n -F 'a bracket the grammar does not accept' README.md
rg -n -F 'Use `--jmespath` for indexing, filtering, aggregation, multiple projections' templates/readme.tmpl
git diff --check
make verify
go test ./...
go vet ./...
```

Expected: each `rg` finds the documented contract, `git diff --check` emits no output, and every verification command exits 0 with no failures.

- [x] **Step 4: Commit Task 3**

```sh
git add README.md templates/readme.tmpl
git commit -m "docs: explain array element column paths"
```

## Final branch verification

After all three task reviews are approved, run:

```sh
git diff --check origin/main...
make verify
go test ./...
go vet ./...
git status --short
```

Expected: diff check is silent; smoke generation, the full test suite, and vet exit 0; the working tree contains no uncommitted files.
