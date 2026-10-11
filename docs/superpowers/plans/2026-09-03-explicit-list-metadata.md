# Explicit List Metadata Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let explicit OpenAPI metadata classify list operations for every HTTP method while preserving GET-only automatic inference and current machine-readable output behavior.

**Architecture:** Add `x-cli-list` as an operation-level boolean override and keep non-empty `x-cli-list-fields` as an implicit positive marker. The generator owns classification; the existing command template continues to select `FormatList`. Separately, make the runtime list extractor recognize an empty custom-named envelope only when it contains exactly one array candidate.

**Tech Stack:** Go, kin-openapi, Go templates, Cobra CLI, Testify

## Global Constraints

- `x-cli-list: true` forces list classification for any HTTP method.
- `x-cli-list: false` disables automatic GET inference.
- `x-cli-list: false` combined with a non-empty `x-cli-list-fields` is a contradiction and fails generation, rather than being silently arbitrated in either direction.
- Non-empty `x-cli-list-fields` implies list classification and preserves declared column order.
- `x-cli-list-fields: []` does not imply list classification.
- A present but non-boolean `x-cli-list` fails generation. `extBool` is made strict to match, so `x-cli-no-validate` fails the same way instead of silently coercing to `false` — one contract for boolean extensions.
- Automatic path and response inference remains GET-only.
- Nested row paths such as `search.data` are out of scope.
- Pipes, explicit serialization formats and `--raw` remain unchanged.
- JMESPath **table columns** change: a projection reshapes the rows, so the schema's `x-cli-list-fields` no longer describe them and columns are inferred from the projected rows instead. An explicit `--columns` still wins. This amends the original constraint that JMESPath behaviour was untouched; leaving it untouched meant `-j 'data[].{id: id, model: model.id}'` rendered an empty `NAME` column and no `MODEL`. Serialized JMESPath output is unaffected.

---

### Task 1: Explicit generator classification

**Files:**
- Modify: `main.go`
- Test: `main_internal_test.go`
- Test: `templates_golden_test.go`
- Test fixture: `testdata/golden/group_widgets_commands.go`
- Modify: `README.md`
- Modify: `templates/readme.tmpl`

**Interfaces:**
- Consumes: operation extensions from `openapi3.Operation.Extensions`.
- Produces: `Operation.IsList bool` and ordered `Operation.ListFields []string` for the unchanged command template.

- [ ] **Step 1: Write failing classifier tests**

Add table-driven `ProcessAPI` coverage proving: POST plus `x-cli-list: true` is a list; POST plus ordered `x-cli-list-fields` is a list; unmarked POST with an array-bearing response is not a list; `x-cli-list: false` suppresses GET inference; non-empty fields win over `x-cli-list: false`; and empty fields do not mark POST as a list.

- [ ] **Step 2: Run the focused tests and verify RED**

Run: `go test . -run 'TestProcessAPI.*List'`

Expected: new positive POST and explicit-false cases fail because `x-cli-list` is not parsed and fields are still behind the GET gate.

- [ ] **Step 3: Implement minimal classification logic**

Add `ExtList = "x-cli-list"`, decode it only when present, and calculate:

```go
explicitList := len(listFields) > 0 || (hasListExtension && listExtension)
inferredList := !hasListExtension || listExtension
isList := explicitList || (inferredList && strings.EqualFold(method, "get") &&
	(isCollectionPath(path) || isCollectionResponse(operation)))
```

Non-empty fields deliberately remain authoritative if contradictory `x-cli-list: false` metadata is supplied.

- [ ] **Step 4: Run focused and package tests and verify GREEN**

Run: `go test . -run 'TestProcessAPI.*List' && go test .`

Expected: PASS.

- [ ] **Step 5: Pin generated command behavior**

Extend `goldenSpec` with a marked POST search using ordered `x-cli-list-fields`, retain the unmarked POST create with an array-bearing response, run `go test . -update`, and inspect that the former calls `FormatList(decoded, "name", "id")` while the latter calls `Formatter.Format(decoded)`.

- [ ] **Step 6: Document the public extension**

Document `x-cli-list`, the implication from non-empty `x-cli-list-fields`, explicit-false opt-out behavior, and GET-only inference in the root README and generated README template. Do not edit the frozen changelog; automatic release notes come from the pull request.

- [ ] **Step 7: Verify Task 1**

Run: `gofmt -w main.go main_internal_test.go templates_golden_test.go && go test ./...`

Expected: PASS.

### Task 2: Empty custom list envelopes

**Files:**
- Modify: `cli/formatter.go`
- Test: `cli/formatter_test.go`

**Interfaces:**
- Consumes: a value passed to `FormatList` whose top-level object may contain one custom-named array.
- Produces: `tableRows(data, requestedColumns)` recognizes a sole empty array while rejecting objects with multiple array candidates.

**Scope added after the plan was written** (recorded here so the plan matches what shipped):
- Candidate selection is one `collectionCandidate` helper covering both conventional keys and resource-named wrappers, run once for populated candidates and once for empty ones. Without this, the conventional-key loop returned an empty `data` before a populated `matches` was ever considered, printing `No results.` over real rows.
- List values render in table cells as their first `maxCellItems` entries followed by `…`, and `autoColumns` stops excluding array-valued fields. This reverses the v0.4.8 decision to skip arrays entirely; a truncated `a, b, c, …` is more useful than a hidden field.

- [ ] **Step 1: Write failing formatter tests**

Add a test proving `FormatList(map[string]interface{}{"matches": []interface{}{}})` prints `No results.` at an interactive table terminal, and retain/add a test proving two empty peer arrays fall back to serialization rather than choosing one.

- [ ] **Step 2: Run the focused test and verify RED**

Run: `go test ./cli -run 'TestDefaultFormatter.*Empty.*Envelope'`

Expected: the sole custom empty-array case serializes instead of printing `No results.`.

- [ ] **Step 3: Implement unambiguous empty-array detection**

In the custom-wrapper scan, treat empty arrays of the supported slice representations as candidates, but preserve the existing ambiguity check so more than one candidate returns `false`.

- [ ] **Step 4: Run focused and package tests and verify GREEN**

Run: `gofmt -w cli/formatter.go cli/formatter_test.go && go test ./cli`

Expected: PASS.

- [ ] **Step 5: Run repository verification**

Run: `make verify && go test ./...`

Expected: PASS with no formatting, lint, generation, or unit-test regressions.
