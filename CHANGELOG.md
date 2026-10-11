# Changelog

All notable changes to Bartolo will be documented in this file.

The project was restarted on 2026-04-09 as a new public release stream under the Bartolo name.

## 2026-08-26

- Upgraded generated MCP servers to spec `2026-07-28` via `github.com/modelcontextprotocol/go-sdk` v1.7.0. Streamable HTTP is served in stateless mode so clients negotiate the new protocol instead of falling back to `2025-11-25`.
- Added a regeneration-safe MCP customization API: `mcpserver.Use` with `AddTools`, `RemoveTools`, `PatchTool`, and `WrapAll`, called from user-owned `cli/custom.Register`. The same tool set is served over stdio, Streamable HTTP, and `mcp list`.
- `--tools`, `--groups`, and `--read-only` are now persistent flags on `mcp`, so they apply to `mcp list` without being redeclared.

## 2026-07-12

- Added MCP (Model Context Protocol) server generation. Every generated CLI now has an `mcp` subcommand that exposes the API's operations as MCP tools for clients like Claude Code, Claude Desktop, and Cursor.
- Each eligible operation becomes a tool with a JSON Schema (2020-12) input schema (path/query/header params + JSON request body) and an output schema from the first 2xx JSON response. Results are returned as `structuredContent`, and upstream API errors surface as `isError` results with a structured `{status, body}` payload.
- Tool annotations (`readOnlyHint`, `destructiveHint`, `idempotentHint`) are derived from the HTTP method; outbound auth reuses the CLI's existing env vars and profiles.
- Added `mcp list` to print the tool manifest as JSON, runtime filtering flags (`--tools`, `--groups`, `--read-only`), and Streamable HTTP transport (`--http`) with inbound auth modes (`--http-auth none|bearer|oauth`, including RFC 9728 protected-resource metadata and a `<PREFIX>_MCP_TOKEN` static bearer token).
- SSE-only, multipart/binary, `deprecated`, and `x-mcp-ignore` operations are excluded from the tool surface (and reported in the generate summary); operations offering JSON and SSE are included using the JSON media type.
- Added generation targets: `bartolo init` now asks (or takes `--targets cli,mcp`) which surfaces to generate, recorded in `.bartolo.json` under `targets` and honored by `generate` / `sync` without re-prompting. `cli` only skips the MCP server (and prunes the MCP SDK dependency on tidy); `mcp` only produces an MCP-first binary whose bare invocation serves stdio directly while keeping `mcp`, `doctor`, and `auth setup`. Legacy configs without `targets` migrate on the next run (interactive prompt, or default-both with a warning when non-interactive).

## 2026-05-02 (v0.4.3)

- Bumped Bartolo to v0.4.3.
- Extended top-level `oneOf` / `anyOf` request bodies to union the properties of every branch into a single flag set. Endpoints like `chunking parse` (whose body is `oneOf` over multiple chunker strategies) now expose every strategy's fields as flags. Required fields are the intersection across branches; properties from earlier branches win on conflict.

## 2026-05-02 (v0.4.2)

- Bumped Bartolo to v0.4.2.
- Added a `json` body field flag fallback so nested objects, arrays of objects, and polymorphic unions are exposed as flags accepting a JSON value instead of being silently dropped. Endpoints like `deployments invoke` now expose every top-level field (e.g. `--messages`, `--invoke-options`, `--prefix-messages`, `--thread`, `--knowledge-filter`, `--identity`, `--documents`).

## 2026-05-02 (v0.4.1)

- Bumped Bartolo to v0.4.1.
- Added `bartolo version` command and `bartolo --version` flag so users can verify which generator binary they have on PATH.

## 2026-05-02 (v0.4.0)

- Bumped Bartolo to v0.4.0.
- Merged `allOf` compositions when extracting body field flags so endpoints whose top-level request schema is `allOf: [...]` (e.g. chat completions) now expose flags for every merged property instead of generating an empty flag list.

## 2026-05-02 (v0.3.0)

- Bumped Bartolo to v0.3.0.
- Expanded generated body field flags to cover nullable scalars (`string | null`, `type: [X, "null"]`, `anyOf` with null), repeatable arrays of scalars (`--tag a --tag b`), `additionalProperties` string maps (`--metadata key=value`), and string enums (with shell completion + value validation).
- Collapsed `anyOf` / `oneOf` shapes with a single non-null branch so they are exposed as flags instead of silently skipped.
- Nullable scalar flags accept the literal `null` to send an explicit JSON null.

## 2026-04-12

- Rebranded the project to `bartolo` with the module path `github.com/orq-ai/bartolo`.
- Added interactive `bartolo init`, including prompts for CLI name and default output format.
- Added generated CLI support for `default-format`, `doctor`, and raw `request`.
- Improved string escaping in generated Go code so large real-world specs compile cleanly.
- Added `make smoke` and `make verify` workflows for one-command local validation.
- Rewrote the root README around Bartolo's product positioning and operator workflow.

## 2026-04-11

- Added grouped command inference from tags, `x-cli-group`, and path-based fallbacks for untagged operations.
- Improved grouped verb synthesis for commands like `list`, `get`, `create-version`, and `query`.
- Added generated per-CLI README files with auth setup, first-run checks, and grouped command examples.
- Added predictable API key and bearer env var support for generated CLIs.

## 2026-04-10

- Added OpenAPI JSON input support alongside YAML.
- Added compatibility normalization for common OpenAPI 3.1 schema shapes such as numeric `exclusiveMinimum` and `exclusiveMaximum`.
- Added formatter and matcher regression tests around agent-oriented output paths.

## 2026-04-09

- Started Bartolo as an agent-friendly OpenAPI-to-CLI generator focused on publishable product CLIs instead of raw endpoint wrappers.
