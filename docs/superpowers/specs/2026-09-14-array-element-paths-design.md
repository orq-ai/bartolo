# Array Element Paths for Table Columns

**Issue:** [RES-1574](https://linear.app/orqai/issue/RES-1574/bartolo-reach-into-arrays-from-x-cli-list-fields-with-an-element-path)

**Status:** Approved design

## Summary

Bartolo will let explicitly selected table columns project a field from every
object in an array. Both an OpenAPI `x-cli-list-fields` declaration and the
runtime `--columns` flag will accept selectors such as
`settings.tools[].key`. The resulting values will use Bartolo's existing list
cell rendering: source order, at most three displayed entries, an ellipsis when
more entries exist, and the normal 40-rune cell limit.

This feature extends the existing dotted object-path selector. It does not turn
table columns into a general expression language and does not change list
classification, row extraction, automatic column inference, or serialized
output.

## Motivation

Today `tableField` can resolve an exact top-level key or walk a dotted path
through nested objects, so `model.id` works. Traversal stops when it encounters
an array. Selecting `settings.tools` instead is not useful for an array of
objects: each object becomes compact JSON and the complete cell is truncated,
often before even the first identifying value is visible
(`cli/formatter.go:332-358,756-793`).

The limitation prevents useful default columns for resources whose identity is
partly expressed by related objects. Current first-party response shapes
include agent tools, team members, and knowledge bases. RES-1574 also identifies
smart-router models and alert data as consumers.

## User-facing contract

### Selector grammar

Existing dotted paths remain valid:

```text
model.id
settings.model.parameters.name
```

A selector may contain one array projection marker, written as the exact `[]`
suffix on an object-path segment:

```text
settings.tools[].key
team_of_agents[].key
settings.tools[].configuration.name
```

The supported grammar is:

```text
selector       := object-path | projected-path
object-path    := name ("." name)*
projected-path := object-prefix "[]" "." object-path
object-prefix  := name ("." name)*
```

`name` follows the existing path rules: it is a non-empty property name that
does not contain a dot. Only the exact `[]` suffix has projection meaning.
Strings such as `tools[0]`, `tools[1]`, `tools[*]`, and `tools[ ]` remain
ordinary property names rather than array operators.

The projection must have a non-empty property path after `[]`. Consequently,
`settings.tools[]` is invalid. A selector may contain no more than one `[]`.
Indexed access, multiple array boundaries, filters, slices, wildcards, and
functions are not part of this grammar.

### Resolution

Resolution preserves the current exact-key-first behavior. If the row has a
top-level property whose name exactly equals the complete selector, that value
wins. For example, a literal top-level key named `settings.tools[].key` takes
precedence over interpreting the text as a path.

Otherwise Bartolo resolves the object prefix using the current dotted-path
behavior. The property bearing `[]` must contain an array. Bartolo then visits
its elements in source order and resolves the suffix path against each object
element.

For each array element:

- A successfully resolved, non-null value is appended to the projected list.
- A non-object element is omitted.
- An element missing the suffix path is omitted.
- A suffix that resolves to `null` is omitted.

An empty source array is a successful projection whose value is an empty list.
A non-empty source array for which no element resolves a non-null value is an
unresolved selector for that row. A projection with at least one surviving
value is successful even if other elements were omitted.

The projected value is an ordinary `[]interface{}`. Existing cell formatting
therefore remains authoritative: strings are unquoted, other scalars use JSON
spelling, nested arrays or objects use the existing compact representation, a
fourth value becomes `…`, and the joined cell is truncated to 40 runes.

### Column validation and errors

The existing any-row validation rule remains unchanged. A requested selector
is valid when it resolves for at least one returned row. Rows where that valid
selector does not resolve render a blank cell. If no row resolves it, Bartolo
returns the existing source-specific error:

```text
--columns: "settings.tools[].nmae" is not a field of the returned items
declared column: "settings.tools[].nmae" is not a field of the returned items
```

An empty array counts as resolved even though it renders an empty cell. This is
consistent with Bartolo accepting declared columns for an empty result set.

Recognized but unsupported projection shapes do not resolve as paths. Unless an
exact top-level key wins first, existing any-row validation rejects them with
the normal `--columns` or `declared column` unknown-field error. For example:

- `settings.tools[]` has no element path.
- `groups[].tools[].key` contains more than one projection.

This feature adds no separate syntax-error category. Other bracketed text has
no special grammar and continues through ordinary property lookup and
unknown-field validation.

### Headers and output modes

The table header continues to use the selector exactly as supplied. For
example, `settings.tools[].key` renders through tablewriter as
`SETTINGS . TOOLS[] . KEY`; Bartolo does not invent a semantic alias such as
`TOOL KEYS`.

Projection applies only while rendering an interactive table. JSON, YAML,
TOON, raw output, and piped output retain the complete response. Existing
JMESPath behavior is unchanged: `--jmespath` remains the escape hatch for
filtering, indexing, aggregation, and arbitrary response restructuring.

## Examples

Given this row:

```json
{
  "key": "refund-agent",
  "settings": {
    "tools": [
      {"key": "lookup"},
      {"key": "refund"},
      {"key": null},
      {"id": "tool-without-key"},
      {"key": "policy"},
      {"key": "escalate"}
    ]
  }
}
```

this declaration:

```yaml
x-cli-list-fields:
  - key
  - settings.tools[].key
```

renders the projected cell as:

```text
lookup, refund, policy, …
```

The equivalent per-invocation override is:

```sh
orq agents list --columns key,settings.tools[].key
```

For transformations outside the selector grammar, callers continue to use
JMESPath:

```sh
orq agents list \
  --jmespath 'data[].{key: key, first_tool: settings.tools[0].key}'
```

## Architecture

The feature belongs in the runtime table-field resolver:

1. The generator already decodes `x-cli-list-fields` as `[]string` and passes
   every selector verbatim to `FormatList`.
2. `--columns` already produces the same requested-column list at runtime.
3. Both sources converge on `checkColumns`, `tableField`, and table-cell
   rendering.

`cli/formatter.go` will add a small selector parser/resolver around
`tableField`. It will retain the initial complete-key lookup, distinguish a
plain dotted path from a single valid projection, traverse normalized
`map[string]interface{}` and `[]interface{}` values, and return the projected
slice through the same `(interface{}, bool)` contract. Unsupported projection
shapes return `found == false`, allowing `checkColumns` to keep applying the
current declared-versus-user diagnostic prefix.

The implementation will not translate selectors to JMESPath. The table-column
contract intentionally differs from general JMESPath in exact literal-key
precedence, any-row validation, header spelling, and its narrow accepted
grammar.

No OpenAPI schema traversal is required. Recursive JSON/YAML normalization
already runs before table resolution, and existing list-cell formatting already
implements the desired output policy (`cli/formatter.go:592-600,697-793`).

## Compatibility

The following behavior must remain unchanged:

- Exact top-level keys, including literal keys containing dots or `[]`, win.
- Plain dotted object paths such as `model.id` resolve as before.
- Whole-array columns such as `settings.tools` keep their current rendering.
- A column is accepted when any returned row resolves it.
- Missing values in individual rows produce blank cells.
- JSON- and YAML-shaped maps behave identically after normalization.
- Declared and explicit columns are never removed to fit terminal width.
- A JMESPath projection causes columns to be inferred from its reshaped rows
  unless the invocation also provides `--columns`.
- Non-table output does not apply column selection.

## Testing strategy

Focused formatter tests will cover both declared columns and `--columns`:

- Projecting one, three, and four scalar values from arrays of objects.
- Preserving source order and applying the existing ellipsis and truncation.
- Traversing multiple object segments before and after `[]`.
- Omitting missing, null, and non-object elements.
- Treating an empty array as a resolved empty value.
- Treating a non-empty array with no surviving values as unresolved.
- Keeping a valid column across mixed rows and rendering unresolved rows blank.
- Rejecting a selector that no row resolves with the correct ownership prefix.
- Rejecting terminal and multiple projections through existing unknown-column
  diagnostics.
- Treating `[0]`, `[1]`, `[*]`, and `[ ]` as ordinary property-name text.
- Preferring a literal top-level key equal to the complete projected selector.
- Preserving plain nested paths, whole-array columns, and YAML normalization.

The generated-CLI integration fixture will declare an array element path in
`x-cli-list-fields`, return a representative array-of-objects payload, invoke
interactive table output, and assert that projected values appear. This closes
the current gap where the generated nested-column test invokes `-o json` and
therefore does not exercise table lookup.

Documentation tests or golden output will pin that `[]` selectors pass through
generation verbatim. Repository verification will run the focused formatter and
generated-command tests, `make verify`, the complete Go test suite, and
`git diff --check`.

## Alternatives considered

### Use the JMESPath evaluator for each column

This would provide projections, indexes, filters, and functions immediately,
but it would make `x-cli-list-fields` an expression language. It also conflicts
with literal-key precedence and existing validation semantics, while creating
new questions about expression results, column labels, and commas inside
`--columns`. Bartolo already offers full response-level JMESPath through
`--jmespath`, so this is not justified by the current requirements.

### Split once around `[]` and reuse object lookup on each side

This could be implemented with less initial structure, but it makes syntax
validation, error ownership, literal-key precedence, and future maintenance
harder to reason about. A small explicit parser keeps the accepted language and
failure modes visible.

### Add indexed element access

Selectors such as `tools[0].key` are intuitive but make schema-authored default
columns depend on array ordering and introduce another set of grammar and
out-of-range rules without a motivating RES-1574 use case. Indexing remains
available through `--jmespath` and is excluded from this feature.

## Explicit non-goals

- Count or aggregation forms such as `count(settings.tools)`.
- Indexed access such as `settings.tools[0].key`.
- Multiple array boundaries such as `groups[].tools[].key`.
- Filters, predicates, slices, wildcards, or arbitrary JMESPath expressions in
  a column selector.
- Terminal projections such as `settings.tools[]`.
- Automatic discovery of nested columns or automatic invention of `[]` paths.
- Nested collection roots such as `search.data` or an `x-cli-list-path`
  extension.
- OpenAPI schema validation of selector paths during generation.
- Changes to list classification, row extraction, terminal fitting, or
  machine-readable output.
- A speculative follow-up ticket for full JMESPath columns. That work should be
  opened only when a concrete declarative-column requirement is identified.

## Acceptance criteria

- `x-cli-list-fields: [key, settings.tools[].key]` generates a list command and
  renders tool keys as a normal abbreviated list cell.
- `--columns key,settings.tools[].key` produces the same table values and order.
- Empty, sparse, null-containing, and mixed-type arrays follow the resolution
  rules above without panics.
- Misspelled projected paths retain Bartolo's unknown-column protection.
- Exact literal keys and all existing dotted-object selectors remain backward
  compatible.
- Unsupported projection shapes fail with the existing source-specific
  unknown-column errors.
- All output modes other than interactive tables remain byte-for-byte governed
  by their existing formatting paths.
- Root and generated README documentation explain the syntax, behavior, and
  JMESPath boundary.
