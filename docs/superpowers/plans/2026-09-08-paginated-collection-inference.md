# Paginated Collection Inference Implementation Plan

> **Status: implemented** in `ec7166d..0ec66cf` (PR #42). This is a historical record; the shipped
> behaviour is `main.go` and its tests, and the code excerpts and line numbers below are already
> stale. Do not execute the checkboxes.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Infer list classification for any HTTP method when a 2xx JSON response is a conventional paginated collection envelope, so standard POST retrieval endpoints render as tables without per-operation annotation, while `x-cli-list` / `x-cli-list-fields` remain the authority for everything else.

**Architecture:** One envelope vocabulary, exported from `cli/formatter.go` and consumed by both the runtime formatter and the generator, replaces the three overlapping key lists in the codebase today. On top of it, `isPaginatedCollectionResponse` accepts a response only when it has an array **of objects** under a conventional collection key next to a pagination key. That becomes the second arm of a new named predicate `isInferredList`, alongside the existing GET-only path/response guess, which is untouched. Explicit metadata keeps precedence in both directions.

**Tech Stack:** Go, kin-openapi v0.146.0, Go templates, Cobra CLI, Testify

**Spec:** Linear RES-1540 — "POST search commands never render a table: bartolo infers list-ness for GET only" (https://linear.app/orqai/issue/RES-1540). Prior work: `docs/superpowers/plans/2026-09-03-explicit-list-metadata.md` (ENG-2942), which added the explicit extensions this plan complements.

## Global Constraints

- Explicit metadata keeps absolute precedence. `x-cli-list: true` and non-empty `x-cli-list-fields` still force list classification; `x-cli-list: false` still disables **all** automatic inference, including the new rule.
- **One vocabulary, one home.** `ConventionalCollectionKeys` and `PaginationKeys` are declared once in `cli/formatter.go` and consumed by the generator through the existing `bartolocli` import (`main.go:26`). No key list is copied into `main.go`. `main.go` already consumes exported `cli` symbols this way — `bartolocli.OutputFormats` (`main.go:2512`), `bartolocli.ReservedFlagName` (`main.go:427`) — so this is the established direction of dependency, not a new one.
- `ConventionalCollectionKeys` is exactly today's `conventionalKeys` (`cli/formatter.go:595`), renamed and exported, including `servers`: `items`, `data`, `results`, `records`, `entries`, `servers`.
- `PaginationKeys` is exactly the key list `isEnvelopePlumbing` already suppresses from the table footer (`cli/formatter.go:480-483`), plus the four cursor spellings it is missing: `has_more`, `has_next_page`, `next_page_token`, `next_page`, `next_cursor`, `prev_cursor`, `cursor`, `next`, `previous`, `starting_after`, `ending_before`, `total`, `total_count`, `total_pages`, `count`, `limit`, `offset`, `page`, `per_page`.
- The new rule is a shape test on the response only. It never looks at the method, the path, or the operation name — a paginated envelope is a paginated envelope. Applying it to GET as well changes no GET outcome, because every GET matching it already infers a list from its path or response.
- All three halves are required: a conventional collection key, whose schema is an **array of objects**, next to a pagination key. Each half is load-bearing:
  - Without the pagination key, `POST /v2/router/embeddings` and `POST /v2/router/rerank` match — both return `{object: "list", data | results: [...]}` and neither wants embedding vectors as table cells. `object: "list"` is therefore **not** an accepted signal on its own.
  - Without the array-of-objects check, a `data: [string]` envelope classifies as a list and then dead-ends at runtime: `objectRows` (`cli/formatter.go:653`) rejects non-object rows, so `FormatList` prints "Not shown as a table" instead of a table.
- Only `application/json` and `+json` response content is inspected by the new rule. `isCollectionResponse` keeps its current media-type-agnostic behaviour; the shared walker passes the media type through so each predicate decides for itself.
- Out of scope, unchanged by this plan: nested row paths such as `search.data` (`POST /v3/traces/query` returns `{object, search}` and stays non-list); responses composed with `allOf`/`oneOf`, whose `Properties` map is empty and which therefore fall through to the existing behaviour; arrays under keys outside the conventional set, such as `matches` or `buckets`, which remain the job of `x-cli-list-fields`.
- Do not edit `CHANGELOG.md`. It is frozen; release notes are generated from the merged pull request.
- **Expected effect, measured outside this repo and not pinned by its tests.** Against `orq-cli` `origin/main:openapi.yaml` (292 operations), the rule classifies five non-GET operations as lists: `POST /v3/traces/search`, `POST /v3/logs/search`, `POST /v2/reporting`, `POST /v2/webhooks/query`, `POST /v2/knowledge/{knowledge_id}/datasources/{datasource_id}/chunks/list`. No GET changes. That document is not vendored here and no test in this repo can check it — Task 3 pins the rule against the fixture that *is* vendored (`testdata/orq/openapi.json`, 142 operations), where one non-GET operation qualifies.

## File Structure

| File | Responsibility in this change |
| --- | --- |
| `cli/formatter.go` | Home of the envelope vocabulary: export `ConventionalCollectionKeys` and `PaginationKeys`, rewrite `isEnvelopePlumbing` over the latter. |
| `cli/formatter_test.go` | Prove the footer no longer leaks cursor tokens. |
| `main.go` | Shared response walker; `isPaginatedCollectionResponse`; `isInferredList`; the call site at line 408. |
| `main_internal_test.go` | Synthetic coverage of the new rule; an exhaustive pin over the vendored orq fixture. |
| `templates_golden_test.go` | Add an unannotated paginated POST to `goldenSpec`. |
| `testdata/golden/group_widgets_commands.go` | Regenerated golden render (via `go test . -update`). |
| `README.md`, `templates/readme.tmpl` | Document the new inference. |

No template change: `templates/command_partials.tmpl:211` already selects `FormatList` from `IsList`, and `--columns` is already a global flag (`cli/cli.go:161`).

---

### Task 1: One envelope vocabulary in `cli`

**Files:**
- Modify: `cli/formatter.go:473-486` (`isEnvelopePlumbing`), `cli/formatter.go:593-595` (`conventionalKeys`), `cli/formatter.go:606` (its only use)
- Test: `cli/formatter_test.go`

**Interfaces:**
- Produces: `cli.ConventionalCollectionKeys []string` and `cli.PaginationKeys []string`, both exported and consumed by Task 2 through the `bartolocli` alias.
- Consumes: nothing new.

Why this task is first: the generator cannot reference the vocabulary before it exists, and the footer defect this fixes is user-visible on its own.

- [ ] **Step 1: Write the failing formatter test**

`orq traces search` returns `{data: [...], has_more, next_page_token}`. `has_more` is suppressed from the footer today; `next_page_token` is not, so a base64 cursor is printed under every table. Add to `cli/formatter_test.go`, following the existing table tests' style (find one that asserts on footer text and mirror its setup):

```go
func TestDefaultFormatterSuppressesCursorPlumbingInFooter(t *testing.T) {
	viper.Reset()
	viper.Set("output-format", tableFormat)
	viper.Set("jmespath", "")
	viper.Set("raw", false)
	out := new(bytes.Buffer)
	original := Stdout
	Stdout = out
	t.Cleanup(func() { Stdout = original })

	err := NewDefaultFormatter(true, true).FormatList(map[string]interface{}{
		"object": "list",
		"data": []map[string]interface{}{
			{"id": "trace_1", "name": "first"},
		},
		"has_more":        true,
		"next_page_token": "eyJvZmZzZXQiOjUwfQ",
		"total_pages":     3,
	})

	assert.NoError(t, err)
	assert.Contains(t, out.String(), "trace_1")
	assert.NotContains(t, out.String(), "eyJvZmZzZXQiOjUwfQ")
	assert.NotContains(t, out.String(), "total_pages")
}
```

This mirrors `TestDefaultFormatterRendersListEnvelopeMetadata` (`cli/formatter_test.go:70`) — same viper setup, same `Stdout` swap, same `NewDefaultFormatter(true, true)` construction. Do not invent a new helper.

- [ ] **Step 2: Run the test and verify RED**

Run: `go test ./cli -run TestDefaultFormatterSuppressesCursorPlumbingInFooter -v`

Expected: FAIL — the footer contains `next_page_token: eyJvZmZzZXQiOjUwfQ` and `total_pages: 3`, because neither key is in `isEnvelopePlumbing`'s switch.

- [ ] **Step 3: Export the vocabulary and rewrite the plumbing check**

In `cli/formatter.go`, replace `isEnvelopePlumbing` (lines 473-486) with:

```go
// PaginationKeys are the envelope fields that carry paging bookkeeping rather
// than payload. The generator classifies a response as a collection from these
// too, so the two must not drift: a cursor the table hides in its footer is the
// same cursor that proves the response is one page of many.
var PaginationKeys = []string{
	"has_more", "has_next_page", "next_page_token", "next_page",
	"next_cursor", "prev_cursor", "cursor", "next", "previous",
	"starting_after", "ending_before",
	"total", "total_count", "total_pages", "count",
	"limit", "offset", "page", "per_page",
}

func isEnvelopePlumbing(key string, value interface{}) bool {
	switch key {
	case "object", "kind", "type":
		return value == "list" || value == "collection"
	}
	return slices.Contains(PaginationKeys, key)
}
```

Add `"slices"` to the import block if it is absent.

Then, at `cli/formatter.go:593-595`, rename and export the collection keys:

```go
// ConventionalCollectionKeys are envelope names that outrank a wrapper named
// after the resource, so a stray nested array is not mistaken for the
// collection.
var ConventionalCollectionKeys = []string{"items", "data", "results", "records", "entries", "servers"}
```

and update its only use at line 606 from `range conventionalKeys` to `range ConventionalCollectionKeys`.

- [ ] **Step 4: Verify GREEN**

Run: `gofmt -w cli/formatter.go cli/formatter_test.go && go test ./cli`

Expected: PASS. If another footer test now fails because a key it asserted on became plumbing, read it: the four newly-suppressed keys are `has_next_page`, `next_page_token`, `next_page`, `total_pages`, and suppressing them is the point.

- [ ] **Step 5: Commit**

```bash
git add cli/formatter.go cli/formatter_test.go
git commit -m "fix(cli): suppress cursor plumbing from the table footer (RES-1540)"
```

---

### Task 2: Paginated collection shape test in the generator

**Files:**
- Modify: `main.go` (new walker and predicates near `isCollectionResponse`, line 1198; call site at lines 406-409)
- Test: `main_internal_test.go`

**Interfaces:**
- Consumes: `bartolocli.ConventionalCollectionKeys` and `bartolocli.PaginationKeys` from Task 1.
- Produces: `isInferredList(method, path string, operation *openapi3.Operation) bool`, `isPaginatedCollectionResponse(operation *openapi3.Operation) bool`, and `forEachSuccessResponseContent(operation *openapi3.Operation, fn func(mediaType string, content *openapi3.MediaType) bool) bool`. Sets `Operation.IsList` for matching operations; `Operation.ListFields` stays empty, so the template emits `FormatList(decoded)` with no declared columns.

- [ ] **Step 1: Write the failing tests**

Append to `main_internal_test.go`. Only the first three cases and the JSON-media-type case go red before Step 3 — the rest already pass under today's code and are non-regression assertions, kept because they pin the exclusions the rule depends on:

```go
func TestProcessAPIInfersPaginatedCollectionResponses(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		mediaType  string
		properties string
		wantList   bool
	}{
		{
			name:   "POST with a collection key and a pagination sibling",
			method: "post",
			path:   "/traces/search",
			properties: "                  data: {type: array, items: {type: object}}\n" +
				"                  has_more: {type: boolean}\n",
			wantList: true,
		},
		{
			name:   "POST with next_page_token instead of has_more",
			method: "post",
			path:   "/logs/search",
			properties: "                  data: {type: array, items: {type: object}}\n" +
				"                  next_page_token: {type: string}\n",
			wantList: true,
		},
		{
			name:   "POST with items and count",
			method: "post",
			path:   "/webhooks/query",
			properties: "                  items: {type: array, items: {type: object}}\n" +
				"                  count: {type: integer}\n",
			wantList: true,
		},
		{
			name:       "POST with a collection key but no pagination sibling",
			method:     "post",
			path:       "/router/embeddings",
			properties: "                  data: {type: array, items: {type: object}}\n",
			wantList:   false,
		},
		{
			name:   "object list marker is not a pagination sibling",
			method: "post",
			path:   "/router/rerank",
			properties: "                  object: {type: string, enum: [list]}\n" +
				"                  results: {type: array, items: {type: object}}\n",
			wantList: false,
		},
		{
			name:   "rows the table cannot render do not qualify",
			method: "post",
			path:   "/ids/search",
			properties: "                  data: {type: array, items: {type: string}}\n" +
				"                  has_more: {type: boolean}\n",
			wantList: false,
		},
		{
			name:   "pagination without a conventional collection key",
			method: "post",
			path:   "/knowledge/search",
			properties: "                  matches: {type: array, items: {type: object}}\n" +
				"                  has_more: {type: boolean}\n",
			wantList: false,
		},
		{
			name:   "a scalar named data does not qualify",
			method: "post",
			path:   "/jobs/run",
			properties: "                  data: {type: string}\n" +
				"                  has_more: {type: boolean}\n",
			wantList: false,
		},
		{
			name:   "nested rows stay out of scope",
			method: "post",
			path:   "/traces/query",
			properties: "                  search: {type: object, properties: {data: {type: array, items: {type: object}}, has_more: {type: boolean}}}\n",
			wantList: false,
		},
		{
			name:      "a non-JSON envelope is not inspected",
			method:    "post",
			path:      "/exports/search",
			mediaType: "application/xml",
			properties: "                  data: {type: array, items: {type: object}}\n" +
				"                  has_more: {type: boolean}\n",
			wantList: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mediaType := tt.mediaType
			if mediaType == "" {
				mediaType = "application/json"
			}
			doc := loadTestSpec(t, fmt.Sprintf(`
openapi: 3.0.3
info:
  title: Paginated API
  version: "1"
paths:
  %s:
    %s:
      operationId: testPaginatedInference
      responses:
        "200":
          description: ok
          content:
            %s:
              schema:
                type: object
                properties:
%s`, tt.path, tt.method, mediaType, tt.properties))

			byRoute := operationsByRoute(ProcessAPI("example", doc))
			op, ok := byRoute[strings.ToUpper(tt.method)+" "+tt.path]
			if !ok {
				t.Fatalf("%s %s is missing from the generated CLI", tt.method, tt.path)
			}
			if got := op.IsList; got != tt.wantList {
				t.Fatalf("IsList = %t, want %t", got, tt.wantList)
			}
			if op.IsList && len(op.ListFields) != 0 {
				t.Fatalf("inferred list should declare no columns, got %v", op.ListFields)
			}
		})
	}
}

func TestProcessAPIExplicitFalseSuppressesPaginatedInference(t *testing.T) {
	doc := loadTestSpec(t, `
openapi: 3.0.3
info:
  title: Suppressed API
  version: "1"
paths:
  /traces/search:
    post:
      operationId: searchTraces
      x-cli-list: false
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                properties:
                  data: {type: array, items: {type: object}}
                  has_more: {type: boolean}
`)

	op, ok := operationsByRoute(ProcessAPI("example", doc))["POST /traces/search"]
	if !ok {
		t.Fatal("POST /traces/search is missing from the generated CLI")
	}
	if op.IsList {
		t.Fatal("x-cli-list: false should suppress paginated inference")
	}
}
```

- [ ] **Step 2: Run the tests and verify RED**

Run: `go test . -run 'TestProcessAPI(InfersPaginated|ExplicitFalseSuppresses)'`

Expected: FAIL on the three positive POST cases (`IsList = false, want true`), because classification still requires GET. Every other case, and the suppression test, already passes.

- [ ] **Step 3: Add the walker and the shape test**

In `main.go`, replace `isCollectionResponse` (lines 1198-1229) with a version built on a shared walker, and add the new predicate beside it:

```go
func forEachSuccessResponseContent(operation *openapi3.Operation, fn func(mediaType string, content *openapi3.MediaType) bool) bool {
	for code, response := range operation.Responses.Map() {
		status, err := strconv.Atoi(code)
		if err != nil || status < 200 || status >= 300 || response == nil || response.Value == nil {
			continue
		}

		for mediaType, content := range response.Value.Content {
			if content == nil {
				continue
			}
			if fn(mediaType, content) {
				return true
			}
		}
	}

	return false
}

func isCollectionResponse(operation *openapi3.Operation) bool {
	return forEachSuccessResponseContent(operation, func(_ string, content *openapi3.MediaType) bool {
		if _, ok := content.Example.([]interface{}); ok {
			return true
		}
		if content.Schema == nil || content.Schema.Value == nil {
			return false
		}

		schema := content.Schema.Value
		if schema.Items != nil {
			return true
		}
		// Any array property counts: wrappers are named after the resource
		// as often as they are called `data` or `items`.
		for _, property := range schema.Properties {
			if property != nil && property.Value != nil && property.Value.Items != nil {
				return true
			}
		}
		return false
	})
}

// isPaginatedCollectionResponse reports whether a 2xx JSON response is a page of
// rows. All three halves are load-bearing: the pagination key is what separates
// retrieval from computation, keeping /v2/router/embeddings out; requiring rows
// to be objects keeps out envelopes the table renderer would reject anyway.
func isPaginatedCollectionResponse(operation *openapi3.Operation) bool {
	return forEachSuccessResponseContent(operation, func(mediaType string, content *openapi3.MediaType) bool {
		if !isJSONMediaType(mediaType) || content.Schema == nil || content.Schema.Value == nil {
			return false
		}

		properties := content.Schema.Value.Properties
		if !hasObjectArrayProperty(properties, bartolocli.ConventionalCollectionKeys) {
			return false
		}
		for _, key := range bartolocli.PaginationKeys {
			if properties[key] != nil {
				return true
			}
		}
		return false
	})
}

func isJSONMediaType(mediaType string) bool {
	base, _, _ := strings.Cut(mediaType, ";")
	base = strings.TrimSpace(base)
	return base == "application/json" || strings.HasSuffix(base, "+json")
}

func hasObjectArrayProperty(properties openapi3.Schemas, keys []string) bool {
	for _, key := range keys {
		property := properties[key]
		if property == nil || property.Value == nil || property.Value.Items == nil {
			continue
		}
		// `items: {type: object}` with no nested `properties:` parses with a nil
		// Properties map, so the declared type has to be consulted too. Types.Is
		// is nil-safe.
		if items := property.Value.Items.Value; items != nil && (items.Type.Is("object") || items.Properties != nil) {
			return true
		}
	}

	return false
}
```

- [ ] **Step 4: Name the classification rule and use it**

Still in `main.go`, add beside the predicates:

```go
func isInferredList(method, path string, operation *openapi3.Operation) bool {
	if strings.EqualFold(method, "get") && (isCollectionPath(path) || isCollectionResponse(operation)) {
		return true
	}
	return isPaginatedCollectionResponse(operation)
}
```

Then replace the expression at lines 408-409:

```go
			isList := markedList || (!inferenceDisabled && strings.EqualFold(method, "get") &&
				(isCollectionPath(path) || isCollectionResponse(operation)))
```

with:

```go
			isList := markedList || (!inferenceDisabled && isInferredList(method, path, operation))
```

- [ ] **Step 5: Verify GREEN**

Run: `gofmt -w main.go main_internal_test.go && go test .`

Expected: PASS, including the pre-existing `"unmarked POST array response is not inferred"` case (`main_internal_test.go:266`) — its response body is `{matches: [...]}` with no pagination sibling, so the new rule correctly leaves it alone.

- [ ] **Step 6: Commit**

```bash
git add main.go main_internal_test.go
git commit -m "feat(cli): infer list output for paginated collection responses (RES-1540)"
```

---

### Task 3: Pin the rule exhaustively against the vendored orq document

**Files:**
- Test: `main_internal_test.go` (new package-level helper next to `loadTestSpec` at line 35; new test)

**Interfaces:**
- Consumes: `operationsByRoute`, the new predicates via `ProcessAPI`, and `testdata/orq/openapi.json`.
- Produces: `loadOrqSpec(t *testing.T) *openapi3.T`, a package-level helper matching the `loadTestSpec` convention. `TestProcessAPIMarksOrqPostCollections` (line 417) currently inlines this load — refactor it to use the helper in the same commit.

Why exhaustive: the rule is broad and key-based, so the regression that matters is not "does the known route still classify" but "did something else start classifying". Asserting the exact set is the only version of this test that catches that.

- [ ] **Step 1: Add the helper and the exhaustive test**

Add next to `loadTestSpec` (`main_internal_test.go:35`):

```go
func loadOrqSpec(t *testing.T) *openapi3.T {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", "orq", "openapi.json"))
	if err != nil {
		t.Fatalf("read orq spec: %v", err)
	}
	doc, err := loadOpenAPIDocument(data)
	if err != nil {
		t.Fatalf("load orq spec: %v", err)
	}

	return doc
}
```

Rewrite the body of `TestProcessAPIMarksOrqPostCollections` (line 417) to call it instead of its inline `os.ReadFile` + `loadOpenAPIDocument` block, leaving its assertions unchanged. Then append:

```go
// The rule keys off property names, so the regression that matters is a route
// nobody looked at starting to render as a table. Assert the whole non-GET set,
// not just the routes this change was aimed at.
func TestProcessAPIClassifiesOrqNonGetListsExactly(t *testing.T) {
	const chunksRoute = "POST /v2/knowledge/{knowledge_id}/datasources/{datasource_id}/chunks/list"

	t.Run("inference alone classifies the paginated route", func(t *testing.T) {
		doc := loadOrqSpec(t)
		item := doc.Paths.Find("/v2/knowledge/{knowledge_id}/datasources/{datasource_id}/chunks/list")
		if item == nil || item.Post == nil {
			t.Fatal("the chunks route is missing from the fixture")
		}
		delete(item.Post.Extensions, ExtList)
		delete(item.Post.Extensions, ExtListFields)

		op, ok := operationsByRoute(ProcessAPI("orq", doc))[chunksRoute]
		if !ok {
			t.Fatalf("%s is missing from the generated CLI", chunksRoute)
		}
		if !op.IsList {
			t.Error("a data + has_more envelope should be inferred as a list without annotation")
		}
		if len(op.ListFields) != 0 {
			t.Errorf("inferred list should declare no columns, got %v", op.ListFields)
		}
	})

	t.Run("no other non-GET operation classifies", func(t *testing.T) {
		want := map[string]bool{
			// Inferred: data + has_more.
			chunksRoute: true,
			// Annotated by ENG-2942, not inferred: `matches` is not a
			// conventional collection key.
			"POST /v2/knowledge/{knowledge_id}/search": true,
		}

		got := map[string]bool{}
		for route, op := range operationsByRoute(ProcessAPI("orq", loadOrqSpec(t))) {
			if !op.IsList || strings.HasPrefix(route, "GET ") {
				continue
			}
			got[route] = true
		}

		for route := range got {
			if !want[route] {
				t.Errorf("%s newly renders as a list; add it to the expected set only if that is intended", route)
			}
		}
		for route := range want {
			if !got[route] {
				t.Errorf("%s should render as a list but does not", route)
			}
		}
	})
}
```

- [ ] **Step 2: Run the test**

Run: `go test . -run 'TestProcessAPI(ClassifiesOrqNonGetListsExactly|MarksOrqPostCollections)' -v`

Expected: PASS on all subtests. A failure in "no other non-GET operation classifies" names the route that unexpectedly qualified — read its response schema before touching the assertion. Notably `POST /v2/router/embeddings`, `/rerank`, `/moderations` and `/images/generations` must be absent: each has a collection key but no pagination key.

- [ ] **Step 3: Run the package tests**

Run: `go test .`

Expected: PASS. The in-memory extension deletion in the first subtest cannot leak into `TestProcessAPIMarksOrqPostCollections`, because `loadOrqSpec` re-reads the file per call.

- [ ] **Step 4: Commit**

```bash
git add main_internal_test.go
git commit -m "test(cli): pin the orq non-GET list classification set (RES-1540)"
```

---

### Task 4: Pin the generated command render

**Files:**
- Modify: `templates_golden_test.go` — the `goldenSpec` constant, inserting after the `/widgets/search` block (which ends with `name: {type: string}` at line 94) and before `/widgets/{id}` at line 95; and the doc comment at lines 12-16
- Modify: `testdata/golden/group_widgets_commands.go` (regenerated, do not hand-edit)

**Interfaces:**
- Consumes: `Operation.IsList` / `Operation.ListFields` as rendered by `templates/command_partials.tmpl:211`.
- Produces: a golden render proving an inferred list emits `FormatList(decoded)` with no column arguments.

- [ ] **Step 1: Add an unannotated paginated POST to the golden spec**

Insert into `goldenSpec`, keeping the two-space path indentation:

```yaml
  /widgets/query:
    post:
      operationId: QueryWidgets
      summary: Query widgets
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                properties:
                  data:
                    type: array
                    items:
                      type: object
                      properties:
                        id: {type: string}
                        name: {type: string}
                  has_more: {type: boolean}
                  next_page_token: {type: string}
```

In the `goldenSpec` doc comment (lines 12-16), append to the final clause: `, and a POST whose paginated envelope is inferred as a list without annotation`.

- [ ] **Step 2: Run the golden test and verify it fails**

Run: `go test . -run TestGeneratedOutputMatchesGolden`

Expected: FAIL — the render now contains a `widgets query` command the checked-in golden file does not.

- [ ] **Step 3: Regenerate and read the diff**

Run: `go test . -update && git diff testdata/golden/group_widgets_commands.go`

Expected in the diff: a new `queryWidgets` command whose formatting line is `bartolocli.FormatList(decoded)` — no trailing column string arguments. Confirm the neighbours are unchanged: `searchWidgets` still calls `bartolocli.FormatList(decoded, "name", "id")`, and `createWidget` — whose response is `{warnings: [array of string]}`, an unconventional key holding non-object rows with no pagination sibling — still calls `bartolocli.Formatter.Format(decoded)`.

If `createWidget` changed, stop: the rule is matching something it should not.

- [ ] **Step 4: Verify GREEN**

Run: `go test .`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add templates_golden_test.go testdata/golden/group_widgets_commands.go
git commit -m "test(cli): pin the inferred list render in the golden output (RES-1540)"
```

---

### Task 5: Document the inference and verify the repository

**Files:**
- Modify: `README.md:102` (extension table row) and `README.md:110-114` (the inference paragraph)
- Modify: `templates/readme.tmpl:156`

**Interfaces:**
- Consumes: nothing. Produces the user-facing contract for Tasks 1-4.

- [ ] **Step 1: Update the extension table row**

In `README.md:102`, replace:

```markdown
| `x-cli-list` | Set to `true` to mark an operation as a collection, or `false` to suppress automatic GET collection detection. |
```

with:

```markdown
| `x-cli-list` | Set to `true` to mark an operation as a collection, or `false` to suppress automatic collection detection. |
```

- [ ] **Step 2: Replace the inference paragraph**

In `README.md`, replace the paragraph at lines 110-114:

```markdown
Collection paths and responses are inferred automatically for GET operations.
For other HTTP methods, use `x-cli-list: true` to opt into inferred columns or
provide `x-cli-list-fields`. A non-empty field list also marks the operation as
a collection, while `x-cli-list: false` disables automatic GET inference. An
empty `x-cli-list-fields: []` marks nothing on its own, and combining
`x-cli-list: false` with declared columns is a contradiction that fails
generation.
```

with:

```markdown
Collection paths and responses are inferred automatically for GET operations.
Any method is also inferred as a collection when its 2xx JSON response is a
conventional page of rows: an array of objects under `data`, `items`, `results`,
`records`, `entries` or `servers`, next to a pagination field such as `has_more`,
`next_page_token`, `next_cursor`, `total_count` or `count`. That covers the usual
POST search and query endpoints without annotation. Every part is required: an
embeddings call returns a `data` array with no cursor and stays serialized, and an
array of plain strings is not something a table can render.

Use `x-cli-list: true` for a collection that does not match, such as rows under a
resource-named key, or `x-cli-list-fields` to both mark it and set the columns. A
non-empty field list also marks the operation as a collection, while
`x-cli-list: false` disables all automatic inference. An empty
`x-cli-list-fields: []` marks nothing on its own, and combining `x-cli-list: false`
with declared columns is a contradiction that fails generation.
```

- [ ] **Step 3: Update the generated README template**

`templates/readme.tmpl:156` is one dense bullet, not the two-paragraph shape above, so it needs its own wording rather than a copy. Replace the bullet that begins `- GET collections are inferred automatically.` with:

```
- GET collections are inferred automatically, as is any method whose 2xx JSON response is a conventional page of rows: an array of objects under `data`, `items`, `results`, `records`, `entries` or `servers`, next to a pagination field such as `has_more`, `next_page_token` or `total_count`. Anything else can opt in with `x-cli-list: true`, and `x-cli-list: false` disables inference. A non-empty `x-cli-list-fields` list both marks the operation as a collection and sets the table's ordered default columns (an empty `x-cli-list-fields: []` marks nothing on its own); otherwise columns are inferred from the response (nested objects skipped, lists abbreviated to their first three entries) and trimmed to fit the terminal. Use `--columns id,name` to pick and order them for one invocation; declared or explicit columns can include nested fields such as `model.id`; use `--jmespath` for a one-off projection (its result is tabled with columns inferred from the projected rows, so `-j 'data[].{id: id, model: model.id}'` surfaces a nested field), and `-o json` for the complete response.
```

- [ ] **Step 4: Run the full verification**

Run: `gofmt -l . && make verify`

Expected: `gofmt -l .` prints nothing; `make verify` runs smoke plus `go test ./...` and passes. Do not edit `CHANGELOG.md`.

- [ ] **Step 5: Commit**

```bash
git add README.md templates/readme.tmpl
git commit -m "docs(cli): document paginated collection inference (RES-1540)"
```

---

## Self-Review Notes

- The `x-cli-list-fields`-only path, the `x-cli-list: true` path and the contradiction panic are unchanged and stay covered by `TestProcessAPIReadsListFieldsExtension`, `TestProcessAPIListExtensionControlsClassification`, `TestProcessAPIRejectsContradictoryListMetadata` and `TestProcessAPIRejectsNonBooleanListExtension`.
- `isCollectionResponse` keeps matching any array property, which is why it stays behind the GET gate inside `isInferredList`.
- Every symbol used in a later task is defined in an earlier one: `ConventionalCollectionKeys` and `PaginationKeys` in Task 1, `isPaginatedCollectionResponse` / `isInferredList` / `forEachSuccessResponseContent` in Task 2, `loadOrqSpec` in Task 3.

## Follow-up, not in this plan

Shipping this to users needs a bartolo release, a dependency bump in `orq-cli` (`go.mod`, `.bartolo.json`, both currently `v0.12.0`) and a regenerate. Handled outside this repo.

Endpoints this rule does not reach, each needing explicit `x-cli-list-fields` upstream (ENG-2967) or separate nested-row support: `traces query-oql` (`{object, search}`, nested rows), `traces aggregate` (`data`, no pagination key), `logs aggregate` (`buckets`). RES-1540's problem statement names all three as broken — whether they stay on that ticket is the ticket owner's call, not this plan's.
